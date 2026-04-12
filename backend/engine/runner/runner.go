// Package runner spawns and monitors deployment artifacts. The operator
// creates a Runner when a deployment should start and calls Stop when it
// should stop or be replaced. OS-process runners own crash-restart with
// exponential backoff. Systemd runners are monitor-only — systemd owns
// process restarts via its Restart= directives.
package runner

import (
	"context"
	"log/slog"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

// Runner drives a deployment artifact's process lifecycle. Runners own their
// own goroutines; Stop blocks until the runner has fully stopped and written
// its terminal state to the store. Stop is idempotent.
type Runner interface {
	Stop()
	SeqNo() int32
}

// Create picks the correct runner variant for the deployment and starts it.
// The artifact to execute is taken from status.Preparer.Artifact — the
// operator only calls Create once the preparer has reached READY for
// dep.SeqNo.
func Create(ctx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, status *apigen.DeploymentStatus) Runner {
	artifact := ""
	if status != nil && status.Preparer != nil {
		artifact = status.Preparer.Artifact
	}
	slog.InfoContext(ctx, "runner.Create", "artifact", artifact, "seqNo", dep.SeqNo, "systemd", useSystemd(dep))
	if useSystemd(dep) {
		return newSystemdRunnerWithRestart(ctx, store, dep, artifact)
	}
	return newOSProcessRunner(ctx, store, dep, artifact, nil)
}

// ReAttach resumes supervision of a deployment that was already running
// before opsagent restarted. For os-process runners the adopted PID is
// polled and falls through to the normal spawn-and-respawn loop on exit.
// For systemd runners this starts a monitor-only loop — no install or restart.
func ReAttach(ctx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, prev *apigen.RunnerStatus) Runner {
	if prev == nil {
		slog.InfoContext(ctx, "runner.ReAttach: no previous runner, returning stopped")
		return Stopped()
	}
	slog.InfoContext(ctx, "runner.ReAttach: reattaching",
		"prevStatus", prev.Status, "prevPid", prev.RunningPid,
		"prevArtifact", prev.RunningArtifact, "prevSeqNo", prev.DeploymentSeqNo)
	if useSystemd(dep) {
		return newSystemdMonitor(ctx, store, dep, prev)
	}
	return newOSProcessRunner(ctx, store, dep, prev.RunningArtifact, prev)
}

// Stopped returns a no-op Runner sentinel used when no process is running.
func Stopped() Runner { return stoppedRunner{} }

type stoppedRunner struct{}

func (stoppedRunner) Stop()        {}
func (stoppedRunner) SeqNo() int32 { return -1 }

func useSystemd(dep *apigen.DeploymentConfig) bool {
	return dep.Spec != nil && dep.Spec.Runner != nil && dep.Spec.Runner.Systemd != nil
}
