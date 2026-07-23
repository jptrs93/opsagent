// Package nixdocker builds Nix-produced OCI image streams and imports them into
// OpenDeploy's containerd image store.
package nixdocker

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/goutil/cmdu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/preparerlog"
	repogit "github.com/jptrs93/opsagent/backend/lib/repo/git"
)

// Preparer builds a Nix flake whose default output is an executable OCI image
// stream, imports the stream into containerd, and returns the local image ref.
type Preparer struct {
	gitManager *repogit.Manager
	sem        chan struct{}
	imageReady func(context.Context, string) error
}

const imageCacheSchemaVersion = "v1"

// New creates a Nix Docker preparer. Builds are limited to one concurrent Nix
// invocation per Preparer instance to avoid thrashing the Nix store.
func New(gitManager *repogit.Manager) *Preparer {
	return &Preparer{
		gitManager: gitManager,
		sem:        make(chan struct{}, 1),
		imageReady: ctrd.Default.ImageReady,
	}
}

func (p *Preparer) Prepare(ctx context.Context, dep *apigen.DeploymentConfig, log *preparerlog.Log) (string, apigen.PreparationStatus) {
	version := dep.WorkloadVersion()
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return "", apigen.PreparationStatus_FAILED
	}

	nix := dep.Spec.Container().Source.NixDockerBuild
	localImageRef := imageRef(nix, version)
	log.Write("checking for reusable image %s", localImageRef)
	if err := p.imageReady(ctx, localImageRef); err == nil {
		log.Write("reusing existing image %s", localImageRef)
		return localImageRef, apigen.PreparationStatus_READY
	} else if !errors.Is(err, ctrd.ErrImageUnavailable) {
		log.Error("checking reusable image: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	log.Write("reusable image not found; building %s", localImageRef)

	logPath := dep.PrepareOutputPath()
	slog.InfoContext(ctx, "nix docker build starting", "log_path", logPath)
	log.Write("checking out repository %s at version %s", nix.Repo, version)
	checkoutStarted := time.Now()
	repoDir, err := p.gitManager.EnsureCheckout(ctx, nix.Repo, version, log.Output())
	if err != nil {
		log.Error("checking out repository: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	log.Write("checkout complete in %s: %s", time.Since(checkoutStarted).Round(time.Millisecond), repoDir)

	flakePath, err := checkedOutFlakePath(repoDir, nix.Flake)
	if err != nil {
		log.Error("validating flake path: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	nixDir := filepath.Dir(flakePath)
	log.Write("running Nix build in %s", nixDir)
	buildStarted := time.Now()
	stdoutLines, err := runCmdCapture(ctx, nixDir, log, "nix", nixBuildArgs(nix.Target)...)
	if err != nil {
		log.Error("running Nix build: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}

	artifactPath := lastNonEmptyLine(stdoutLines)
	log.Write("build complete in %s, stream artifact: %s", time.Since(buildStarted).Round(time.Millisecond), artifactPath)
	if artifactPath == "" {
		log.Error("Nix build returned an empty artifact path")
		return "", apigen.PreparationStatus_FAILED
	}
	streamPath, err := resolveImageStreamPath(artifactPath)
	if err != nil {
		log.Error("resolving image stream: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}

	log.Write("importing image stream %s as %s", streamPath, localImageRef)
	imageStreamStarted := time.Now()
	if err := p.importStream(ctx, streamPath, localImageRef, log); err != nil {
		log.Error("importing image: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	log.Write("image stream export/import complete in %s", time.Since(imageStreamStarted).Round(time.Millisecond))
	imageSize, err := ctrd.Default.ImageSize(ctx, localImageRef)
	if err != nil {
		log.Write("image import complete: %s (size unavailable: %v)", localImageRef, err)
	} else {
		log.Write("image import complete: %s (size: %s)", localImageRef, formatImageSize(imageSize))
	}

	return localImageRef, apigen.PreparationStatus_READY
}

func nixBuildArgs(target string) []string {
	args := []string{
		"--extra-experimental-features", "nix-command flakes",
		"build", "--no-update-lock-file", "--no-link", "--print-out-paths", "-L",
	}
	if target != "" {
		args = append(args, target)
	}
	return args
}

func (p *Preparer) importStream(ctx context.Context, streamPath string, localImageRef string, log *preparerlog.Log) error {
	cmd := exec.CommandContext(ctx, streamPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("opening stream stdout: %w", err)
	}
	cmd.Stderr = log.Output()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting image stream: %w", err)
	}
	_, importErr := ctrd.Default.Import(ctx, ctrd.ImageStream{Reader: stdout, Ref: localImageRef})
	if importErr != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return importErr
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("image stream exited: %w", err)
	}
	return nil
}

func runCmdCapture(ctx context.Context, dir string, log *preparerlog.Log, name string, args ...string) ([]string, error) {
	cmdStr := sanitizeCommandForLogs(name, args)
	slog.InfoContext(ctx, "exec", "cmd", cmdStr, "dir", dir)
	log.Write("running command: %s", cmdStr)

	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}

	stdout, stderr, _, closePipes, err := cmdu.InitStdPipes(cmd)
	if err != nil {
		slog.ErrorContext(ctx, "initializing std pipes failed", "cmd", cmdStr, "err", err)
		return nil, fmt.Errorf("initializing std pipes: %w", err)
	}
	defer closePipes()

	if err := cmd.Start(); err != nil {
		slog.ErrorContext(ctx, "cmd start failed", "cmd", cmdStr, "err", err)
		log.Error("starting command: %v", err)
		return nil, fmt.Errorf("start %s: %w", cmdStr, err)
	}
	slog.InfoContext(ctx, "cmd started", "cmd", cmdStr, "pid", cmd.Process.Pid)

	stopCancellationWatch := watchCommandCancellation(ctx, cmd, cmdStr, log)
	defer stopCancellationWatch()

	var mu sync.Mutex
	var stdoutLines []string
	var wg sync.WaitGroup
	streamPipe := func(prefix string, r io.Reader, capture bool) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)
		for scanner.Scan() {
			line := scanner.Text()
			_, _ = fmt.Fprintln(log.Output(), line)
			if capture {
				mu.Lock()
				stdoutLines = append(stdoutLines, line)
				mu.Unlock()
			}
		}
		if scanErr := scanner.Err(); scanErr != nil {
			slog.ErrorContext(ctx, "scanner error", "cmd", cmdStr, "stream", prefix, "err", scanErr)
			log.Error("reading command %s: %v", prefix, scanErr)
		}
	}

	wg.Add(2)
	go streamPipe("stdout", stdout, true)
	go streamPipe("stderr", stderr, false)
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		if isContextDone(ctx.Err()) {
			return stdoutLines, ctx.Err()
		}
		exitErr := fmt.Sprintf("cmd failed: %s: %v", cmdStr, err)
		slog.ErrorContext(ctx, exitErr)
		log.Error("%s", exitErr)
		return stdoutLines, fmt.Errorf("%s: %w", cmdStr, err)
	}
	slog.InfoContext(ctx, "cmd completed", "cmd", cmdStr)
	return stdoutLines, nil
}

func watchCommandCancellation(ctx context.Context, cmd *exec.Cmd, cmdStr string, log *preparerlog.Log) func() {
	done := make(chan struct{})
	var once sync.Once

	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process == nil {
				return
			}
			slog.WarnContext(ctx, "interrupting command due to cancellation", "cmd", cmdStr)
			log.Write("interrupting command due to cancellation")
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				slog.WarnContext(ctx, "failed to send interrupt signal", "cmd", cmdStr, "err", err)
			}

			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			select {
			case <-done:
			case <-timer.C:
				slog.WarnContext(ctx, "force killing command after interrupt grace period", "cmd", cmdStr)
				log.Write("force killing command after interrupt grace period")
				if err := cmd.Process.Kill(); err != nil {
					slog.WarnContext(ctx, "failed to kill command", "cmd", cmdStr, "err", err)
				}
			}
		case <-done:
		}
	}()

	return func() {
		once.Do(func() { close(done) })
	}
}

func isContextDone(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func checkedOutFlakePath(repoDir string, flake string) (string, error) {
	clean, err := repogit.CleanFlakePath(flake)
	if err != nil {
		return "", err
	}
	path := filepath.Join(repoDir, clean)
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("flake file not found at %s: %w", clean, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("flake path is not a regular file: %s", clean)
	}
	return path, nil
}

func resolveImageStreamPath(artifactPath string) (string, error) {
	info, err := os.Stat(artifactPath)
	if err != nil {
		return "", fmt.Errorf("stat artifact path: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("artifact path is a directory, expected executable image stream: %s", artifactPath)
	}
	if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("artifact path is not executable: %s", artifactPath)
	}
	return artifactPath, nil
}

func lastNonEmptyLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}

func formatImageSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(size)
	for _, suffix := range units {
		value /= unit
		if value < unit || suffix == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%d B", size)
}

func imageRef(nix *apigen.NixDockerBuild, version string) string {
	return fmt.Sprintf(
		"opendeploy.local/nix-docker-build/%s/%s:%s",
		imageCacheSchemaVersion,
		imageSourceKey(nix, runtime.GOOS, runtime.GOARCH),
		sanitizeImageTag(strings.ToLower(version)),
	)
}

func imageSourceKey(nix *apigen.NixDockerBuild, goos, goarch string) string {
	h := sha256.New()
	for _, value := range []string{nix.Repo, nix.Flake, nix.Target, goos, goarch} {
		_, _ = fmt.Fprintf(h, "%d:", len(value))
		_, _ = io.WriteString(h, value)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sanitizeImageTag(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		if isASCIITagChar(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
		if b.Len() >= 128 {
			break
		}
	}
	out := b.String()
	if out == "" {
		return "unknown"
	}
	if !isASCIITagStart(rune(out[0])) {
		out = "v" + out
	}
	if len(out) > 128 {
		out = out[:128]
	}
	return out
}

func isASCIITagChar(r rune) bool {
	return isASCIITagStart(r) || r == '.' || r == '-'
}

func isASCIITagStart(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}

func sanitizeCommandForLogs(name string, args []string) string {
	if len(args) == 0 {
		return name
	}
	safeArgs := make([]string, 0, len(args))
	for _, arg := range args {
		safeArgs = append(safeArgs, redactGithubToken(arg))
	}
	return name + " " + strings.Join(safeArgs, " ")
}

func redactGithubToken(s string) string {
	const prefix = "x-access-token:"
	idx := strings.Index(s, prefix)
	if idx == -1 {
		return s
	}
	afterPrefix := idx + len(prefix)
	atIdx := strings.Index(s[afterPrefix:], "@")
	if atIdx == -1 {
		return s
	}
	atIdx += afterPrefix
	return s[:afterPrefix] + "***" + s[atIdx:]
}
