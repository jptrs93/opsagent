// Package preparer produces an executable artifact on disk for a deployment.
// Each variant (nix build, github release) owns its own goroutine while the
// work runs and writes its status through storage.OperatorStore. The operator
// drives lifecycle by calling StartPrepare / ReAttach and later Cancel.
package preparer

import (
	"context"
	"log/slog"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

// Preparer is an in-flight or already-completed preparation for a particular
// DeploymentConfig SeqNo. The operator cancels it when the deployment is
// superseded or removed.
type Preparer interface {
	Cancel()
	SeqNo() int32
}

// Preparer2 is a transitional alias kept so the in-progress operator.go
// refactor can continue to reference the interface under its old name.
type Preparer2 = Preparer

// PrepareWrapper is a transitional placeholder kept solely so that the
// in-progress operator.go still type-checks for the unused PrepareFunc type
// declaration. The old wrapper's real fields are gone; nothing reads them.
type PrepareWrapper struct{}

// Package-level variants, wired by the process bootstrap before any operator
// starts. StartPrepare dispatches through them so the operator does not need
// to hold references to the variant instances.
var (
	Nix   *NixBuilder
	GHRel *GithubReleaseDownloader
)

// StartPrepare kicks off a fresh preparation for dep's current desired
// version. The returned Preparer owns its goroutine until Cancel or natural
// completion.
func StartPrepare(store storage.OperatorStore, dep *apigen.DeploymentConfig) Preparer {
	return startFor(context.Background(), store, dep)
}

// ReAttach resumes observation of a preparation that was in flight before
// opsagent last shut down. Preparations are not resumable: if the previous
// run reached READY for this SeqNo we return a no-op handle, otherwise we
// start a fresh preparation.
func ReAttach(ctx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, prev *apigen.PreparerStatus) Preparer {
	if prev != nil && prev.DeploymentSeqNo == dep.SeqNo && prev.Status == apigen.PreparationStatus_READY {
		slog.InfoContext(ctx, "preparer.ReAttach: already READY, returning finished",
			"seqNo", dep.SeqNo, "artifact", prev.Artifact)
		return &finishedPreparer{seqNo: dep.SeqNo}
	}
	if prev == nil {
		slog.InfoContext(ctx, "preparer.ReAttach: no previous preparer, starting fresh",
			"seqNo", dep.SeqNo, "desiredVersion", desiredVersion(dep))
	} else {
		slog.InfoContext(ctx, "preparer.ReAttach: previous preparer not ready, restarting",
			"seqNo", dep.SeqNo, "prevStatus", prev.Status, "prevSeqNo", prev.DeploymentSeqNo)
	}
	return startFor(ctx, store, dep)
}

func startFor(ctx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig) Preparer {
	switch {
	case hasNixBuild(dep):
		slog.InfoContext(ctx, "preparer.startFor: dispatching nixBuild", "seqNo", dep.SeqNo)
		return Nix.start(ctx, store, dep)
	case hasGithubRelease(dep):
		slog.InfoContext(ctx, "preparer.startFor: dispatching githubRelease", "seqNo", dep.SeqNo)
		return GHRel.start(ctx, store, dep)
	}
	slog.WarnContext(ctx, "preparer.startFor: no prepare config found, marking FAILED", "seqNo", dep.SeqNo)
	writePrepareStatus(context.Background(), store, dep, "", apigen.PreparationStatus_FAILED)
	return &finishedPreparer{seqNo: dep.SeqNo}
}

func hasNixBuild(dep *apigen.DeploymentConfig) bool {
	return dep.Spec != nil && dep.Spec.Prepare != nil && dep.Spec.Prepare.NixBuild != nil
}

func hasGithubRelease(dep *apigen.DeploymentConfig) bool {
	return dep.Spec != nil && dep.Spec.Prepare != nil && dep.Spec.Prepare.GithubRelease != nil
}

// activePreparer is the handle shared by the nix + github variants: ctx owns
// the worker goroutine, done is closed on exit, seqNo is dep.SeqNo at the
// time of construction.
type activePreparer struct {
	cancel context.CancelFunc
	done   chan struct{}
	seqNo  int32
}

func (p *activePreparer) Cancel() {
	p.cancel()
	<-p.done
}

func (p *activePreparer) SeqNo() int32 { return p.seqNo }

// finishedPreparer satisfies Preparer for an already-terminal preparation
// (READY at reattach, or a trivial FAILED dispatch).
type finishedPreparer struct{ seqNo int32 }

func (f *finishedPreparer) Cancel()      {}
func (f *finishedPreparer) SeqNo() int32 { return f.seqNo }

// writePrepareStatus is the single entry point for preparer status writes.
// It bumps StatusSeqNo, guards against stale writes from superseded runs,
// and always uses a background context so terminal writes still land after
// the worker's own ctx has been cancelled.
func writePrepareStatus(_ context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, artifact string, status apigen.PreparationStatus) {
	slog.Info("preparer.writePrepareStatus", "seqNo", dep.SeqNo, "status", status, "artifact", artifact)
	store.MustWriteDeploymentStatus(context.Background(), *dep.ID, func(s *apigen.DeploymentStatus) {
		if s.Preparer != nil && s.Preparer.DeploymentSeqNo > dep.SeqNo {
			return
		}
		s.StatusSeqNo++
		s.Timestamp = time.Now()
		s.DeploymentID = dep.ID
		s.Preparer = &apigen.PreparerStatus{
			DeploymentSeqNo: dep.SeqNo,
			Artifact:        artifact,
			Status:          status,
		}
	})
}

func desiredVersion(dep *apigen.DeploymentConfig) string {
	if dep.DesiredState == nil {
		return ""
	}
	return dep.DesiredState.Version
}
