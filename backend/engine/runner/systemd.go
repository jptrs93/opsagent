package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

// systemdRunner monitors a systemd unit by polling `systemctl is-active`.
// It never restarts or installs anything — systemd owns process lifecycle.
// The install+restart path is a one-shot in newSystemdRunnerWithRestart
// before entering the same monitor loop.
type systemdRunner struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	store storage.OperatorStore
	id    *apigen.DeploymentIdentifier
	seqNo int32

	unit       string
	binPath    string
	artifact   string
	outputPath string
}

// newSystemdMonitor creates a monitor-only runner. Used by ReAttach.
func newSystemdMonitor(parentCtx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, prev *apigen.RunnerStatus) *systemdRunner {
	ctx, cancel := context.WithCancel(parentCtx)
	sys := dep.Spec.Runner.Systemd
	r := &systemdRunner{
		ctx:        ctx,
		cancel:     cancel,
		done:       make(chan struct{}),
		store:      store,
		id:         dep.ID,
		seqNo:      prev.DeploymentSeqNo,
		unit:       normalizeUnit(sys.Name),
		artifact:   prev.RunningArtifact,
		outputPath: dep.RunOutputPath(),
	}
	go r.monitor()
	return r
}

// newSystemdRunnerWithRestart installs the prepared artifact, issues a
// systemd restart, writes the new status, then enters the monitor loop.
// Called only from runner.Create when the operator has a new artifact ready.
// No retries — if install or restart fails, it writes CRASHED and exits.
func newSystemdRunnerWithRestart(parentCtx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, artifact string) *systemdRunner {
	ctx, cancel := context.WithCancel(parentCtx)
	sys := dep.Spec.Runner.Systemd
	r := &systemdRunner{
		ctx:        ctx,
		cancel:     cancel,
		done:       make(chan struct{}),
		store:      store,
		id:         dep.ID,
		seqNo:      dep.SeqNo,
		unit:       normalizeUnit(sys.Name),
		binPath:    sys.BinPath,
		artifact:   artifact,
		outputPath: dep.RunOutputPath(),
	}
	go r.installAndMonitor()
	return r
}

func (r *systemdRunner) SeqNo() int32 { return r.seqNo }

// Stop cancels the monitor goroutine. It does NOT stop the systemd unit.
func (r *systemdRunner) Stop() {
	r.cancel()
	<-r.done
}

func (r *systemdRunner) installAndMonitor() {
	defer close(r.done)

	r.writeStatus(apigen.RunningStatus_STARTING, 0)

	if err := atomicSymlink(r.artifact, r.binPath); err != nil {
		slog.ErrorContext(r.ctx, "symlinking artifact failed", "err", err)
		r.appendOutput("symlink failed: %s\n", err)
		r.writeStatus(apigen.RunningStatus_CRASHED, 0)
		return
	}
	r.appendOutput("symlinked %s -> %s\n", r.binPath, r.artifact)

	out, err := systemctlRestart(r.ctx, r.unit)
	if err != nil {
		slog.ErrorContext(r.ctx, "systemctl restart failed", "err", err, "unit", r.unit)
		r.appendOutput("restart failed: %s\n%s\n", err, out)
		r.writeStatus(apigen.RunningStatus_CRASHED, 0)
		return
	}
	r.appendOutput("restart issued\n")

	r.monitorLoop()
}

func (r *systemdRunner) monitor() {
	defer close(r.done)
	r.monitorLoop()
}

func (r *systemdRunner) monitorLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var last apigen.RunningStatus
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			active, err := systemctlIsActive(r.ctx, r.unit)
			if err != nil {
				slog.WarnContext(r.ctx, "systemctl is-active failed", "unit", r.unit, "err", err)
				continue
			}
			status := mapActiveState(active)
			if status == last {
				continue
			}
			last = status
			pid, _ := systemctlMainPID(r.ctx, r.unit)
			r.writeStatus(status, pid)
		}
	}
}

func (r *systemdRunner) writeStatus(status apigen.RunningStatus, pid int) {
	r.store.MustWriteDeploymentStatus(context.Background(), *r.id, func(s *apigen.DeploymentStatus) {
		if s.Runner != nil && s.Runner.DeploymentSeqNo > r.seqNo {
			return
		}
		s.StatusSeqNo++
		s.Timestamp = time.Now()
		s.DeploymentID = r.id
		s.Runner = &apigen.RunnerStatus{
			DeploymentSeqNo: r.seqNo,
			Status:          status,
			RunningPid:      int32(pid),
			RunningArtifact: r.artifact,
		}
	})
}

func (r *systemdRunner) appendOutput(format string, args ...any) {
	if r.outputPath == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(r.outputPath), 0o755)
	f, err := os.OpenFile(r.outputPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, format, args...)
}

// --- helpers ---

func normalizeUnit(name string) string {
	if !strings.HasSuffix(name, ".service") {
		return name + ".service"
	}
	return name
}

func binMatchesArtifact(binPath, artifactPath string) (bool, error) {
	if binPath == "" || artifactPath == "" {
		return false, nil
	}
	binInfo, err := os.Stat(binPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	artInfo, err := os.Stat(artifactPath)
	if err != nil {
		return false, err
	}
	if os.SameFile(binInfo, artInfo) {
		return true, nil
	}
	if binInfo.Size() != artInfo.Size() {
		return false, nil
	}
	return fileContentsEqual(binPath, artifactPath)
}

func fileContentsEqual(a, b string) (bool, error) {
	fa, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer fb.Close()

	const chunk = 32 * 1024
	bufA := make([]byte, chunk)
	bufB := make([]byte, chunk)
	for {
		nA, errA := io.ReadFull(fa, bufA)
		nB, errB := io.ReadFull(fb, bufB)
		if nA != nB || string(bufA[:nA]) != string(bufB[:nB]) {
			return false, nil
		}
		if errA == io.EOF && errB == io.EOF {
			return true, nil
		}
		if errA != nil && errA != io.ErrUnexpectedEOF {
			return false, errA
		}
		if errB != nil && errB != io.ErrUnexpectedEOF {
			return false, errB
		}
	}
}

func atomicSymlink(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating bin dir: %w", err)
	}
	// Create a temp symlink, then atomically rename over dst.
	tmpLink := dst + ".new"
	_ = os.Remove(tmpLink)
	if err := os.Symlink(src, tmpLink); err != nil {
		return fmt.Errorf("creating symlink: %w", err)
	}
	if err := os.Rename(tmpLink, dst); err != nil {
		_ = os.Remove(tmpLink)
		return fmt.Errorf("atomic rename: %w", err)
	}
	return nil
}

func systemctlRestart(ctx context.Context, unit string) (string, error) {
	if ainit.Config.IsLocalDev == "true" {
		return systemctl(ctx, "restart", unit)
	}
	cmd := exec.CommandContext(ctx, "sudo", "-n",
		"/usr/bin/systemd-run", "--no-block",
		"/usr/bin/systemctl", "restart", unit)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)),
			fmt.Errorf("sudo systemd-run restart %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func systemctl(ctx context.Context, args ...string) (string, error) {
	var cmd *exec.Cmd
	if ainit.Config.IsLocalDev == "true" {
		cmd = exec.CommandContext(ctx, "systemctl", args...)
	} else {
		sudoArgs := append([]string{"-n", "/usr/bin/systemctl"}, args...)
		cmd = exec.CommandContext(ctx, "sudo", sudoArgs...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)),
			fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func systemctlIsActive(ctx context.Context, unit string) (string, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "is-active", unit).Output()
	state := strings.TrimSpace(string(out))
	if state != "" {
		return state, nil
	}
	if err != nil {
		return "", err
	}
	return state, nil
}

func systemctlMainPID(ctx context.Context, unit string) (int, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "show", unit, "--property=MainPID", "--value").Output()
	if err != nil {
		return 0, err
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &pid); err != nil {
		return 0, err
	}
	return pid, nil
}

func mapActiveState(state string) apigen.RunningStatus {
	switch state {
	case "active", "reloading":
		return apigen.RunningStatus_RUNNING
	case "activating":
		return apigen.RunningStatus_STARTING
	case "deactivating", "inactive":
		return apigen.RunningStatus_STOPPED
	case "failed":
		return apigen.RunningStatus_CRASHED
	default:
		return apigen.RunningStatus_CRASHED
	}
}
