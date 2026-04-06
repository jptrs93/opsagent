// Package runner spawns and monitors deployment artifacts. Each runner
// variant (os process, systemd) owns its own lifecycle: the operator creates
// a Runner when a deployment should start and calls Stop when it should stop
// or be replaced. Crash-restart behaviour (including exponential backoff) is
// the runner's responsibility — the operator is not involved.
package runner

import (
	"context"

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
	if useSystemd(dep) {
		return newSystemdRunner(ctx, store, dep, artifact, nil)
	}
	return newOSProcessRunner(ctx, store, dep, artifact, nil)
}

// ReAttach resumes supervision of a deployment that was already running
// before opsagent restarted. The artifact path and process identity come
// from the previous RunnerStatus. For os-process runners the adopted PID is
// polled and falls through to the normal spawn-and-respawn loop on exit.
// For systemd this is effectively identical to Create because the runner
// short-circuits to monitor-only mode when the installed binary already
// matches the prepared artifact.
func ReAttach(ctx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, prev *apigen.RunnerStatus) Runner {
	artifact := ""
	if prev != nil {
		artifact = prev.RunningArtifact
	}
	if useSystemd(dep) {
		return newSystemdRunner(ctx, store, dep, artifact, prev)
	}
	return newOSProcessRunner(ctx, store, dep, artifact, prev)
}

func useSystemd(dep *apigen.DeploymentConfig) bool {
	return dep.Spec != nil && dep.Spec.Runner != nil && dep.Spec.Runner.Systemd != nil
}
