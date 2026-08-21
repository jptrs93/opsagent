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
	"github.com/jptrs93/opsagent/backend/util/version"
)

// Runner drives a deployment artifact's process lifecycle. Runners own their
// own goroutines; Stop blocks until the runner has fully stopped and written
// its terminal state to the store. Stop is idempotent.
type Runner interface {
	Stop()
	Version() int32
	ArtifactMissing() <-chan struct{}
	// Serve claims the instance's stable inbound address for this placement:
	// its host route and its published host ports. It is idempotent and safe to
	// call before the container exists, so the operator can call it on every
	// update where the target state is RUN_SERVING.
	//
	// A placement that is not serving must not claim the address. On a
	// cross-node rollover the standby runs a full runner on the other node, and
	// activating there on startup would give two nodes a host route for one
	// address until the cluster map caught up — traffic originating on each
	// node reaching a different container.
	Serve() error
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
func Create(store storage.OperatorStore, inputs *runtimeinputs.RuntimeInputs, instanceID int32, dep *apigen.DeploymentConfig, status *apigen.ScheduledInstanceStatus) Runner {
	var preparer apigen.PreparerStatus
	if status != nil {
		preparer = status.Preparer
	}
	slog.InfoContext(deploymentLogContext(instanceID, dep), "runner.Create", "artifact", preparer.Artifact, "deploymentConfigVersion", preparer.DeploymentConfigVersion, "systemd", useSystemd(dep))
	switch {
	case useSystemd(dep):
		return newSystemdRunnerWithRestart(store, instanceID, dep, preparer)
	}
	return newContainerRunner(store, inputs, instanceID, dep, preparer)
}

func CreateRolloverCandidate(store storage.OperatorStore, inputs *runtimeinputs.RuntimeInputs, instanceID int32, dep *apigen.DeploymentConfig, status *apigen.ScheduledInstanceStatus) RolloverCandidate {
	var preparer apigen.PreparerStatus
	if status != nil {
		preparer = status.Preparer
	}
	slog.InfoContext(deploymentLogContext(instanceID, dep), "runner.CreateRolloverCandidate", "artifact", preparer.Artifact, "deploymentConfigVersion", preparer.DeploymentConfigVersion)
	return newRolloverContainerRunner(store, inputs, instanceID, dep, preparer)
}

// ReAttachRunning resumes supervision for a deployment whose desired state is
// running. Container runners adopt an existing task by id, or start a fresh task
// when there is nothing to adopt.
//
// For systemd/OpenDeploy self-deployments, an empty previous status means this
// scheduled instance has never installed/restarted for its config version. Return
// Stopped so the operator waits for prepare and then Create (install+restart).
// Only a non-empty previous status reattaches to the already-running unit.
func ReAttachRunning(store storage.OperatorStore, inputs *runtimeinputs.RuntimeInputs, instanceID int32, dep *apigen.DeploymentConfig, prev apigen.RunnerStatus) Runner {
	if useSystemd(dep) && dep.WorkloadVersion() != version.Version {
		slog.InfoContext(deploymentLogContext(instanceID, dep), "runner.ReAttachRunning: desired systemd build differs from current process", "desired", dep.WorkloadVersion(), "current", version.Version)
		return Stopped()
	}
	if prev.IsZero() {
		if useSystemd(dep) {
			slog.InfoContext(deploymentLogContext(instanceID, dep), "runner.ReAttachRunning: observing matching current systemd build")
			return observeExistingSystemdRunner(store, instanceID, dep)
		}
		slog.InfoContext(deploymentLogContext(instanceID, dep), "runner.ReAttachRunning: no previous runner, returning stopped")
		return Stopped()
	}
	slog.InfoContext(deploymentLogContext(instanceID, dep), "runner.ReAttachRunning: reattaching",
		"prevStatus", prev.Status, "prevPid", prev.RunningPid,
		"prevArtifact", prev.RunningArtifact, "prevSeqNo", prev.DeploymentConfigVersion)
	switch {
	case useSystemd(dep):
		return reAttachSystemdRunner(store, instanceID, dep, prev)
	}
	return reAttachContainerRunner(store, inputs, instanceID, dep, prev, containerStartupReattachRunning)
}

// ReAttachStopped reconciles runtime leftovers for a deployment whose desired
// state is stopped. Container runners may adopt an existing task only to stop
// and delete it; they never start a fresh task from this path.
func ReAttachStopped(store storage.OperatorStore, inputs *runtimeinputs.RuntimeInputs, instanceID int32, dep *apigen.DeploymentConfig, prev apigen.RunnerStatus) Runner {
	if prev.IsZero() {
		slog.InfoContext(deploymentLogContext(instanceID, dep), "runner.ReAttachStopped: no previous runner, returning stopped")
		return Stopped()
	}
	slog.InfoContext(deploymentLogContext(instanceID, dep), "runner.ReAttachStopped: reconciling stopped deployment",
		"prevStatus", prev.Status, "prevPid", prev.RunningPid,
		"prevArtifact", prev.RunningArtifact, "prevSeqNo", prev.DeploymentConfigVersion)
	if useSystemd(dep) {
		return Stopped()
	}
	return reAttachContainerRunner(store, inputs, instanceID, dep, prev, containerStartupReattachStopped)
}

// Stopped returns a no-op Runner sentinel used when no process is running.
func Stopped() Runner { return stoppedRunner{} }

type stoppedRunner struct{}

func (stoppedRunner) Stop()                            {}
func (stoppedRunner) Version() int32                   { return -1 }
func (stoppedRunner) ArtifactMissing() <-chan struct{} { return nil }
func (stoppedRunner) Serve() error                     { return nil }

func useSystemd(dep *apigen.DeploymentConfig) bool {
	return dep.Spec.SystemdSpec != nil
}

func deploymentLogContext(instanceID int32, dep *apigen.DeploymentConfig) context.Context {
	ctx := logu.AddKV(context.Background(), "scheduled_instance", instanceID)
	return logu.AddKV(ctx, "dep", dep.ID)
}
