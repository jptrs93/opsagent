package preparer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/goutil/cmdu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

// NixBuilder clones a repo, checks out a specific git version, and runs
// `nix build` to produce an executable artifact. A semaphore limits
// concurrency to one nix invocation at a time so simultaneous deploys
// don't thrash the Nix store.
type NixBuilder struct {
	dataDir     string
	githubToken string
	sem         chan struct{} // capacity 1: one build at a time
	Git         *GitManagerImpl
}

func NewNixBuilder(dataDir string, githubToken string) *NixBuilder {
	return &NixBuilder{
		dataDir:     dataDir,
		githubToken: githubToken,
		sem:         make(chan struct{}, 1),
		Git:         NewGitManager(dataDir, githubToken),
	}
}

func (b *NixBuilder) start(parentCtx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig) Preparer {
	ctx, cancel := context.WithCancel(parentCtx)
	p := &activePreparer{cancel: cancel, done: make(chan struct{}), deploymentConfigVersion: dep.Version}

	version := desiredVersion(dep)
	if version == "" {
		cancel()
		writePrepareStatus(ctx, store, dep, "", apigen.PreparationStatus_FAILED)
		close(p.done)
		return p
	}

	go func() {
		defer close(p.done)
		select {
		case b.sem <- struct{}{}:
			defer func() { <-b.sem }()
		case <-ctx.Done():
			writePrepareStatus(ctx, store, dep, "", apigen.PreparationStatus_FAILED)
			return
		}
		artifact, status := b.runBuild(ctx, store, dep, version)
		writePrepareStatus(ctx, store, dep, artifact, status)
	}()

	return p
}

func (b *NixBuilder) runBuild(ctx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, version string) (string, apigen.PreparationStatus) {
	logPath := dep.PrepareOutputPath()
	slog.InfoContext(ctx, "build starting", "log_path", logPath)
	writePrepareStatus(ctx, store, dep, "", apigen.PreparationStatus_PREPARING)

	logFile, err := os.Create(logPath)
	if err != nil {
		slog.ErrorContext(ctx, "creating prepare log file failed", "path", logPath, "err", err)
		return "", apigen.PreparationStatus_FAILED
	}
	defer logFile.Close()

	writeLog := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		slog.InfoContext(ctx, msg)
		fmt.Fprintf(logFile, "==> %s\n", msg)
	}

	nix := dep.Spec.Prepare.NixBuild

	repoDir := filepath.Join(b.dataDir, "repos", nix.Repo)
	writeLog("repo dir: %s", repoDir)

	writeLog("ensuring repo %s", nix.Repo)
	if err := b.ensureRepo(ctx, repoDir, nix.Repo, logFile); err != nil {
		writeLog("ERROR git clone/fetch failed: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	writeLog("repo ready")

	writeLog("checking out version %s", version)
	if err := b.runCmd(ctx, repoDir, logFile, "git", "reset", "--hard"); err != nil {
		writeLog("ERROR git reset --hard failed: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	if err := b.runCmd(ctx, repoDir, logFile, "git", "clean", "-fdx"); err != nil {
		writeLog("ERROR git clean failed: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	if err := b.runCmd(ctx, repoDir, logFile, "git", "checkout", version); err != nil {
		writeLog("ERROR git checkout failed: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	writeLog("checkout complete")

	nixDir := filepath.Join(repoDir, filepath.Dir(nix.Flake))

	writeLog("running nix build in %s", nixDir)
	stdoutLines, err := b.runCmdCapture(ctx, nixDir, logFile, "nix", "--extra-experimental-features", "nix-command flakes", "build", "--no-link", "--print-out-paths", "-L")
	if err != nil {
		writeLog("ERROR nix build failed: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}

	artifactPath := ""
	for i := len(stdoutLines) - 1; i >= 0; i-- {
		if strings.TrimSpace(stdoutLines[i]) != "" {
			artifactPath = strings.TrimSpace(stdoutLines[i])
			break
		}
	}

	writeLog("build complete, artifact: %s", artifactPath)
	if artifactPath == "" {
		writeLog("empty artifact path %s", artifactPath)
		return "", apigen.PreparationStatus_FAILED
	}

	execPath, err := resolveExecPath(artifactPath, nix.OutputExecutable)
	if err != nil {
		writeLog("ERROR resolving executable: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	writeLog("resolved executable: %s", execPath)

	return execPath, apigen.PreparationStatus_READY
}

func (b *NixBuilder) ensureRepo(ctx context.Context, repoDir string, repoURL string, logFile io.Writer) error {
	cloneURL := b.resolveCloneURL(repoURL)

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil {
		fmt.Fprintf(logFile, "[%s] fetching latest for %s\n", time.Now().Format(time.RFC3339), repoURL)
		return b.runCmd(ctx, repoDir, logFile, "git", "fetch", "--all")
	}

	fmt.Fprintf(logFile, "[%s] cloning %s into %s\n", time.Now().Format(time.RFC3339), repoURL, repoDir)
	if err := os.MkdirAll(filepath.Dir(repoDir), 0o755); err != nil {
		return fmt.Errorf("creating repo dir: %w", err)
	}
	return b.runCmd(ctx, "", logFile, "git", "clone", cloneURL, repoDir)
}

func (b *NixBuilder) resolveCloneURL(repoURL string) string {
	if b.githubToken != "" {
		return fmt.Sprintf("https://x-access-token:%s@%s.git", b.githubToken, repoURL)
	}
	return fmt.Sprintf("https://%s.git", repoURL)
}

func resolveExecPath(artifactPath string, outputExecutable string) (string, error) {
	artifactInfo, err := os.Stat(artifactPath)
	if err != nil {
		return "", fmt.Errorf("stat artifact path: %w", err)
	}

	if !artifactInfo.IsDir() {
		if !isExecutableFile(artifactInfo.Mode()) {
			return "", fmt.Errorf("artifact path is not executable: %s", artifactPath)
		}
		return artifactPath, nil
	}

	binDir := filepath.Join(artifactPath, "bin")
	binEntries, err := os.ReadDir(binDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("artifact path has no bin directory: %s", artifactPath)
		}
		return "", fmt.Errorf("reading bin dir: %w", err)
	}

	if outputExecutable != "" {
		if filepath.Base(outputExecutable) != outputExecutable {
			return "", fmt.Errorf("configured outputExecutable must be a file name: %q", outputExecutable)
		}

		candidate := filepath.Join(binDir, outputExecutable)
		info, statErr := os.Stat(candidate)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return "", fmt.Errorf("configured executable %q not found in artifact bin dir: %s", outputExecutable, binDir)
			}
			return "", fmt.Errorf("stat configured executable %q: %w", outputExecutable, statErr)
		}
		if info.IsDir() {
			return "", fmt.Errorf("configured executable %q is a directory in artifact bin dir: %s", outputExecutable, binDir)
		}
		if !isExecutableFile(info.Mode()) {
			return "", fmt.Errorf("configured executable %q is not executable in artifact bin dir: %s", outputExecutable, binDir)
		}
		return candidate, nil
	}

	executables := make([]string, 0, len(binEntries))
	for _, entry := range binEntries {
		candidate := filepath.Join(binDir, entry.Name())
		info, infoErr := os.Stat(candidate)
		if infoErr != nil || info.IsDir() {
			continue
		}
		if isExecutableFile(info.Mode()) {
			executables = append(executables, candidate)
		}
	}

	if len(executables) == 0 {
		return "", fmt.Errorf("no executable found in artifact bin dir: %s", binDir)
	}
	if len(executables) > 1 {
		return "", fmt.Errorf("multiple executables found in artifact bin dir: %s", binDir)
	}

	return executables[0], nil
}

func isExecutableFile(mode os.FileMode) bool {
	return mode&0o111 != 0
}

func (b *NixBuilder) runCmd(ctx context.Context, dir string, logWriter io.Writer, name string, args ...string) error {
	_, err := b.runCmdCapture(ctx, dir, logWriter, name, args...)
	return err
}

func (b *NixBuilder) runCmdCapture(ctx context.Context, dir string, logWriter io.Writer, name string, args ...string) ([]string, error) {
	cmdStr := sanitizeCommandForLogs(name, args)
	slog.InfoContext(ctx, "exec", "cmd", cmdStr, "dir", dir)
	fmt.Fprintf(logWriter, "$ %s\n", cmdStr)

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
		fmt.Fprintf(logWriter, "ERROR start failed: %v\n", err)
		return nil, fmt.Errorf("start %s: %w", cmdStr, err)
	}
	slog.InfoContext(ctx, "cmd started", "cmd", cmdStr, "pid", cmd.Process.Pid)

	stopCancellationWatch := watchCommandCancellation(ctx, cmd, cmdStr, logWriter)
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
			fmt.Fprintf(logWriter, "%s\n", line)
			if capture {
				mu.Lock()
				stdoutLines = append(stdoutLines, line)
				mu.Unlock()
			}
		}
		if err := scanner.Err(); err != nil {
			slog.ErrorContext(ctx, "scanner error", "cmd", cmdStr, "stream", prefix, "err", err)
			fmt.Fprintf(logWriter, "ERROR reading %s: %v\n", prefix, err)
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
		fmt.Fprintf(logWriter, "ERROR %s\n", exitErr)
		return stdoutLines, fmt.Errorf("%s: %w", cmdStr, err)
	}
	slog.InfoContext(ctx, "cmd completed", "cmd", cmdStr)
	return stdoutLines, nil
}

func isContextDone(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func watchCommandCancellation(ctx context.Context, cmd *exec.Cmd, cmdStr string, logWriter io.Writer) func() {
	done := make(chan struct{})
	var once sync.Once

	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process == nil {
				return
			}

			slog.WarnContext(ctx, "interrupting command due to cancellation", "cmd", cmdStr)
			fmt.Fprintf(logWriter, "==> interrupting command due to cancellation\n")

			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				slog.WarnContext(ctx, "failed to send interrupt signal", "cmd", cmdStr, "err", err)
			}

			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()

			select {
			case <-done:
			case <-timer.C:
				slog.WarnContext(ctx, "force killing command after interrupt grace period", "cmd", cmdStr)
				fmt.Fprintf(logWriter, "==> force killing command after interrupt grace period\n")
				if err := cmd.Process.Kill(); err != nil {
					slog.WarnContext(ctx, "failed to kill command", "cmd", cmdStr, "err", err)
				}
			}
		case <-done:
		}
	}()

	return func() {
		once.Do(func() {
			close(done)
		})
	}
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

	atIdx = afterPrefix + atIdx
	return s[:afterPrefix] + "***" + s[atIdx:]
}
