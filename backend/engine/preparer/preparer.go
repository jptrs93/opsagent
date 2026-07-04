// Package preparer produces a containerd image ref for a deployment. Each
// variant owns its own goroutine while the work runs and writes its status
// through storage.OperatorStore. The operator drives lifecycle by calling
// StartPrepare / ReAttach and later Cancel.
package preparer

import (
	"context"
	"log/slog"

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

// Package-level variants, wired by the process bootstrap before any operator
// starts. StartPrepare dispatches through them so the operator does not need
// to hold references to the variant instances.
var (
	GHRel        *GithubReleaseDownloader
	NixDocker    *NixDockerBuilder
	ContainerImg *ContainerImagePuller
)

// StartPrepare kicks off a fresh preparation for dep's current desired
// version. The returned Preparer owns its goroutine until Cancel or natural
// completion.
func StartPrepare(store storage.OperatorStore, dep *apigen.DeploymentConfig) Preparer {
	return startFor(store, dep)
}

// Idle returns a no-op preparer handle for a deployment that should not be
// preparing on process startup, usually because its desired state is stopped.
func Idle(version int32) Preparer {
	return &finishedPreparer{deploymentConfigVersion: version}
}

// ReAttach resumes observation of a preparation that was in flight before
// opendeploy last shut down. Preparations are not resumable: if the previous
// run reached READY for this SeqNo we return a no-op handle, otherwise we
// start a fresh preparation.
func ReAttach(store storage.OperatorStore, dep *apigen.DeploymentConfig, prev apigen.PreparerStatus) Preparer {
	if prev.DeploymentConfigVersion == dep.Version && prev.Status == apigen.PreparationStatus_READY {
		if err := EnsureRuntimeRefsReady(context.Background(), dep); err != nil {
			slog.Error("preparer.ReAttach: prepared runtime refs unavailable", "configVersion", dep.Version, "err", err)
			writePrepareStatus(store, dep, prev.Artifact, apigen.PreparationStatus_FAILED)
			return &finishedPreparer{deploymentConfigVersion: dep.Version}
		}
		slog.Info("preparer.ReAttach: already READY, returning finished",
			"configVersion", dep.Version, "artifact", prev.Artifact)
		return &finishedPreparer{deploymentConfigVersion: dep.Version}
	}
	if isSystemdDeployment(dep) && prev.IsZero() && dep.DesiredState.Version != "" {
		slog.Info("preparer.ReAttach: systemd deployment already installed, returning finished",
			"deploymentConfigVersion", dep.Version, "desiredVersion", desiredVersion(dep))
		return &finishedPreparer{deploymentConfigVersion: dep.Version}
	}
	if dep.DesiredState.Version == "" {
		slog.Info("preparer.ReAttach: no version to build, returning finished",
			"deploymentConfigVersion", dep.Version)
		return &finishedPreparer{deploymentConfigVersion: dep.Version}
	}
	if prev.IsZero() {
		slog.Info("preparer.ReAttach: no previous preparer, starting fresh",
			"deploymentConfigVersion", dep.Version, "desiredVersion", desiredVersion(dep))
	} else {
		slog.Info("preparer.ReAttach: previous preparer not ready, restarting",
			"deploymentConfigVersion", dep.Version, "prevStatus", prev.Status, "prevconfigVersion", prev.DeploymentConfigVersion)
	}
	return startFor(store, dep)
}

func startFor(store storage.OperatorStore, dep *apigen.DeploymentConfig) Preparer {
	switch {
	case hasGithubRelease(dep):
		slog.Info("preparer.startFor: dispatching internal githubRelease", "deploymentConfigVersion", dep.Version)
		return GHRel.start(store, dep)
	case hasNixDockerBuild(dep):
		slog.Info("preparer.startFor: dispatching nixDockerBuild", "deploymentConfigVersion", dep.Version)
		return NixDocker.start(store, dep)
	case hasContainerImage(dep):
		slog.Info("preparer.startFor: dispatching containerImage", "deploymentConfigVersion", dep.Version)
		return ContainerImg.start(store, dep)
	}
	slog.Warn("preparer.startFor: no prepare config found, marking FAILED", "deploymentConfigVersion", dep.Version)
	writePrepareStatus(store, dep, "", apigen.PreparationStatus_FAILED)
	return &finishedPreparer{deploymentConfigVersion: dep.Version}
}

func hasGithubRelease(dep *apigen.DeploymentConfig) bool {
	return dep.Spec.Prepare.GithubRelease != nil
}

func hasNixDockerBuild(dep *apigen.DeploymentConfig) bool {
	return dep.Spec.Prepare.NixDockerBuild != nil
}

func hasContainerImage(dep *apigen.DeploymentConfig) bool {
	return dep.Spec.Prepare.ContainerImage != nil
}

func isSystemdDeployment(dep *apigen.DeploymentConfig) bool {
	return !dep.Spec.Runner.Systemd.IsZero()
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
// It bumps UpdatedAt and guards against stale writes from superseded runs.
func writePrepareStatus(store storage.OperatorStore, dep *apigen.DeploymentConfig, artifact string, status apigen.PreparationStatus) {
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

func desiredVersion(dep *apigen.DeploymentConfig) string {
	return dep.DesiredState.Version
}
