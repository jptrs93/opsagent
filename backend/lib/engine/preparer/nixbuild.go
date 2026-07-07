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
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/goutil/cmdu"
	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
)

// NixBuilder holds the shared Git/Nix command helpers used by NixDockerBuilder.
// A semaphore limits concurrency to one nix invocation at a time so simultaneous
// deploys don't thrash the Nix store.
type NixBuilder struct {
	dataDir string
	sem     chan struct{} // capacity 1: one build at a time
	Git     *GitManager
}

func NewNixBuilder(dataDir string, provider githubcredentials.Provider) *NixBuilder {
	return &NixBuilder{
		dataDir: dataDir,
		sem:     make(chan struct{}, 1),
		Git:     NewGitManager(dataDir, provider),
	}
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
