package state

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestPrimaryStorageIgnoresRetiredDesiredStateColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	if err := Open(dbPath).Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`ALTER TABLE deployment_configs ADD COLUMN desired_version TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE deployment_configs ADD COLUMN desired_running INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE deployment_config_versions ADD COLUMN desired_version TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE deployment_config_versions ADD COLUMN desired_running INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store := Open(dbPath)
	node := testNode(store, "primary")
	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
		SpaceID: DefaultSpaceID,
		Name:    "api",
	}, node.ID, testSpecWithState("v1", true))
	store.MustSetDeploymentWorkloadState(apigen.Context{}, cfg.ID, "v2", false)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = Open(dbPath)
	defer store.Close()
	reloaded := store.configCache[cfg.ID]
	if reloaded == nil || reloaded.WorkloadVersion() != "v2" || reloaded.WorkloadRunning() {
		t.Fatalf("reloaded deployment = %+v, want stopped v2", reloaded)
	}
}

func testNode(store *Service, identifier string) *Node {
	return store.EnsurePrimaryNode(identifier, identifier)
}

func TestInvalidateNodeRuntimeStatePreservesConfigAndHistory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := Open(dbPath)
	defer func() { _ = store.Close() }()

	primaryNode := testNode(store, "primary")
	workerNode := testNode(store, "worker")
	create := func(nodeID int32, name string, spec *apigen.DeploymentSpec) *apigen.DeploymentConfig {
		return store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
			SpaceID: DefaultSpaceID,
			Name:    name,
		}, nodeID, spec)
	}
	containerSpec := testSpecWithState("v1", true)
	primary := create(primaryNode.ID, "app", containerSpec)
	worker := create(workerNode.ID, "app", containerSpec)
	system := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
		SpaceID: OpendeploySpaceID,
		Name:    internaldeploy.SelfName,
	}, primaryNode.ID, testSystemSpecWithState("v1", true))

	seedStatus := func(cfg *apigen.DeploymentConfig, artifact string) *apigen.ScheduledInstance {
		inst := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
		store.MustWriteScheduledInstanceStatus(inst.ID, func(status *apigen.ScheduledInstanceStatus) bool {
			status.BumpUpdatedAt()
			status.Preparer = apigen.PreparerStatus{DeploymentConfigVersion: cfg.Version, Artifact: artifact, Inputs: apigen.InputsStatus_INPUTS_READY, Image: apigen.ImageStatus_IMAGE_READY}
			status.Runner = apigen.RunnerStatus{DeploymentConfigVersion: cfg.Version, RunningArtifact: artifact, Status: apigen.RunningStatus_RUNNING}
			return true
		})
		return inst
	}
	primaryInst := seedStatus(primary, "example/app:v1")
	seedStatus(worker, "example/app:v1")
	seedStatus(system, "/var/lib/opendeploy/releases/v1/opendeploy")

	primaryConfigHistoryCount := len(store.MustFetchDeploymentHistory(primary.ID))
	primaryConfigVersion := primary.Version

	count, err := store.InvalidateNodeRuntimeState(primaryNode.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("invalidated count = %d, want 1", count)
	}
	got := store.FetchScheduledInstanceStatus(primaryInst.ID)
	if got != nil && (!got.Preparer.IsZero() || !got.Runner.IsZero()) {
		t.Fatalf("primary runtime status was not cleared: %+v", got)
	}
	if len(store.MustFetchDeploymentHistory(primary.ID)) != primaryConfigHistoryCount {
		t.Fatal("runtime invalidation changed config history")
	}
	workerInst := store.ListNonFinalScheduledInstancesForDeployment(worker.ID)[0]
	if store.FetchScheduledInstanceStatus(workerInst.ID).Runner.Status != apigen.RunningStatus_RUNNING {
		t.Fatal("worker runtime status was cleared")
	}
	systemInst := store.ListNonFinalScheduledInstancesForDeployment(system.ID)[0]
	if store.FetchScheduledInstanceStatus(systemInst.ID).Runner.Status != apigen.RunningStatus_RUNNING {
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
	store = Open(dbPath)
	got = store.FetchScheduledInstanceStatus(primaryInst.ID)
	if got != nil && (!got.Preparer.IsZero() || !got.Runner.IsZero()) {
		t.Fatalf("persisted primary runtime status was not cleared correctly: %+v", got)
	}
}

func TestEnsureSystemDeploymentRepairsExistingSpec(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	node := testNode(store, "primary")
	cid := &apigen.DeploymentIdentity{SpaceID: OpendeploySpaceID, Name: internaldeploy.SelfName}
	created := store.MustCreateDeploymentForNode(apigen.Context{}, cid, node.ID, testSpecWithState("", false))
	store.MustSetDeploymentWorkloadState(apigen.Context{}, created.ID, "v0.0.194", true)

	store.EnsureSystemDeployment(node.ID, "v0.0.195")

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
	if !internaldeploy.IsSelfSpec(&repaired.Spec) {
		t.Fatalf("spec was not repaired: %+v", repaired.Spec)
	}
	if repaired.WorkloadVersion() != "v0.0.194" || !repaired.WorkloadRunning() {
		t.Fatalf("workload state = %q/%v, want preserved running v0.0.194", repaired.WorkloadVersion(), repaired.WorkloadRunning())
	}
}

func TestEnsureNetproxyDeploymentCreatesInternalConfig(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	node := testNode(store, "primary")
	cfg := store.EnsureNetproxyDeployment(node.ID, "v0.0.200")
	if cfg == nil {
		t.Fatal("netproxy config not returned")
	}
	if cfg.NodeID != node.ID || cfg.Identity.SpaceID != OpendeploySpaceID || cfg.Identity.Name != internaldeploy.NetproxyName {
		t.Fatalf("unexpected config id: %+v", cfg.Identity)
	}
	if !internaldeploy.IsNetproxyConfig(cfg) || !internaldeploy.IsInternalConfig(cfg) {
		t.Fatalf("netproxy config not recognized as internal: %+v", cfg.Identity)
	}
	if !cfg.WorkloadRunning() || cfg.WorkloadVersion() != "v0.0.200" {
		t.Fatalf("workload state = %q/%v, want running v0.0.200", cfg.WorkloadVersion(), cfg.WorkloadRunning())
	}
	if cfg.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL || len(cfg.Spec.Networking.PortForwarding) != 0 {
		t.Fatalf("unexpected networking config: %+v", cfg.Spec.Networking)
	}
	if got := cfg.Spec.Container().Runtime.FileDescriptorLimit; got != netproxyFileDescriptorLimit {
		t.Fatalf("file descriptor limit = %d, want %d", got, netproxyFileDescriptorLimit)
	}
	again := store.EnsureNetproxyDeployment(node.ID, "v0.0.200")
	if again.Version != cfg.Version {
		t.Fatalf("ensure bumped unchanged netproxy version from %d to %d", cfg.Version, again.Version)
	}
}

func TestInternalDeploymentsAreScopedByNodeID(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	nodeA := testNode(store, "node-a")
	nodeB := testNode(store, "node-b")

	a := store.EnsureNetproxyDeployment(nodeA.ID, "v0.0.200")
	b := store.EnsureNetproxyDeployment(nodeB.ID, "v0.0.200")
	if a.ID == b.ID || a.NodeID != nodeA.ID || b.NodeID != nodeB.ID {
		t.Fatalf("netproxy deployments not scoped by node: a=%+v b=%+v", a, b)
	}
	if a.Identity.SpaceID != b.Identity.SpaceID || a.Identity.Name != b.Identity.Name {
		t.Fatalf("internal identities differ across nodes: a=%+v b=%+v", a.Identity, b.Identity)
	}
}

func TestEnsureNetproxyDeploymentRepairsExistingSpec(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	node := testNode(store, "primary")
	cfg := store.EnsureNetproxyDeployment(node.ID, "v0.0.200")
	broken := internaldeploy.NetproxySpec()
	broken.Container().Runtime.FileDescriptorLimit = 128
	store.MustUpdateDeploymentSpec(apigen.Context{}, cfg.ID, broken)
	brokenVersion := store.configCache[cfg.ID].Version

	repaired := store.EnsureNetproxyDeployment(node.ID, "v0.0.201")
	if repaired.Version <= brokenVersion {
		t.Fatalf("version = %d, want above broken version %d", repaired.Version, brokenVersion)
	}
	if got := repaired.Spec.Container().Runtime.FileDescriptorLimit; got != netproxyFileDescriptorLimit {
		t.Fatalf("file descriptor limit = %d, want %d", got, netproxyFileDescriptorLimit)
	}
	if repaired.WorkloadVersion() != "v0.0.200" || !repaired.WorkloadRunning() {
		t.Fatalf("workload state changed during repair: %q/%v", repaired.WorkloadVersion(), repaired.WorkloadRunning())
	}
}

func TestEnsureNetproxyDeploymentRepairsSpecOnceConcurrently(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	node := testNode(store, "primary")
	cfg := store.EnsureNetproxyDeployment(node.ID, "v0.0.200")
	broken := internaldeploy.NetproxySpec()
	broken.Container().Runtime.FileDescriptorLimit = 128
	store.MustUpdateDeploymentSpec(apigen.Context{}, cfg.ID, broken)
	brokenVersion := store.configCache[cfg.ID].Version

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { store.EnsureNetproxyDeployment(node.ID, "v0.0.200") })
	}
	wg.Wait()

	repaired := store.configCache[cfg.ID]
	if repaired.Version != brokenVersion+1 {
		t.Fatalf("version = %d, want one repair above %d", repaired.Version, brokenVersion)
	}
}

func TestEnsureNetproxyDeploymentPreservesDesiredStateConcurrently(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	node := testNode(store, "primary")
	cfg := store.EnsureNetproxyDeployment(node.ID, "v0.0.200")
	store.MustSetDeploymentWorkloadState(apigen.Context{}, cfg.ID, "v0.0.199", false)
	manualVersion := store.configCache[cfg.ID].Version

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { store.EnsureNetproxyDeployment(node.ID, "v0.0.201") })
	}
	wg.Wait()

	updated := store.configCache[cfg.ID]
	if updated.Version != manualVersion {
		t.Fatalf("version = %d, want unchanged manual version %d", updated.Version, manualVersion)
	}
	if updated.WorkloadVersion() != "v0.0.199" || updated.WorkloadRunning() {
		t.Fatalf("workload state = %q/%v, want stopped v0.0.199", updated.WorkloadVersion(), updated.WorkloadRunning())
	}
}

func TestEnsureNetproxyDeploymentRequiresExplicitVersion(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	node := testNode(store, "primary")
	defer func() {
		if recover() == nil {
			t.Fatal("EnsureNetproxyDeployment did not panic without version")
		}
	}()
	store.EnsureNetproxyDeployment(node.ID, "")
}

func TestEnsureNetproxyDeploymentPreservesExistingVersion(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	node := testNode(store, "primary")
	cfg := store.EnsureNetproxyDeployment(node.ID, "v0.0.200")

	again := store.EnsureNetproxyDeployment(node.ID, "v0.0.201")
	if again.WorkloadVersion() != "v0.0.200" {
		t.Fatalf("desired version = %q, want preserved v0.0.200", again.WorkloadVersion())
	}
	if again.Version != cfg.Version {
		t.Fatalf("version = %d, want unchanged %d", again.Version, cfg.Version)
	}
}

func TestEnsurePrimaryNodeCreatesPrimaryRole(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))

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
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := Open(dbPath)
	defer store.Close()
	seedDB := sqlitedb.MustOpen(dbPath)
	if _, err := seedDB.Exec(`
		INSERT INTO nodes (enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key)
		VALUES (NULL, 0, 'coflip-prod', 'coflip-prod', '[1]', '[]', '')`); err != nil {
		t.Fatalf("seed secondary node: %v", err)
	}
	if _, err := seedDB.Exec(`
		INSERT INTO nodes (enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key)
		VALUES (NULL, 0, 'primary', 'primary', '[0]', '[]', '')`); err != nil {
		t.Fatalf("seed primary node: %v", err)
	}
	if err := seedDB.Close(); err != nil {
		t.Fatal(err)
	}

	node := store.EnsurePrimaryNode("primary", "primary")
	if node.Name != "primary" || node.Identifier != "primary" {
		t.Fatalf("primary node = %+v, want primary certificate identity", node)
	}
}

func TestAcceptEnrollmentRequestCreatesNode(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	req := store.MustUpsertEnrollmentRequest("127.0.0.1", "requesting-id", "v0.0.200", "10.0.0.2")

	status, err := store.AcceptEnrollmentRequest(req.ID, "worker-1", req.RequestingMachineID, req.UnderlayAddress, req.UpdatedAt)
	if err != nil {
		t.Fatalf("AcceptEnrollmentRequest: %v", err)
	}
	if status.Status != EnrollmentStatusAccepted {
		t.Fatalf("status = %q, want accepted", status.Status)
	}
	if status.UnderlayAddress != "10.0.0.2" {
		t.Fatalf("underlay address = %q, want 10.0.0.2", status.UnderlayAddress)
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
	if len(node.Addresses) != 1 || node.Addresses[0] != "10.0.0.2" {
		t.Fatalf("node addresses = %v, want [10.0.0.2]", node.Addresses)
	}
}

func TestAcceptEnrollmentRequestRejectsReplacedSessionRevision(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	first := store.MustUpsertEnrollmentRequest("127.0.0.1", "requesting-id", "v1", "10.0.0.2")
	second := store.MustUpsertEnrollmentRequest("127.0.0.2", "requesting-id", "v2", "10.0.0.3")
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("replacement revision %s did not advance past %s", second.UpdatedAt, first.UpdatedAt)
	}
	_, err := store.AcceptEnrollmentRequest(first.ID, "worker-1", first.RequestingMachineID, first.UnderlayAddress, first.UpdatedAt)
	if !errors.Is(err, ErrEnrollmentRequestChanged) {
		t.Fatalf("accept error = %v, want ErrEnrollmentRequestChanged", err)
	}
	if nodes := store.ListNodes(); len(nodes) != 0 {
		t.Fatalf("stale enrollment created nodes: %+v", nodes)
	}
}

func TestDeploymentNodeIDPopulatedOnWrites(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := store.EnsurePrimaryNode("primary", "primary-id")

	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
		SpaceID: DefaultSpaceID,
		Name:    "api",
	}, node.ID, testSystemSpecWithState("v1", true))
	if cfg.NodeID != node.ID {
		t.Fatalf("created node ID = %d, want %d", cfg.NodeID, node.ID)
	}

	nextSpec := cfg.Spec
	if err := nextSpec.SetWorkloadState("v2", true); err != nil {
		t.Fatal(err)
	}
	updated, changed, versionOK := store.UpdateDeploymentConfig(apigen.Context{}, cfg.ID, DeploymentConfigUpdate{
		ExpectedVersion: 2,
		Spec:            &nextSpec,
	})
	if !changed || !versionOK || updated.NodeID != node.ID {
		t.Fatalf("updated config = %+v, changed=%v versionOK=%v, want node ID %d", updated, changed, versionOK, node.ID)
	}

	history := store.MustFetchDeploymentHistory(cfg.ID)
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}
	for _, entry := range history {
		if entry.NodeID != node.ID {
			t.Fatalf("history version %d node ID = %d, want %d", entry.Version, entry.NodeID, node.ID)
		}
	}
}

func TestSetDeploymentWorkloadStateReencodesSpec(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := Open(dbPath)
	node := testNode(store, "primary")
	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
		SpaceID: DefaultSpaceID,
		Name:    "api",
	}, node.ID, testSpecWithState("v1", true))

	store.MustSetDeploymentWorkloadState(apigen.Context{}, cfg.ID, "v2", false)

	row, err := store.q.GetDeploymentConfig(context.Background(), int64(cfg.ID))
	if err != nil {
		t.Fatalf("read updated deployment: %v", err)
	}
	assertPersistedWorkloadState(t, row.SpecBlob, "v2", false)
	history, err := store.q.ListDeploymentConfigVersions(context.Background(), int64(cfg.ID))
	if err != nil {
		t.Fatalf("read updated deployment history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}
	latest := history[len(history)-1]
	assertPersistedWorkloadState(t, latest.SpecBlob, "v2", false)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = Open(dbPath)
	defer store.Close()
	reloaded := store.configCache[cfg.ID]
	if reloaded == nil || reloaded.WorkloadVersion() != "v2" || reloaded.WorkloadRunning() {
		t.Fatalf("reloaded deployment = %+v, want stopped v2", reloaded)
	}
}

func assertPersistedWorkloadState(t *testing.T, blob []byte, wantVersion string, wantRunning bool) {
	t.Helper()
	spec, err := apigen.DecodeDeploymentSpec(blob)
	if err != nil {
		t.Fatalf("decode persisted deployment spec: %v", err)
	}
	if spec.WorkloadVersion() != wantVersion || spec.WorkloadRunning() != wantRunning {
		t.Fatalf("persisted workload = %q/%v, want %q/%v", spec.WorkloadVersion(), spec.WorkloadRunning(), wantVersion, wantRunning)
	}
}

func TestRenameNodePreservesIdentifier(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	primaryNode := store.EnsurePrimaryNode("primary", "primary-id")
	store.EnsureSystemDeployment(primaryNode.ID, "v1")

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
	if len(configs) == 0 || configs[0].NodeID != primaryNode.ID {
		t.Fatalf("deployment targets after rename = %+v", configs)
	}
}

func TestEnsureRunScheduledInstanceIsConcurrentAndIdempotent(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := store.EnsurePrimaryNode("primary", "primary-id")
	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
		SpaceID: DefaultSpaceID,
		Name:    "api",
	}, node.ID, testSpecWithState("v1", true))

	const callers = 16
	ids := make(chan int32, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inst, _ := store.EnsureRunScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
			ids <- inst.ID
		}()
	}
	wg.Wait()
	close(ids)
	var want int32
	for id := range ids {
		if want == 0 {
			want = id
		}
		if id != want {
			t.Fatalf("EnsureRunScheduledInstance returned ids %d and %d", want, id)
		}
	}
	active := store.ListNonFinalScheduledInstancesForDeployment(cfg.ID)
	if len(active) != 1 || active[0].ID != want {
		t.Fatalf("active instances = %+v, want one id %d", active, want)
	}
}

func testSystemSpecWithState(version string, running bool) *apigen.DeploymentSpec {
	spec := internaldeploy.SelfSpec()
	if err := spec.SetWorkloadState(version, running); err != nil {
		panic(err)
	}
	return spec
}
