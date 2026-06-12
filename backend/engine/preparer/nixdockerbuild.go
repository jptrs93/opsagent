package preparer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/credentials"
	"github.com/jptrs93/opsagent/backend/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/storage"
)

// NixDockerBuilder builds a Nix flake whose default output is an executable
// OCI/Docker image stream (for example dockerTools.streamLayeredImage), imports
// that stream into OpenDeploy's containerd, and returns the local image ref.
type NixDockerBuilder struct {
	*NixBuilder
	client *ctrd.Client
}

func NewNixDockerBuilder(dataDir string, provider credentials.GithubCredentialsProvider, client *ctrd.Client) *NixDockerBuilder {
	return &NixDockerBuilder{NixBuilder: NewNixBuilder(dataDir, provider), client: client}
}

func (b *NixDockerBuilder) start(store storage.OperatorStore, dep *apigen.DeploymentConfig) Preparer {
	ctx, cancel := context.WithCancel(context.Background())
	p := &activePreparer{cancel: cancel, done: make(chan struct{}), deploymentConfigVersion: dep.Version}

	version := desiredVersion(dep)
	if version == "" {
		cancel()
		writePrepareStatus(store, dep, "", apigen.PreparationStatus_FAILED)
		close(p.done)
		return p
	}

	go func() {
		defer close(p.done)
		select {
		case b.sem <- struct{}{}:
			defer func() { <-b.sem }()
		case <-ctx.Done():
			writePrepareStatus(store, dep, "", apigen.PreparationStatus_FAILED)
			return
		}
		artifact, status := b.runBuild(ctx, store, dep, version)
		writePrepareStatus(store, dep, artifact, status)
	}()

	return p
}

func (b *NixDockerBuilder) runBuild(ctx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, version string) (string, apigen.PreparationStatus) {
	logPath := dep.PrepareOutputPath()
	slog.InfoContext(ctx, "nix docker build starting", "log_path", logPath)
	writePrepareStatus(store, dep, "", apigen.PreparationStatus_PREPARING)

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

	if b.client == nil || !b.client.Supported() {
		writeLog("ERROR containers are not supported on this platform (linux + containerd required)")
		return "", apigen.PreparationStatus_FAILED
	}

	nix := dep.Spec.Prepare.NixDockerBuild
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

	artifactPath := lastNonEmptyLine(stdoutLines)
	writeLog("build complete, stream artifact: %s", artifactPath)
	if artifactPath == "" {
		writeLog("ERROR empty artifact path")
		return "", apigen.PreparationStatus_FAILED
	}
	streamPath, err := resolveImageStreamPath(artifactPath)
	if err != nil {
		writeLog("ERROR resolving image stream: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}

	imageRef := nixDockerImageRef(dep.ID, version)
	writeLog("importing image stream %s as %s", streamPath, imageRef)
	if err := b.importStream(ctx, streamPath, imageRef, logFile); err != nil {
		writeLog("ERROR image import failed: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	writeLog("image import complete: %s", imageRef)

	return imageRef, apigen.PreparationStatus_READY
}

func (b *NixDockerBuilder) importStream(ctx context.Context, streamPath string, imageRef string, logFile io.Writer) error {
	cmd := exec.CommandContext(ctx, streamPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("opening stream stdout: %w", err)
	}
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting image stream: %w", err)
	}
	_, importErr := b.client.Import(ctx, ctrd.ImageStream{Reader: stdout, Ref: imageRef})
	if importErr != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return importErr
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		return fmt.Errorf("image stream exited: %w", waitErr)
	}
	return nil
}

func resolveImageStreamPath(artifactPath string) (string, error) {
	info, err := os.Stat(artifactPath)
	if err != nil {
		return "", fmt.Errorf("stat artifact path: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("artifact path is a directory, expected executable image stream: %s", artifactPath)
	}
	if !isExecutableFile(info.Mode()) {
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

func nixDockerImageRef(deploymentID int32, version string) string {
	return fmt.Sprintf("opendeploy.local/nix-docker-build/%d:%s", deploymentID, sanitizeImageTag(version))
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
