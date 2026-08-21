// Package preparer defines preparation lifecycle contracts and shared runtime
// input handling. Concrete strategies live in subpackages and are selected by
// the deployment operator.
package prepare

import (
	"context"
	"log/slog"
	"sync"

	"github.com/jptrs93/goutil/logu"
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

// StatusUpdate is one preparer transition across both preparation stages. The
// rollup that gates runner start is derived from the pair by
// apigen.PreparerStatus.Rollup(), so there is nothing here to keep in step with
// it.
type StatusUpdate struct {
	// Artifact is the resolved runtime artifact, empty until the image stage
	// records one.
	Artifact string
	// Inputs is stage 1: assets, secrets, and configs.
	Inputs apigen.InputsStatus
	// Image is stage 2: the nix build, image pull, or release download.
	Image apigen.ImageStatus
}

// InProgress reports whether preparation is still running for this status, and
// is what holds a prepare-log stream open.
//
// It reads the rollup rather than the two stages on purpose: an input retry on
// an already-prepared instance leaves Inputs resolving while writing nothing to
// the prepare log, and the rollup gets that case right.
func InProgress(p apigen.PreparerStatus) bool {
	switch p.Rollup() {
	case apigen.PreparationStatus_PREPARING,
		apigen.PreparationStatus_DOWNLOADING,
		apigen.PreparationStatus_PULLING:
		return true
	default:
		return false
	}
}

// WriteStatus is the single entry point for preparer status writes.
// It bumps UpdatedAt and guards against stale writes from superseded runs.
//
// A write that would leave the preparer status exactly as it already is gets
// dropped. Preparation is re-driven for reasons that have nothing to do with the
// artifact changing — an agent restart, an artifact repair, a rollover candidate
// falling back — and it usually lands on the artifact already recorded.
// Publishing that identity would bump the clock, wake every subscriber, and push
// a no-op to the primary, for no observable change.
func WriteStatus(store storage.OperatorStore, instanceID int32, dep *apigen.DeploymentConfig, update StatusUpdate) {
	ctx := logu.AddKV(context.Background(), "scheduled_instance", instanceID)
	ctx = logu.AddKV(ctx, "dep", dep.ID)
	next := apigen.PreparerStatus{
		DeploymentConfigVersion: dep.Version,
		Artifact:                update.Artifact,
		Inputs:                  update.Inputs,
		Image:                   update.Image,
	}
	store.MustWriteScheduledInstanceStatus(instanceID, func(s *apigen.ScheduledInstanceStatus) bool {
		if !s.Preparer.IsZero() && s.Preparer.DeploymentConfigVersion > dep.Version {
			return false
		}
		// A zero status is never republished as unchanged: it means nothing has
		// been recorded for this instance yet, so the first write must land.
		if !s.Preparer.IsZero() && s.Preparer == next {
			slog.DebugContext(ctx, "preparer.writePrepareStatus: unchanged, not publishing", "deploymentConfigVersion", dep.Version, "status", next.Rollup(), "inputs", next.Inputs, "image", next.Image, "artifact", next.Artifact)
			return false
		}
		slog.InfoContext(ctx, "preparer.writePrepareStatus", "deploymentConfigVersion", dep.Version, "status", next.Rollup(), "inputs", next.Inputs, "image", next.Image, "artifact", next.Artifact)
		s.BumpUpdatedAt()
		s.ScheduledInstanceID = instanceID
		s.DeploymentID = dep.ID
		s.Preparer = next
		return true
	})
}
