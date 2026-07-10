package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestEnsureSystemDeploymentRepairsExistingSpec(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	cid := &apigen.DeploymentIdentifier{SpaceID: OpendeploySpaceID, Machine: "primary", Name: systemDeploymentName}
	created := store.MustCreateDeployment(apigen.Context{}, cid, &apigen.DeploymentSpec{
		Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
	}, apigen.DesiredState{})
	store.MustSetDeploymentDesiredState(apigen.Context{}, created.ID, apigen.DesiredState{Version: "v0.0.194", Running: true})

	store.EnsureSystemDeployment("primary", "v0.0.195")

	var repaired *apigen.DeploymentConfig
	for _, cfg := range store.ListActiveDeploymentConfigs() {
		if cfg.ID == created.ID {
			repaired = cfg
			break
		}
	}
	if repaired == nil {
		t.Fatal("repaired deployment not found")
	}
	if repaired.Version <= created.Version {
		t.Fatalf("version = %d, want repaired version above %d", repaired.Version, created.Version)
	}
	if !isSystemDeploymentSpec(&repaired.Spec) {
		t.Fatalf("spec was not repaired: %+v", repaired.Spec)
	}
	if repaired.DesiredState.Version != "v0.0.194" || !repaired.DesiredState.Running {
		t.Fatalf("desired state = %+v, want preserved running v0.0.194", repaired.DesiredState)
	}
}

func TestEnsureDataplaneDeploymentCreatesInternalConfig(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	cfg := store.EnsureDataplaneDeployment("primary", "v0.0.200")
	if cfg == nil {
		t.Fatal("dataplane config not returned")
	}
	if cfg.ConfigID.SpaceID != OpendeploySpaceID || cfg.ConfigID.Machine != "primary" || cfg.ConfigID.Name != dataplaneDeploymentName {
		t.Fatalf("unexpected config id: %+v", cfg.ConfigID)
	}
	if !IsDataplaneDeploymentConfig(cfg) || !IsInternalDeploymentConfig(cfg) {
		t.Fatalf("dataplane config not recognized as internal: %+v", cfg.ConfigID)
	}
	if !cfg.DesiredState.Running || cfg.DesiredState.Version != "v0.0.200" {
		t.Fatalf("desired state = %+v, want running v0.0.200", cfg.DesiredState)
	}
	if cfg.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL || len(cfg.Spec.Networking.PortForwarding) != 0 {
		t.Fatalf("unexpected networking config: %+v", cfg.Spec.Networking)
	}
	again := store.EnsureDataplaneDeployment("primary", "v0.0.200")
	if again.Version != cfg.Version {
		t.Fatalf("ensure bumped unchanged dataplane version from %d to %d", cfg.Version, again.Version)
	}
}

func TestEnsureDataplaneDeploymentRequiresExplicitVersion(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	defer func() {
		if recover() == nil {
			t.Fatal("EnsureDataplaneDeployment did not panic without version")
		}
	}()
	store.EnsureDataplaneDeployment("primary", "")
}

func TestEnsureDataplaneDeploymentDoesNotReconcileExistingVersion(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	cfg := store.EnsureDataplaneDeployment("primary", "v0.0.200")

	again := store.EnsureDataplaneDeployment("primary", "v0.0.201")
	if again.DesiredState.Version != "v0.0.200" {
		t.Fatalf("desired version = %q, want preserved v0.0.200", again.DesiredState.Version)
	}
	if again.Version != cfg.Version {
		t.Fatalf("ensure bumped unchanged dataplane version from %d to %d", cfg.Version, again.Version)
	}
}
