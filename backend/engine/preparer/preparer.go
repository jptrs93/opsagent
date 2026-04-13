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
	Version() int32
}

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
	if prev != nil && prev.DeploymentConfigVersion == dep.Version && prev.Status == apigen.PreparationStatus_READY {
		slog.InfoContext(ctx, "preparer.ReAttach: already READY, returning finished",
			"configVersion", dep.Version, "artifact", prev.Artifact)
		return &finishedPreparer{deploymentConfigVersion: dep.Version}
	}
	if dep.DesiredState == nil || dep.DesiredState.Version == "" {
		slog.InfoContext(ctx, "preparer.ReAttach: no version to build, returning finished",
			"deploymentConfigVersion", dep.Version)
		return &finishedPreparer{deploymentConfigVersion: dep.Version}
	}
	if prev == nil {
		slog.InfoContext(ctx, "preparer.ReAttach: no previous preparer, starting fresh",
			"deploymentConfigVersion", dep.Version, "desiredVersion", desiredVersion(dep))
	} else {
		slog.InfoContext(ctx, "preparer.ReAttach: previous preparer not ready, restarting",
			"deploymentConfigVersion", dep.Version, "prevStatus", prev.Status, "prevconfigVersion", prev.DeploymentConfigVersion)
	}
	return startFor(ctx, store, dep)
}

func startFor(ctx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig) Preparer {
	switch {
	case hasNixBuild(dep):
		slog.InfoContext(ctx, "preparer.startFor: dispatching nixBuild", "deploymentConfigVersion", dep.Version)
		return Nix.start(ctx, store, dep)
	case hasGithubRelease(dep):
		slog.InfoContext(ctx, "preparer.startFor: dispatching githubRelease", "deploymentConfigVersion", dep.Version)
		return GHRel.start(ctx, store, dep)
	}
	slog.WarnContext(ctx, "preparer.startFor: no prepare config found, marking FAILED", "deploymentConfigVersion", dep.Version)
	writePrepareStatus(context.Background(), store, dep, "", apigen.PreparationStatus_FAILED)
	return &finishedPreparer{deploymentConfigVersion: dep.Version}
}

func hasNixBuild(dep *apigen.DeploymentConfig) bool {
	return dep.Spec != nil && dep.Spec.Prepare != nil && dep.Spec.Prepare.NixBuild != nil
}

func hasGithubRelease(dep *apigen.DeploymentConfig) bool {
	return dep.Spec != nil && dep.Spec.Prepare != nil && dep.Spec.Prepare.GithubRelease != nil
}

// activePreparer is the handle shared by the nix + github variants: ctx owns
// the worker goroutine, done is closed on exit, configVersion is dep.Version at the
// time of construction.
type activePreparer struct {
	cancel                  context.CancelFunc
	done                    chan struct{}
	deploymentConfigVersion int32
}

func (p *activePreparer) Cancel() {
	p.cancel()
	<-p.done
}

func (p *activePreparer) Version() int32 { return p.deploymentConfigVersion }

// finishedPreparer satisfies Preparer for an already-terminal preparation
// (READY at reattach, or a trivial FAILED dispatch).
type finishedPreparer struct{ deploymentConfigVersion int32 }

func (f *finishedPreparer) Cancel()        {}
func (f *finishedPreparer) Version() int32 { return f.deploymentConfigVersion }

// writePrepareStatus is the single entry point for preparer status writes.
// It bumps StatusSeqNo, guards against stale writes from superseded runs,
// and always uses a background context so terminal writes still land after
// the worker's own ctx has been cancelled.
func writePrepareStatus(_ context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, artifact string, status apigen.PreparationStatus) {
	slog.Info("preparer.writePrepareStatus", "deploymentConfigVersion", dep.Version, "status", status, "artifact", artifact)
	store.MustWriteDeploymentStatus(context.Background(), dep.ID, func(s *apigen.DeploymentStatus) {
		if s.Preparer != nil && s.Preparer.DeploymentConfigVersion > dep.Version {
			return
		}
		s.StatusSeqNo++
		s.Timestamp = time.Now()
		s.DeploymentID = dep.ID
		s.Preparer = &apigen.PreparerStatus{
			DeploymentConfigVersion: dep.Version,
			Artifact:                artifact,
			Status:                  status,
		}
	})
}

func desiredVersion(dep *apigen.DeploymentConfig) string {
	if dep.DesiredState == nil {
		return ""
	}
	return dep.DesiredState.Version
}
