// Package runner spawns and monitors deployment artifacts. The operator
// creates a Runner when a deployment should start and calls Stop when it
// should stop or be replaced. Container runners own crash-restart with
// exponential backoff. Systemd is retained for the internal OPENDEPLOY
// deployment only.
package runner

import (
	"log/slog"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

// Runner drives a deployment artifact's process lifecycle. Runners own their
// own goroutines; Stop blocks until the runner has fully stopped and written
// its terminal state to the store. Stop is idempotent.
type Runner interface {
	Stop()
	Version() int32
}

// Create picks the correct runner variant for the deployment and starts it.
// The artifact to execute is taken from status.Preparer.Artifact — the
// operator only calls Create once the preparer has reached READY for
// dep.Version.
func Create(store storage.OperatorStore, dep *apigen.DeploymentConfig, status *apigen.DeploymentStatus) Runner {
	var preparer apigen.PreparerStatus
	if status != nil {
		preparer = status.Preparer
	}
	slog.Info("runner.Create", "artifact", preparer.Artifact, "deploymentConfigVersion", preparer.DeploymentConfigVersion, "systemd", useSystemd(dep))
	switch {
	case useSystemd(dep):
		return newSystemdRunnerWithRestart(store, dep, preparer)
	}
	return newContainerRunner(store, dep, preparer)
}

// ReAttach resumes supervision of a deployment that was already running
// before opendeploy restarted. Container runners reattach to a running task by
// id. Systemd runners start a monitor-only loop — no install or restart.
func ReAttach(store storage.OperatorStore, dep *apigen.DeploymentConfig, prev apigen.RunnerStatus) Runner {
	if prev.IsZero() {
		if useSystemd(dep) && dep.DesiredState.Running {
			slog.Info("runner.ReAttach: observing existing systemd unit without previous runner status")
			return observeExistingSystemdRunner(store, dep)
		}
		slog.Info("runner.ReAttach: no previous runner, returning stopped")
		return Stopped()
	}
	slog.Info("runner.ReAttach: reattaching",
		"prevStatus", prev.Status, "prevPid", prev.RunningPid,
		"prevArtifact", prev.RunningArtifact, "prevSeqNo", prev.DeploymentConfigVersion)
	switch {
	case useSystemd(dep):
		return reAttachSystemdRunner(store, dep, prev)
	}
	return reAttachContainerRunner(store, dep, prev)
}

// Stopped returns a no-op Runner sentinel used when no process is running.
func Stopped() Runner { return stoppedRunner{} }

type stoppedRunner struct{}

func (stoppedRunner) Stop()          {}
func (stoppedRunner) Version() int32 { return -1 }

func useSystemd(dep *apigen.DeploymentConfig) bool {
	return !dep.Spec.Runner.Systemd.IsZero()
}
