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

type RolloverCandidate interface {
	Runner
	WaitReady() error
	Promote()
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

func CreateRolloverCandidate(store storage.OperatorStore, dep *apigen.DeploymentConfig, status *apigen.DeploymentStatus) RolloverCandidate {
	var preparer apigen.PreparerStatus
	if status != nil {
		preparer = status.Preparer
	}
	slog.Info("runner.CreateRolloverCandidate", "artifact", preparer.Artifact, "deploymentConfigVersion", preparer.DeploymentConfigVersion)
	return newRolloverContainerRunner(store, dep, preparer)
}

// ReAttachRunning resumes supervision for a deployment whose desired state is
// running. Container runners adopt an existing task by id, or start a fresh task
// when there is nothing to adopt. Systemd runners publish the current
// OpenDeploy process as RUNNING — no install or restart.
func ReAttachRunning(store storage.OperatorStore, dep *apigen.DeploymentConfig, prev apigen.RunnerStatus) Runner {
	if prev.IsZero() {
		if useSystemd(dep) {
			slog.Info("runner.ReAttachRunning: observing existing systemd unit without previous runner status")
			return observeExistingSystemdRunner(store, dep)
		}
		slog.Info("runner.ReAttachRunning: no previous runner, returning stopped")
		return Stopped()
	}
	slog.Info("runner.ReAttachRunning: reattaching",
		"prevStatus", prev.Status, "prevPid", prev.RunningPid,
		"prevArtifact", prev.RunningArtifact, "prevSeqNo", prev.DeploymentConfigVersion)
	switch {
	case useSystemd(dep):
		return reAttachSystemdRunner(store, dep, prev)
	}
	return reAttachContainerRunner(store, dep, prev, containerStartupReattachRunning)
}

// ReAttachStopped reconciles runtime leftovers for a deployment whose desired
// state is stopped. Container runners may adopt an existing task only to stop
// and delete it; they never start a fresh task from this path.
func ReAttachStopped(store storage.OperatorStore, dep *apigen.DeploymentConfig, prev apigen.RunnerStatus) Runner {
	if prev.IsZero() {
		slog.Info("runner.ReAttachStopped: no previous runner, returning stopped")
		return Stopped()
	}
	slog.Info("runner.ReAttachStopped: reconciling stopped deployment",
		"prevStatus", prev.Status, "prevPid", prev.RunningPid,
		"prevArtifact", prev.RunningArtifact, "prevSeqNo", prev.DeploymentConfigVersion)
	if useSystemd(dep) {
		return Stopped()
	}
	return reAttachContainerRunner(store, dep, prev, containerStartupReattachStopped)
}

// Stopped returns a no-op Runner sentinel used when no process is running.
func Stopped() Runner { return stoppedRunner{} }

type stoppedRunner struct{}

func (stoppedRunner) Stop()          {}
func (stoppedRunner) Version() int32 { return -1 }

func useSystemd(dep *apigen.DeploymentConfig) bool {
	return !dep.Spec.Runner.Systemd.IsZero()
}
