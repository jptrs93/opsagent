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

func TestEnsureNodesForSystemDeploymentsBackfillsPrimaryAndWorkers(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	store.EnsureSystemDeployment("primary", "v0.0.200")
	store.EnsureSystemDeployment("worker-1", "v0.0.200")

	store.EnsureNodesForSystemDeployments("primary")

	nodes := store.ListNodes()
	if len(nodes) != 2 {
		t.Fatalf("node count = %d, want 2: %+v", len(nodes), nodes)
	}
	rolesByName := map[string][]int32{}
	for _, node := range nodes {
		if node.Name != node.SNI {
			t.Fatalf("node %q sni = %q, want same as name", node.Name, node.SNI)
		}
		if node.EnrollmentID != nil {
			t.Fatalf("backfilled node %q enrollment id = %v, want nil", node.Name, *node.EnrollmentID)
		}
		rolesByName[node.Name] = node.Roles
	}
	if len(rolesByName["primary"]) != 1 || rolesByName["primary"][0] != NodeRolePrimary {
		t.Fatalf("primary roles = %+v, want primary", rolesByName["primary"])
	}
	if len(rolesByName["worker-1"]) != 1 || rolesByName["worker-1"][0] != NodeRoleSecondary {
		t.Fatalf("worker roles = %+v, want secondary", rolesByName["worker-1"])
	}
}

func TestAcceptEnrollmentRequestCreatesNode(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	req := store.MustUpsertEnrollmentRequest("127.0.0.1", "requesting-id", "v0.0.200")

	status, err := store.AcceptEnrollmentRequest(req.ID, "worker-1")
	if err != nil {
		t.Fatalf("AcceptEnrollmentRequest: %v", err)
	}
	if status.Status != EnrollmentStatusAccepted {
		t.Fatalf("status = %q, want accepted", status.Status)
	}
	nodes := store.ListNodes()
	if len(nodes) != 1 {
		t.Fatalf("node count = %d, want 1: %+v", len(nodes), nodes)
	}
	node := nodes[0]
	if node.Name != "worker-1" || node.SNI != "worker-1" {
		t.Fatalf("node identity = name %q sni %q, want worker-1", node.Name, node.SNI)
	}
	if node.EnrollmentID == nil || *node.EnrollmentID != req.ID {
		t.Fatalf("node enrollment id = %v, want %d", node.EnrollmentID, req.ID)
	}
	if len(node.Roles) != 1 || node.Roles[0] != NodeRoleSecondary {
		t.Fatalf("node roles = %+v, want secondary", node.Roles)
	}
}
