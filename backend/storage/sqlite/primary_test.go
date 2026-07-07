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
	if !isDataplaneDeploymentSpec(&cfg.Spec) {
		t.Fatalf("unexpected dataplane spec: %+v", cfg.Spec)
	}
	if cfg.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL || len(cfg.Spec.Networking.PortForwarding) != 0 {
		t.Fatalf("unexpected networking config: %+v", cfg.Spec.Networking)
	}
	again := store.EnsureDataplaneDeployment("primary", "v0.0.200")
	if again.Version != cfg.Version {
		t.Fatalf("ensure bumped unchanged dataplane version from %d to %d", cfg.Version, again.Version)
	}
}

func TestEnsureDataplaneDeploymentRepairsExistingSpec(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	cid := &apigen.DeploymentIdentifier{SpaceID: OpendeploySpaceID, Machine: "primary", Name: dataplaneDeploymentName}
	created := store.MustCreateDeployment(apigen.Context{}, cid, &apigen.DeploymentSpec{
		Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
	}, apigen.DesiredState{Version: "v0.0.199", Running: true})

	repaired := store.EnsureDataplaneDeployment("primary", "v0.0.200")
	if repaired.ID != created.ID {
		t.Fatalf("dataplane id = %d, want existing id %d", repaired.ID, created.ID)
	}
	if repaired.Version <= created.Version {
		t.Fatalf("version = %d, want repaired version above %d", repaired.Version, created.Version)
	}
	if !isDataplaneDeploymentSpec(&repaired.Spec) {
		t.Fatalf("spec was not repaired: %+v", repaired.Spec)
	}
	if repaired.DesiredState.Version != "v0.0.199" || !repaired.DesiredState.Running {
		t.Fatalf("desired state = %+v, want preserved running v0.0.199", repaired.DesiredState)
	}
}

func TestPrimaryStorageMigratesMissingNetworkingToHost(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := NewPrimaryStorage(dbPath)
	created := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "primary", Name: "legacy"}, &apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
	}, apigen.DesiredState{})
	if err := store.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	reopened := NewPrimaryStorage(dbPath)
	defer reopened.db.Close()
	cfg := reopened.configCache[created.ID]
	if cfg == nil {
		t.Fatal("migrated config not found")
	}
	if cfg.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_HOST {
		t.Fatalf("networking mode = %v, want host", cfg.Spec.Networking.Mode)
	}
	history := reopened.MustFetchDeploymentHistory(created.ID)
	if len(history) != 1 || history[0].Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_HOST {
		t.Fatalf("history networking not migrated: %+v", history)
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

func TestEnsureDataplaneDeploymentsForSystemDeploymentsCreatesPairs(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	store.EnsureSystemDeployment("primary", "v0.0.200")
	store.EnsureSystemDeployment("worker-1", "v0.0.200")

	cfgs := store.EnsureDataplaneDeploymentsForSystemDeployments("v0.0.200")
	if len(cfgs) != 2 {
		t.Fatalf("paired dataplane count = %d, want 2", len(cfgs))
	}
	seen := map[string]bool{}
	for _, cfg := range cfgs {
		seen[cfg.ConfigID.Machine] = true
		if cfg.DesiredState.Version != "v0.0.200" || !cfg.DesiredState.Running {
			t.Fatalf("desired state for %s = %+v, want running v0.0.200", cfg.ConfigID.Machine, cfg.DesiredState)
		}
	}
	if !seen["primary"] || !seen["worker-1"] {
		t.Fatalf("paired machines = %+v, want primary and worker-1", seen)
	}
}
