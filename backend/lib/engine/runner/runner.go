// Package runner spawns and monitors deployment artifacts. The operator
// creates a Runner when a deployment should start and calls Stop when it
// should stop or be replaced. Container runners own crash-restart with
// exponential backoff. Systemd is retained for the internal OPENDEPLOY
// deployment only.
package runner

import (
	"context"
	"log/slog"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/storage"
)

// Runner drives a deployment artifact's process lifecycle. Runners own their
// own goroutines; Stop blocks until the runner has fully stopped and written
// its terminal state to the store. Stop is idempotent.
type Runner interface {
	Stop()
	Version() int32
	ArtifactMissing() <-chan struct{}
}

type RolloverCandidate interface {
	Runner
	WaitReady() error
	Promote() error
}

// Create picks the correct runner variant for the deployment and starts it.
// The artifact to execute is taken from status.Preparer.Artifact — the
// operator only calls Create once the preparer has reached READY for
// dep.Version.
func Create(store storage.OperatorStore, inputs *runtimeinputs.RuntimeInputs, dep *apigen.DeploymentConfig2, status *apigen.DeploymentStatus) Runner {
	var preparer apigen.PreparerStatus
	if status != nil {
		preparer = status.Preparer
	}
	slog.InfoContext(deploymentLogContext(dep), "runner.Create", "artifact", preparer.Artifact, "deploymentConfigVersion", preparer.DeploymentConfigVersion, "systemd", useSystemd(dep))
	switch {
	case useSystemd(dep):
		return newSystemdRunnerWithRestart(store, dep, preparer)
	}
	return newContainerRunner(store, inputs, dep, preparer)
}

func CreateRolloverCandidate(store storage.OperatorStore, inputs *runtimeinputs.RuntimeInputs, dep *apigen.DeploymentConfig2, status *apigen.DeploymentStatus) RolloverCandidate {
	var preparer apigen.PreparerStatus
	if status != nil {
		preparer = status.Preparer
	}
	slog.InfoContext(deploymentLogContext(dep), "runner.CreateRolloverCandidate", "artifact", preparer.Artifact, "deploymentConfigVersion", preparer.DeploymentConfigVersion)
	return newRolloverContainerRunner(store, inputs, dep, preparer)
}

// ReAttachRunning resumes supervision for a deployment whose desired state is
// running. Container runners adopt an existing task by id, or start a fresh task
// when there is nothing to adopt. Systemd runners publish the current
// OpenDeploy process as RUNNING — no install or restart.
func ReAttachRunning(store storage.OperatorStore, inputs *runtimeinputs.RuntimeInputs, dep *apigen.DeploymentConfig2, prev apigen.RunnerStatus) Runner {
	if prev.IsZero() {
		if useSystemd(dep) {
			slog.InfoContext(deploymentLogContext(dep), "runner.ReAttachRunning: observing existing systemd unit without previous runner status")
			return observeExistingSystemdRunner(store, dep)
		}
		slog.InfoContext(deploymentLogContext(dep), "runner.ReAttachRunning: no previous runner, returning stopped")
		return Stopped()
	}
	slog.InfoContext(deploymentLogContext(dep), "runner.ReAttachRunning: reattaching",
		"prevStatus", prev.Status, "prevPid", prev.RunningPid,
		"prevArtifact", prev.RunningArtifact, "prevSeqNo", prev.DeploymentConfigVersion)
	switch {
	case useSystemd(dep):
		return reAttachSystemdRunner(store, dep, prev)
	}
	return reAttachContainerRunner(store, inputs, dep, prev, containerStartupReattachRunning)
}

// ReAttachStopped reconciles runtime leftovers for a deployment whose desired
// state is stopped. Container runners may adopt an existing task only to stop
// and delete it; they never start a fresh task from this path.
func ReAttachStopped(store storage.OperatorStore, inputs *runtimeinputs.RuntimeInputs, dep *apigen.DeploymentConfig2, prev apigen.RunnerStatus) Runner {
	if prev.IsZero() {
		slog.InfoContext(deploymentLogContext(dep), "runner.ReAttachStopped: no previous runner, returning stopped")
		return Stopped()
	}
	slog.InfoContext(deploymentLogContext(dep), "runner.ReAttachStopped: reconciling stopped deployment",
		"prevStatus", prev.Status, "prevPid", prev.RunningPid,
		"prevArtifact", prev.RunningArtifact, "prevSeqNo", prev.DeploymentConfigVersion)
	if useSystemd(dep) {
		return Stopped()
	}
	return reAttachContainerRunner(store, inputs, dep, prev, containerStartupReattachStopped)
}

// Stopped returns a no-op Runner sentinel used when no process is running.
func Stopped() Runner { return stoppedRunner{} }

type stoppedRunner struct{}

func (stoppedRunner) Stop()                            {}
func (stoppedRunner) Version() int32                   { return -1 }
func (stoppedRunner) ArtifactMissing() <-chan struct{} { return nil }

func useSystemd(dep *apigen.DeploymentConfig2) bool {
	return dep.Spec.SystemdSpec != nil
}

func deploymentLogContext(dep *apigen.DeploymentConfig2) context.Context {
	return logu.ExtendLogContext(context.Background(), "dep", dep.ID)
}
