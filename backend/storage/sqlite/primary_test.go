package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestInvalidateMachineRuntimeStatePreservesConfigAndHistory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := NewPrimaryStorage(dbPath)
	defer func() { _ = store.Close() }()

	create := func(machine, name string, spec *apigen.DeploymentSpec) *apigen.DeploymentConfig {
		return store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{
			SpaceID: DefaultSpaceID,
			Machine: machine,
			Name:    name,
		}, spec, apigen.DesiredState{Version: "v1", Running: true})
	}
	containerSpec := &apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "example/app"}},
		Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
	}
	primary := create("primary", "app", containerSpec)
	worker := create("worker", "app", containerSpec)
	system := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{
		SpaceID: OpendeploySpaceID,
		Machine: "primary",
		Name:    systemDeploymentName,
	}, SystemDeploymentSpec(), apigen.DesiredState{Version: "v1", Running: true})

	seedStatus := func(cfg *apigen.DeploymentConfig, artifact string) {
		store.MustWriteDeploymentStatus(cfg.ID, func(status *apigen.DeploymentStatus) bool {
			status.BumpUpdatedAt()
			status.DeploymentID = cfg.ID
			status.Preparer = apigen.PreparerStatus{DeploymentConfigVersion: cfg.Version, Artifact: artifact, Status: apigen.PreparationStatus_READY}
			status.Runner = apigen.RunnerStatus{DeploymentConfigVersion: cfg.Version, RunningArtifact: artifact, Status: apigen.RunningStatus_RUNNING}
			return true
		})
	}
	seedStatus(primary, "example/app:v1")
	seedStatus(worker, "example/app:v1")
	seedStatus(system, "/var/lib/opendeploy/releases/v1/opendeploy")

	primaryStatusTime := store.FetchDeploymentStatus(primary.ID).UpdatedAt
	primaryHistoryCount := len(store.MustFetchDeploymentStatusHistory(primary.ID))
	primaryConfigHistoryCount := len(store.MustFetchDeploymentHistory(primary.ID))
	primaryConfigVersion := primary.Version

	count, err := store.InvalidateMachineRuntimeState("primary")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("invalidated count = %d, want 1", count)
	}
	got := store.FetchDeploymentStatus(primary.ID)
	if !got.Preparer.IsZero() || !got.Runner.IsZero() {
		t.Fatalf("primary runtime status was not cleared: %+v", got)
	}
	if !got.UpdatedAt.Equal(primaryStatusTime) {
		t.Fatalf("updated_at = %v, want preserved %v", got.UpdatedAt, primaryStatusTime)
	}
	if len(store.MustFetchDeploymentStatusHistory(primary.ID)) != primaryHistoryCount {
		t.Fatal("runtime invalidation changed status history")
	}
	if len(store.MustFetchDeploymentHistory(primary.ID)) != primaryConfigHistoryCount {
		t.Fatal("runtime invalidation changed config history")
	}
	if store.FetchDeploymentStatus(worker.ID).Runner.Status != apigen.RunningStatus_RUNNING {
		t.Fatal("worker runtime status was cleared")
	}
	if store.FetchDeploymentStatus(system.ID).Runner.Status != apigen.RunningStatus_RUNNING {
		t.Fatal("primary system deployment runtime status was cleared")
	}
	for _, cfg := range store.ListActiveDeploymentConfigs() {
		if cfg.ID == primary.ID && cfg.Version != primaryConfigVersion {
			t.Fatalf("primary config version = %d, want %d", cfg.Version, primaryConfigVersion)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = NewPrimaryStorage(dbPath)
	got = store.FetchDeploymentStatus(primary.ID)
	if !got.Preparer.IsZero() || !got.Runner.IsZero() || !got.UpdatedAt.Equal(primaryStatusTime) {
		t.Fatalf("persisted primary runtime status was not cleared correctly: %+v", got)
	}
}

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

func TestEnsureNetproxyDeploymentCreatesInternalConfig(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	cfg := store.EnsureNetproxyDeployment("primary", "v0.0.200")
	if cfg == nil {
		t.Fatal("netproxy config not returned")
	}
	if cfg.ConfigID.SpaceID != OpendeploySpaceID || cfg.ConfigID.Machine != "primary" || cfg.ConfigID.Name != netproxyDeploymentName {
		t.Fatalf("unexpected config id: %+v", cfg.ConfigID)
	}
	if !IsNetproxyDeploymentConfig(cfg) || !IsInternalDeploymentConfig(cfg) {
		t.Fatalf("netproxy config not recognized as internal: %+v", cfg.ConfigID)
	}
	if !cfg.DesiredState.Running || cfg.DesiredState.Version != "v0.0.200" {
		t.Fatalf("desired state = %+v, want running v0.0.200", cfg.DesiredState)
	}
	if cfg.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL || len(cfg.Spec.Networking.PortForwarding) != 0 {
		t.Fatalf("unexpected networking config: %+v", cfg.Spec.Networking)
	}
	again := store.EnsureNetproxyDeployment("primary", "v0.0.200")
	if again.Version != cfg.Version {
		t.Fatalf("ensure bumped unchanged netproxy version from %d to %d", cfg.Version, again.Version)
	}
}

func TestEnsureNetproxyDeploymentRequiresExplicitVersion(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	defer func() {
		if recover() == nil {
			t.Fatal("EnsureNetproxyDeployment did not panic without version")
		}
	}()
	store.EnsureNetproxyDeployment("primary", "")
}

func TestEnsureNetproxyDeploymentReconcilesExistingVersion(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	cfg := store.EnsureNetproxyDeployment("primary", "v0.0.200")

	again := store.EnsureNetproxyDeployment("primary", "v0.0.201")
	if again.DesiredState.Version != "v0.0.201" {
		t.Fatalf("desired version = %q, want v0.0.201", again.DesiredState.Version)
	}
	if again.Version <= cfg.Version {
		t.Fatalf("version = %d, want above %d", again.Version, cfg.Version)
	}
}

func TestEnsurePrimaryNodeCreatesPrimaryRole(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))

	store.EnsurePrimaryNode("primary", "primary-id")

	nodes := store.ListNodes()
	if len(nodes) != 1 {
		t.Fatalf("node count = %d, want 1: %+v", len(nodes), nodes)
	}
	node := nodes[0]
	if node.Name != "primary" || node.Identifier != "primary-id" {
		t.Fatalf("node identity = name %q identifier %q, want primary/primary-id", node.Name, node.Identifier)
	}
	if node.EnrollmentID != nil {
		t.Fatalf("primary enrollment id = %v, want nil", *node.EnrollmentID)
	}
	if len(node.Roles) != 1 || node.Roles[0] != NodeRolePrimary {
		t.Fatalf("primary roles = %+v, want primary", node.Roles)
	}
}

func TestEnsurePrimaryNodeUsesCertificateIdentifier(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	if _, err := store.db.Exec(`
		INSERT INTO nodes (enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key)
		VALUES (NULL, 0, 'coflip-prod', 'coflip-prod', '[1]', '[]', '')`); err != nil {
		t.Fatalf("seed secondary node: %v", err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO nodes (enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key)
		VALUES (NULL, 0, 'primary', 'primary', '[0]', '[]', '')`); err != nil {
		t.Fatalf("seed primary node: %v", err)
	}

	node := store.EnsurePrimaryNode("primary", "primary")
	if node.Name != "primary" || node.Identifier != "primary" {
		t.Fatalf("primary node = %+v, want primary certificate identity", node)
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
	if node.Name != "worker-1" || node.Identifier != "requesting-id" {
		t.Fatalf("node identity = name %q identifier %q, want worker-1/requesting-id", node.Name, node.Identifier)
	}
	if node.EnrollmentID == nil || *node.EnrollmentID != req.ID {
		t.Fatalf("node enrollment id = %v, want %d", node.EnrollmentID, req.ID)
	}
	if len(node.Roles) != 1 || node.Roles[0] != NodeRoleSecondary {
		t.Fatalf("node roles = %+v, want secondary", node.Roles)
	}
}

func TestDeploymentNodeIDPopulatedOnWrites(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := store.EnsurePrimaryNode("primary", "primary-id")

	cfg := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{
		SpaceID: DefaultSpaceID,
		Machine: "primary-id",
		Name:    "api",
	}, SystemDeploymentSpec(), apigen.DesiredState{Version: "v1", Running: true})
	if cfg.NodeID != node.ID {
		t.Fatalf("created node ID = %d, want %d", cfg.NodeID, node.ID)
	}

	updated, changed, versionOK := store.UpdateDeploymentConfig(apigen.Context{}, cfg.ID, DeploymentConfigUpdate{
		ExpectedVersion: 2,
		DesiredState:    &apigen.DesiredState{Version: "v2", Running: true},
	})
	if !changed || !versionOK || updated.NodeID != node.ID {
		t.Fatalf("updated config = %+v, changed=%v versionOK=%v, want node ID %d", updated, changed, versionOK, node.ID)
	}

	history := store.MustFetchDeploymentHistory(cfg.ID)
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}
	for _, entry := range history {
		if entry.NodeID != 0 {
			t.Fatalf("history version %d node ID = %d, want 0", entry.Version, entry.NodeID)
		}
	}
}

func TestRenameNodePreservesIdentifier(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	primaryNode := store.EnsurePrimaryNode("primary", "primary-id")
	store.EnsureSystemDeployment("primary-id", "v1")

	node, err := store.RenameNode("primary-id", "control plane")
	if err != nil {
		t.Fatalf("RenameNode: %v", err)
	}
	if node.Name != "control plane" || node.Identifier != "primary-id" {
		t.Fatalf("renamed node = %+v", node)
	}
	configs := store.FetchDeploymentSnapshot(func(cfg apigen.DeploymentConfig) bool {
		return cfg.NodeID == primaryNode.ID
	})
	if len(configs) == 0 || configs[0].Config.ConfigID.Machine != "primary-id" {
		t.Fatalf("deployment targets after rename = %+v", configs)
	}
}
