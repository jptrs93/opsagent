// Package preparer defines preparation lifecycle contracts and shared runtime
// input handling. Concrete strategies live in subpackages and are selected by
// the deployment operator.
package prepare

import (
	"context"
	"log/slog"
	"sync"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

// Handle owns cancellation and completion for one preparation run.
type Handle struct {
	cancel                  context.CancelFunc
	done                    chan struct{}
	complete                sync.Once
	deploymentConfigVersion int32
}

func NewHandle(version int32) (*Handle, context.Context) {
	ctx, cancel := context.WithCancel(context.Background())
	return &Handle{
		cancel:                  cancel,
		done:                    make(chan struct{}),
		deploymentConfigVersion: version,
	}, ctx
}

// Finished returns a no-op preparer handle for a deployment that should not be
// preparing on process startup, usually because its desired state is stopped.
func Finished(version int32) *Handle {
	handle, _ := NewHandle(version)
	handle.cancel()
	handle.Complete()
	return handle
}

// Complete marks the preparation goroutine as stopped. It is safe to call more
// than once so every exit path can defer it.
func (h *Handle) Complete() {
	h.complete.Do(func() { close(h.done) })
}

// Cancel requests cancellation and waits for the preparation goroutine to stop.
func (h *Handle) Cancel() {
	h.cancel()
	<-h.done
}

func (h *Handle) Version() int32 { return h.deploymentConfigVersion }

// WriteStatus is the single entry point for preparer status writes.
// It bumps UpdatedAt and guards against stale writes from superseded runs.
func WriteStatus(store storage.OperatorStore, dep *apigen.DeploymentConfig, artifact string, status apigen.PreparationStatus) {
	slog.Info("preparer.writePrepareStatus", "deploymentConfigVersion", dep.Version, "status", status, "artifact", artifact)
	store.MustWriteDeploymentStatus(dep.ID, func(s *apigen.DeploymentStatus) bool {
		if !s.Preparer.IsZero() && s.Preparer.DeploymentConfigVersion > dep.Version {
			return false
		}
		s.BumpUpdatedAt()
		s.DeploymentID = dep.ID
		s.Preparer = apigen.PreparerStatus{
			DeploymentConfigVersion: dep.Version,
			Artifact:                artifact,
			Status:                  status,
		}
		return true
	})
}
