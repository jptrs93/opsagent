package clusterhandler

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

func TestBuildAllowedRefs(t *testing.T) {
	secretID := int32(7)
	configID := int32(9)
	refs := buildAllowedRefs([]apigen.DeploymentWithStatus{{
		Config: apigen.DeploymentConfig{
			ID: 42,
			Spec: apigen.DeploymentSpec{
				Prepare: apigen.PrepareConfig{NixDockerBuild: &apigen.NixDockerBuildConfig{Repo: "github.com/acme/app", Flake: "flake.nix"}},
				Runner: apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{
					EnvVars: map[string]*apigen.EnvVarValue{
						"SECRET": &apigen.EnvVarValue{SecretID: &secretID},
						"CONFIG": &apigen.EnvVarValue{ConfigID: &configID},
						"ASSET":  &apigen.EnvVarValue{Asset: "app.env", AssetID: 3},
					},
					AssetMounts: []*apigen.ContainerAssetMount{&apigen.ContainerAssetMount{Asset: "nginx.conf", AssetID: 4, Path: "/etc/nginx/nginx.conf"}},
				}},
			},
		},
	}})

	if !refs.deploymentAllowed(42) {
		t.Fatal("deployment id should be allowed")
	}
	if !refs.allSecretsAllowed([]int32{7}) || refs.allSecretsAllowed([]int32{8}) {
		t.Fatal("secret refs not scoped correctly")
	}
	if !refs.allConfigsAllowed([]int32{9}) || refs.allConfigsAllowed([]int32{10}) {
		t.Fatal("config refs not scoped correctly")
	}
	if !refs.assetAllowed(3) || !refs.assetAllowed(4) || refs.assetAllowed(5) {
		t.Fatal("asset refs not scoped correctly")
	}
	if !refs.usesGithub {
		t.Fatal("GitHub credentials should be allowed for GitHub-backed deployments")
	}
}

func TestSessionRejectsCrossMachineStatusWrite(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	m1Node := store.EnsurePrimaryNode("m1", "m1")
	store.EnsurePrimaryNode("m2", "m2")
	spec := &apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "docker.io/library/nginx"}},
		Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
	}
	m1 := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "m1", Name: "web"}, spec, apigen.DesiredState{Version: "1", Running: true})
	m2 := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "m2", Name: "web"}, spec, apigen.DesiredState{Version: "1", Running: true})

	sess := newSession(context.Background(), func() {}, "m1", deploymentPredicateForNode(m1Node.ID), store)
	crossMachine := &apigen.DeploymentStatus{DeploymentID: m2.ID, Runner: apigen.RunnerStatus{Status: apigen.RunningStatus_RUNNING}}
	crossMachine.BumpUpdatedAt()
	sess.handleStatusWrite(crossMachine)
	if got := store.FetchDeploymentStatus(m2.ID); got.Runner.Status == apigen.RunningStatus_RUNNING {
		t.Fatal("cross-machine status write was accepted")
	}

	sameMachine := &apigen.DeploymentStatus{DeploymentID: m1.ID, Runner: apigen.RunnerStatus{Status: apigen.RunningStatus_RUNNING}}
	sameMachine.BumpUpdatedAt()
	sess.handleStatusWrite(sameMachine)
	if got := store.FetchDeploymentStatus(m1.ID); got.Runner.Status != apigen.RunningStatus_RUNNING {
		t.Fatalf("same-machine status write was rejected; status = %v", got.Runner.Status)
	}
}
