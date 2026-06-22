package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

// systemdRunner monitors a systemd unitName by polling `systemctl is-active`.
type systemdRunner struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	store        storage.OperatorStore
	deploymentID int32

	status apigen.RunnerStatus

	unitName    string
	unitBinPath string
}

// reAttachSystemdRunner creates a monitor-only runner. Used by ReAttach.
func reAttachSystemdRunner(store storage.OperatorStore, dep *apigen.DeploymentConfig, runnerStatus apigen.RunnerStatus) *systemdRunner {
	ctx, cancel := context.WithCancel(context.Background())
	sys := dep.Spec.Runner.Systemd
	r := &systemdRunner{
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
		store:        store,
		deploymentID: dep.ID,
		status:       runnerStatus,
		unitName:     normalizeUnit(sys.Name),
	}
	go r.monitor()
	return r
}

// observeExistingSystemdRunner is the first-install path for OpenDeploy's own
// systemd deployment. It does not restart or install anything; it only observes
// the already-running unit and publishes the first real status from systemd.
func observeExistingSystemdRunner(store storage.OperatorStore, dep *apigen.DeploymentConfig) *systemdRunner {
	ctx, cancel := context.WithCancel(context.Background())
	sys := dep.Spec.Runner.Systemd
	r := &systemdRunner{
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
		store:        store,
		deploymentID: dep.ID,
		status: apigen.RunnerStatus{
			DeploymentConfigVersion: dep.Version,
			RunningArtifact:         resolveSystemdRunnerArtifact(sys.BinPath),
			Status:                  apigen.RunningStatus_STARTING,
		},
		unitName: normalizeUnit(sys.Name),
	}
	go r.monitor()
	return r
}

// newSystemdRunnerWithRestart installs the prepared artifact, issues a
// systemd restart, writes the new status, then enters the monitor loop.
// Called only from runner.Create when the operator has a new artifact ready.
// No retries — if install or restart fails, it writes CRASHED and exits.
func newSystemdRunnerWithRestart(store storage.OperatorStore, dep *apigen.DeploymentConfig, preparerStatus apigen.PreparerStatus) *systemdRunner {
	ctx, cancel := context.WithCancel(context.Background())
	sys := dep.Spec.Runner.Systemd
	r := &systemdRunner{
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
		store:        store,
		deploymentID: dep.ID,
		status: apigen.RunnerStatus{
			DeploymentConfigVersion: preparerStatus.DeploymentConfigVersion,
			RunningPid:              0,
			RunningArtifact:         preparerStatus.Artifact,
			Status:                  apigen.RunningStatus_STARTING,
			NumberOfRestarts:        0,
			LastRestartAt:           time.Now(),
		},
		unitName:    normalizeUnit(sys.Name),
		unitBinPath: sys.BinPath,
	}
	r.writeStatus()
	go r.installAndMonitor()
	return r
}

func (r *systemdRunner) Version() int32 { return r.status.DeploymentConfigVersion }

// Stop cancels the monitor goroutine. It does NOT stop the systemd unitName.
func (r *systemdRunner) Stop() {
	r.cancel()
	<-r.done
}

func (r *systemdRunner) installAndMonitor() {
	defer close(r.done)

	if err := atomicSymlink(r.status.RunningArtifact, r.unitBinPath); err != nil {
		slog.ErrorContext(r.ctx, "symlinking artifact failed", "err", err)
		r.updateStatus(apigen.RunningStatus_CRASHED, 0)
		return
	}
	slog.InfoContext(r.ctx, "systemd runner symlinked artifact", "binPath", r.unitBinPath, "artifact", r.status.RunningArtifact)

	out, err := systemctlRestart(r.ctx, r.unitName)
	if err != nil {
		slog.ErrorContext(r.ctx, "systemctl restart failed", "err", err, "unitName", r.unitName, "output", out)
		r.updateStatus(apigen.RunningStatus_CRASHED, 0)
		return
	}
	slog.InfoContext(r.ctx, "systemd runner restart issued", "unitName", r.unitName)

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
	poll := func() {
		active, err := systemctlIsActive(r.ctx, r.unitName)
		if err != nil {
			slog.WarnContext(r.ctx, "systemctl is-active failed", "unitName", r.unitName, "err", err)
			return
		}
		status := mapActiveState(active)
		if status == last {
			return
		}
		last = status
		pid, _ := systemctlMainPID(r.ctx, r.unitName)
		r.updateStatus(status, int32(pid))
	}
	poll()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}

func resolveSystemdRunnerArtifact(binPath string) string {
	if target, err := filepath.EvalSymlinks(binPath); err == nil && target != "" {
		return target
	}
	return binPath
}

func (r *systemdRunner) updateStatus(status apigen.RunningStatus, pid int32) {
	r.status.Status = status
	r.status.RunningPid = pid
	r.writeStatus()
}

func (r *systemdRunner) writeStatus() {
	r.store.MustWriteDeploymentStatus(r.deploymentID, func(s *apigen.DeploymentStatus) bool {
		if !s.Runner.IsZero() && s.Runner.DeploymentConfigVersion > r.status.DeploymentConfigVersion {
			slog.InfoContext(r.ctx, "discarding status update from superseded runner")
			return false
		}
		s.BumpUpdatedAt()
		s.DeploymentID = r.deploymentID
		s.Runner = r.status
		return true
	})
}

// --- helpers ---
func normalizeUnit(name string) string {
	if !strings.HasSuffix(name, ".service") {
		return name + ".service"
	}
	return name
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
	sudoArgs := append([]string{"-n", "/usr/bin/systemctl"}, args...)
	cmd := exec.CommandContext(ctx, "sudo", sudoArgs...)
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
	if err != nil {
		// systemctl is-active returns exit code 3 for "inactive"/"failed" but
		// still writes the state to stdout. Only return error when we got no
		// usable output.
		if state == "" {
			return "", err
		}
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
