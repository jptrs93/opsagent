// Package preparer defines preparation lifecycle contracts and shared runtime
// input handling. Concrete strategies live in subpackages and are selected by
// the deployment operator.
package prepare

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

// Handle owns cancellation and completion for one preparation run.
type Handle struct {
	cancel                context.CancelFunc
	done                  chan struct{}
	complete              sync.Once
	deploymentSpecVersion uint32
}

func NewHandle(version uint32) (*Handle, context.Context) {
	ctx, cancel := context.WithCancel(context.Background())
	return &Handle{
		cancel:                cancel,
		done:                  make(chan struct{}),
		deploymentSpecVersion: version,
	}, ctx
}

// Finished returns a no-op preparer handle for a deployment that should not be
// preparing on process startup, usually because its desired state is stopped.
func Finished(version uint32) *Handle {
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

func (h *Handle) SpecVersion() uint32 { return h.deploymentSpecVersion }

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
	case apigen.PreparationStatus_PREPARATION_STATUS_PREPARING,
		apigen.PreparationStatus_PREPARATION_STATUS_DOWNLOADING,
		apigen.PreparationStatus_PREPARATION_STATUS_PULLING:
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
func WriteStatus(store storage.OperatorStore, instanceID uint64, dep *apigen.DeploymentRecord, update StatusUpdate) {
	ctx := logu.AddTag(context.Background(), "Preparer")
	ctx = logu.AddKV(ctx, "scheduled_instance", instanceID)
	ctx = logu.AddKV(ctx, "dep", dep.Deployment.ID)
	next := apigen.PreparerStatus{
		DeploymentSpecVersion: dep.Meta.SpecVersion,
		Artifact:              update.Artifact,
		Inputs:                update.Inputs,
	}
	if update.Image != apigen.ImageStatus_IMAGE_STATUS_UNSPECIFIED {
		next.SetImage(update.Image)
	}
	fmtNext := func() string {
		return fmt.Sprintf("seqNo=%d status=%v inputs=%v image=%v artifact=%q", dep.Meta.SpecVersion, next.Rollup(), next.Inputs, next.ImageStage(), next.Artifact)
	}
	store.MustWriteScheduledInstanceStatus(instanceID, func(s *apigen.ScheduledInstanceStatus) bool {
		if s.Preparer.Present && s.Preparer.Value.DeploymentSpecVersion > dep.Meta.SpecVersion {
			return false
		}
		// A zero status is never republished as unchanged: it means nothing has
		// been recorded for this instance yet, so the first write must land.
		if s.Preparer.Present && samePreparerStatus(s.Preparer.Value, next) {
			slog.DebugContext(ctx, "preparer.writePrepareStatus: unchanged, not publishing "+fmtNext())
			return false
		}
		slog.InfoContext(ctx, "preparer.writePrepareStatus "+fmtNext())
		s.BumpUpdatedAt()
		s.ScheduledInstanceID = instanceID
		s.Preparer = apigen.Some(next)
		return true
	})
}

func samePreparerStatus(a, b apigen.PreparerStatus) bool {
	return a.DeploymentSpecVersion == b.DeploymentSpecVersion &&
		a.Artifact == b.Artifact &&
		a.Inputs == b.Inputs &&
		a.Image == b.Image
}
