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

// systemdRunner is the internal OpenDeploy self-deployment runner. It installs
// a prepared binary, asks systemd to restart this service, and then stops
// writing status from the old process. The restarted process marks itself
// RUNNING on reattach.
type systemdRunner struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	store               storage.OperatorStore
	scheduledInstanceID int32
	deploymentID        int32

	status apigen.RunnerStatus

	unitName    string
	unitBinPath string
}

var systemctlRestartCommand = systemctlRestart

// reAttachSystemdRunner publishes the current process as the running systemd
// deployment. For OpenDeploy self-management, reaching this code proves the
// service is running; polling systemd only adds transient restart-state races.
func reAttachSystemdRunner(store storage.OperatorStore, instanceID int32, dep *apigen.DeploymentConfig, runnerStatus apigen.RunnerStatus) *systemdRunner {
	ctx, cancel := context.WithCancel(deploymentLogContext(instanceID, dep))
	sys := dep.Spec.SystemdSpec.Runtime
	runnerStatus.RunningArtifact = resolveSystemdRunnerArtifact(sys.BinPath)
	runnerStatus.RunningPid = int32(os.Getpid())
	runnerStatus.Status = apigen.RunningStatus_RUNNING
	r := &systemdRunner{
		ctx:                 ctx,
		cancel:              cancel,
		done:                make(chan struct{}),
		store:               store,
		scheduledInstanceID: instanceID,
		deploymentID:        dep.ID,
		status:              runnerStatus,
		unitName:            normalizeUnit(sys.Name),
	}
	close(r.done)
	r.writeStatus()
	return r
}

// observeExistingSystemdRunner is the first-install path for OpenDeploy's own
// systemd deployment. It does not restart or install anything; it publishes the
// already-running current process.
func observeExistingSystemdRunner(store storage.OperatorStore, instanceID int32, dep *apigen.DeploymentConfig) *systemdRunner {
	ctx, cancel := context.WithCancel(deploymentLogContext(instanceID, dep))
	sys := dep.Spec.SystemdSpec.Runtime
	r := &systemdRunner{
		ctx:                 ctx,
		cancel:              cancel,
		done:                make(chan struct{}),
		store:               store,
		scheduledInstanceID: instanceID,
		deploymentID:        dep.ID,
		status: apigen.RunnerStatus{
			DeploymentConfigVersion: dep.Version,
			RunningPid:              int32(os.Getpid()),
			RunningArtifact:         resolveSystemdRunnerArtifact(sys.BinPath),
			Status:                  apigen.RunningStatus_RUNNING,
		},
		unitName: normalizeUnit(sys.Name),
	}
	close(r.done)
	r.writeStatus()
	return r
}

// newSystemdRunnerWithRestart installs the prepared artifact, issues a
// systemd restart, and leaves the runner in STARTING until the restarted
// process reattaches and publishes RUNNING.
// Called only from runner.Create when the operator has a new artifact ready.
// No retries — if install or restart fails, it writes CRASHED and exits.
func newSystemdRunnerWithRestart(store storage.OperatorStore, instanceID int32, dep *apigen.DeploymentConfig, preparerStatus apigen.PreparerStatus) *systemdRunner {
	ctx, cancel := context.WithCancel(deploymentLogContext(instanceID, dep))
	sys := dep.Spec.SystemdSpec.Runtime
	r := &systemdRunner{
		ctx:                 ctx,
		cancel:              cancel,
		done:                make(chan struct{}),
		store:               store,
		scheduledInstanceID: instanceID,
		deploymentID:        dep.ID,
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
	go r.installAndRestart()
	return r
}

func (r *systemdRunner) Version() int32                   { return r.status.DeploymentConfigVersion }
func (r *systemdRunner) ArtifactMissing() <-chan struct{} { return nil }

// Serve is a no-op: systemd deployments run in the host network namespace and
// have no instance address to claim.
func (r *systemdRunner) Serve() error { return nil }

// Stop cancels any in-flight install/restart. It does NOT stop the systemd unit.
func (r *systemdRunner) Stop() {
	r.cancel()
	<-r.done
}

func (r *systemdRunner) installAndRestart() {
	defer close(r.done)

	if err := atomicSymlink(r.status.RunningArtifact, r.unitBinPath); err != nil {
		slog.ErrorContext(r.ctx, "symlinking artifact failed", "err", err)
		r.updateStatus(apigen.RunningStatus_CRASHED, 0)
		return
	}
	slog.InfoContext(r.ctx, "systemd runner symlinked artifact", "binPath", r.unitBinPath, "artifact", r.status.RunningArtifact)

	out, err := systemctlRestartCommand(r.ctx, r.unitName)
	if err != nil {
		slog.ErrorContext(r.ctx, "systemctl restart failed", "err", err, "unitName", r.unitName, "output", out)
		r.updateStatus(apigen.RunningStatus_CRASHED, 0)
		return
	}
	slog.InfoContext(r.ctx, "systemd runner restart issued", "unitName", r.unitName)
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
	r.store.MustWriteScheduledInstanceStatus(r.scheduledInstanceID, func(s *apigen.ScheduledInstanceStatus) bool {
		if !s.Runner.IsZero() && s.Runner.DeploymentConfigVersion > r.status.DeploymentConfigVersion {
			slog.InfoContext(r.ctx, "discarding status update from superseded runner")
			return false
		}
		s.BumpUpdatedAt()
		s.ScheduledInstanceID = r.scheduledInstanceID
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
