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
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage"
)

// opendeployRunner is the OpenDeploy self-deployment runner. It installs a
// prepared binary, asks systemd to restart this service, and then stops
// writing status from the old process. The restarted process marks itself
// RUNNING on reattach.
type opendeployRunner struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	store               storage.OperatorStore
	scheduledInstanceID int32
	deploymentID        int32

	status apigen.RunnerStatus
}

var (
	systemctlRestartCommand = systemctlRestart
	opendeployBinPath       = internaldeploy.SelfBinPath
)

// attachOpendeployRunner publishes the current process as the running
// opendeploy deployment. Reaching this code proves the service is running;
// polling systemd only adds transient restart-state races.
func attachOpendeployRunner(store storage.OperatorStore, instanceID int32, dep *apigen.DeploymentConfig, prev apigen.RunnerStatus) *opendeployRunner {
	ctx, cancel := context.WithCancel(deploymentLogContext(instanceID, dep))
	if prev.IsZero() {
		prev.DeploymentConfigVersion = dep.Version
	}
	prev.RunningArtifact = resolveOpendeployArtifact()
	prev.RunningPid = int32(os.Getpid())
	prev.Status = apigen.RunningStatus_RUNNING
	r := &opendeployRunner{
		ctx:                 ctx,
		cancel:              cancel,
		done:                make(chan struct{}),
		store:               store,
		scheduledInstanceID: instanceID,
		deploymentID:        dep.ID,
		status:              prev,
	}
	close(r.done)
	r.writeStatus()
	return r
}

// newOpendeployRunnerWithRestart installs the prepared artifact, issues a
// systemd restart, and leaves the runner in STARTING until the restarted
// process reattaches and publishes RUNNING.
// Called only from runner.Create when the operator has a new artifact ready.
// No retries — if install or restart fails, it writes CRASHED and exits.
func newOpendeployRunnerWithRestart(store storage.OperatorStore, instanceID int32, dep *apigen.DeploymentConfig, preparerStatus apigen.PreparerStatus) *opendeployRunner {
	ctx, cancel := context.WithCancel(deploymentLogContext(instanceID, dep))
	r := &opendeployRunner{
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
	}
	r.writeStatus()
	go r.installAndRestart()
	return r
}

func (r *opendeployRunner) Version() int32                   { return r.status.DeploymentConfigVersion }
func (r *opendeployRunner) ArtifactMissing() <-chan struct{} { return nil }

// Serve is a no-op: the self deployment runs in the host network namespace and
// has no instance address to claim.
func (r *opendeployRunner) Serve() error { return nil }

// Stop cancels any in-flight install/restart. It does NOT stop the systemd unit.
func (r *opendeployRunner) Stop() {
	r.cancel()
	<-r.done
}

func (r *opendeployRunner) installAndRestart() {
	defer close(r.done)

	if err := atomicSymlink(r.status.RunningArtifact, opendeployBinPath); err != nil {
		slog.ErrorContext(r.ctx, "symlinking artifact failed", "err", err)
		r.updateStatus(apigen.RunningStatus_CRASHED, 0)
		return
	}
	slog.InfoContext(r.ctx, fmt.Sprintf("opendeploy runner symlinked artifact %s to %s", r.status.RunningArtifact, opendeployBinPath))

	out, err := systemctlRestartCommand(r.ctx, internaldeploy.SelfUnit)
	if err != nil {
		slog.ErrorContext(r.ctx, fmt.Sprintf("systemctl restart of %s failed output=%q", internaldeploy.SelfUnit, out), "err", err)
		r.updateStatus(apigen.RunningStatus_CRASHED, 0)
		return
	}
	slog.InfoContext(r.ctx, fmt.Sprintf("opendeploy runner restart of %s issued", internaldeploy.SelfUnit))
}

func resolveOpendeployArtifact() string {
	if target, err := filepath.EvalSymlinks(opendeployBinPath); err == nil && target != "" {
		return target
	}
	return opendeployBinPath
}

func (r *opendeployRunner) updateStatus(status apigen.RunningStatus, pid int32) {
	r.status.Status = status
	r.status.RunningPid = pid
	r.writeStatus()
}

func (r *opendeployRunner) writeStatus() {
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

func atomicSymlink(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating bin dir: %w", err)
	}
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
