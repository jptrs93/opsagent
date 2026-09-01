package state

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func testNode(store *Service, identifier string) *Node {
	return store.EnsurePrimaryNode(identifier, identifier)
}

func TestInvalidateNodeRuntimeStatePreservesConfigAndHistory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := Open(dbPath)
	defer func() { _ = store.Close() }()

	primaryNode := testNode(store, "primary")
	secondaryNode := testNode(store, "secondary")
	create := func(nodeID int32, name string, spec *apigen.DeploymentSpec) *apigen.Deployment {
		return store.MustCreateDeploymentForNode(apigen.Context{}, DefaultSpaceID, name, nodeID, spec)
	}
	containerSpec := testSpecWithState("v1", true)
	primary := create(primaryNode.ID, "app", containerSpec)
	secondary := create(secondaryNode.ID, "app", containerSpec)
	system := store.MustCreateDeploymentForNode(apigen.Context{}, OpendeploySpaceID, internaldeploy.SelfName, primaryNode.ID, testSystemSpecWithState("v1", true))

	seedStatus := func(cfg *apigen.Deployment, artifact string) *apigen.ScheduledInstance {
		inst := store.CreateScheduledInstanceForTest(cfg.ID, cfg.SpecVersion, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
		store.MustWriteScheduledInstanceStatus(inst.ID, func(status *apigen.ScheduledInstanceStatus) bool {
			status.BumpUpdatedAt()
			status.Preparer = apigen.PreparerStatus{DeploymentSpecVersion: cfg.SpecVersion, Artifact: artifact, Inputs: apigen.InputsStatus_INPUTS_READY, Image: apigen.ImageStatus_IMAGE_READY}
			status.Runner = apigen.RunnerStatus{DeploymentSpecVersion: cfg.SpecVersion, RunningArtifact: artifact, Status: apigen.RunningStatus_RUNNING}
			return true
		})
		return inst
	}
	primaryInst := seedStatus(primary, "example/app:v1")
	seedStatus(secondary, "example/app:v1")
	seedStatus(system, "/var/lib/opendeploy/releases/v1/opendeploy")

	primaryConfigHistoryCount := len(store.MustFetchDeploymentHistory(primary.ID))
	primaryConfigVersion := primary.SpecVersion

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
	secondaryInst := store.ListNonFinalScheduledInstancesForDeployment(secondary.ID)[0]
	if store.FetchScheduledInstanceStatus(secondaryInst.ID).Runner.Status != apigen.RunningStatus_RUNNING {
		t.Fatal("secondary runtime status was cleared")
	}
	systemInst := store.ListNonFinalScheduledInstancesForDeployment(system.ID)[0]
	if store.FetchScheduledInstanceStatus(systemInst.ID).Runner.Status != apigen.RunningStatus_RUNNING {
		t.Fatal("primary system deployment runtime status was cleared")
	}
	for _, cfg := range store.ListActiveDeployments() {
		if cfg.ID == primary.ID && cfg.SpecVersion != primaryConfigVersion {
			t.Fatalf("primary spec version = %d, want %d", cfg.SpecVersion, primaryConfigVersion)
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
	created := store.MustCreateDeploymentForNode(apigen.Context{}, OpendeploySpaceID, internaldeploy.SelfName, node.ID, testSpecWithState("", false))
	mustSetDeploymentWorkloadState(store, apigen.Context{}, created.ID, "v0.0.194", true)

	store.EnsureSystemDeployment(node.ID, "v0.0.195")

	var repaired *apigen.Deployment
	for _, cfg := range store.ListActiveDeployments() {
		if cfg.ID == created.ID {
			repaired = cfg
			break
		}
	}
	if repaired == nil {
		t.Fatal("repaired deployment not found")
	}
	if repaired.SpecVersion <= created.SpecVersion {
		t.Fatalf("version = %d, want repaired version above %d", repaired.SpecVersion, created.SpecVersion)
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
	if cfg.NodeID != node.ID || cfg.SpaceID != OpendeploySpaceID || cfg.Name != internaldeploy.NetproxyName {
		t.Fatalf("unexpected config identity: node=%d space=%d name=%q", cfg.NodeID, cfg.SpaceID, cfg.Name)
	}
	if !internaldeploy.IsNetproxyConfig(cfg) || !internaldeploy.IsInternalConfig(cfg) {
		t.Fatalf("netproxy config not recognized as internal: space=%d name=%q", cfg.SpaceID, cfg.Name)
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
	if again.SpecVersion != cfg.SpecVersion {
		t.Fatalf("ensure bumped unchanged netproxy version from %d to %d", cfg.SpecVersion, again.SpecVersion)
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
	if a.SpaceID != b.SpaceID || a.Name != b.Name {
		t.Fatalf("internal identities differ across nodes: a=%d/%q b=%d/%q", a.SpaceID, a.Name, b.SpaceID, b.Name)
	}
}

func TestEnsureNetproxyDeploymentRepairsExistingSpec(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	node := testNode(store, "primary")
	cfg := store.EnsureNetproxyDeployment(node.ID, "v0.0.200")
	broken := internaldeploy.NetproxySpec()
	broken.Container().Runtime.FileDescriptorLimit = 128
	mustUpdateDeploymentSpec(store, apigen.Context{}, cfg.ID, broken)
	brokenVersion := store.deploymentCache[cfg.ID].SpecVersion

	repaired := store.EnsureNetproxyDeployment(node.ID, "v0.0.201")
	if repaired.SpecVersion <= brokenVersion {
		t.Fatalf("version = %d, want above broken version %d", repaired.SpecVersion, brokenVersion)
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
	mustUpdateDeploymentSpec(store, apigen.Context{}, cfg.ID, broken)
	brokenVersion := store.deploymentCache[cfg.ID].SpecVersion

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { store.EnsureNetproxyDeployment(node.ID, "v0.0.200") })
	}
	wg.Wait()

	repaired := store.deploymentCache[cfg.ID]
	if repaired.SpecVersion != brokenVersion+1 {
		t.Fatalf("version = %d, want one repair above %d", repaired.SpecVersion, brokenVersion)
	}
}

func TestEnsureNetproxyDeploymentPreservesDesiredStateConcurrently(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	node := testNode(store, "primary")
	cfg := store.EnsureNetproxyDeployment(node.ID, "v0.0.200")
	mustSetDeploymentWorkloadState(store, apigen.Context{}, cfg.ID, "v0.0.199", false)
	manualVersion := store.deploymentCache[cfg.ID].SpecVersion

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { store.EnsureNetproxyDeployment(node.ID, "v0.0.201") })
	}
	wg.Wait()

	updated := store.deploymentCache[cfg.ID]
	if updated.SpecVersion != manualVersion {
		t.Fatalf("version = %d, want unchanged manual version %d", updated.SpecVersion, manualVersion)
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
	if again.SpecVersion != cfg.SpecVersion {
		t.Fatalf("version = %d, want unchanged %d", again.SpecVersion, cfg.SpecVersion)
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
	if node.Status != apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL {
		t.Fatalf("primary status = %v, want member normal", node.Status)
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
	for _, seed := range []struct{ name, roles string }{
		{"coflip-prod", "[1]"},
		{"primary", "[0]"},
	} {
		if _, err := seedDB.Exec(`
			INSERT INTO nodes (created_at, enrolled_at, name, identifier)
			VALUES (0, 0, ?1, ?1)`, seed.name); err != nil {
			t.Fatalf("seed node %s: %v", seed.name, err)
		}
		if _, err := seedDB.Exec(`
			INSERT INTO node_versions (node_id, version, created_at, status, roles, addresses, wg_public_key, allowed_spaces, global_seq)
			SELECT id, 1, 0, 4, ?2, '[]', '', '[0]', 0 FROM nodes WHERE identifier = ?1`, seed.name, seed.roles); err != nil {
			t.Fatalf("seed node version %s: %v", seed.name, err)
		}
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
	const wgPublicKey = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="
	req, expectedVersion := store.MustUpsertEnrollmentRequest("127.0.0.1", "requesting-id", "v0.0.200", "10.0.0.2", wgPublicKey)
	if req.Status != apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUESTED || !req.IsConnected {
		t.Fatalf("request = %+v, want connected enrollment-requested", req)
	}
	if req.RequestingIpAddress != "127.0.0.1" || req.OpendeployVersion != "v0.0.200" {
		t.Fatalf("request observed meta = %+v, want 127.0.0.1/v0.0.200", req)
	}
	if members := store.ListNodes(); len(members) != 0 {
		t.Fatalf("requested node listed as member: %+v", members)
	}

	status, err := store.AcceptEnrollmentRequest(req.ID, "secondary-1", req.RequestingMachineID, req.UnderlayAddress, wgPublicKey, expectedVersion)
	if err != nil {
		t.Fatalf("AcceptEnrollmentRequest: %v", err)
	}
	if status.Status != apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL {
		t.Fatalf("status = %v, want member normal", status.Status)
	}
	if status.UnderlayAddress != "10.0.0.2" {
		t.Fatalf("underlay address = %q, want 10.0.0.2", status.UnderlayAddress)
	}
	nodes := store.ListNodes()
	if len(nodes) != 1 {
		t.Fatalf("node count = %d, want 1: %+v", len(nodes), nodes)
	}
	node := nodes[0]
	if node.Name != "secondary-1" || node.Identifier != "requesting-id" {
		t.Fatalf("node identity = name %q identifier %q, want secondary-1/requesting-id", node.Name, node.Identifier)
	}
	if node.ID != req.ID {
		t.Fatalf("node id = %d, want request id %d", node.ID, req.ID)
	}
	if node.EnrolledAt.IsZero() || node.CreatedAt.IsZero() {
		t.Fatalf("node timestamps = %+v, want created and enrolled set", node)
	}
	if len(node.Roles) != 1 || node.Roles[0] != NodeRoleSecondary {
		t.Fatalf("node roles = %+v, want secondary", node.Roles)
	}
	if len(node.Addresses) != 1 || node.Addresses[0] != "10.0.0.2" {
		t.Fatalf("node addresses = %v, want [10.0.0.2]", node.Addresses)
	}
	if node.WGPublicKey != wgPublicKey {
		t.Fatalf("node wg public key = %q, want %q", node.WGPublicKey, wgPublicKey)
	}
}

func TestSetNodeWGPublicKeyIsDiffGatedAndVersioned(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := store.EnsurePrimaryNode("primary", "primary-id")

	const keyA = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="
	const keyB = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI="

	before := store.FetchNetworkMapInputs().Seq
	updated := store.MustSetNodeWGPublicKey(node.ID, keyA)
	if updated.WGPublicKey != keyA {
		t.Fatalf("node wg key = %q, want %q", updated.WGPublicKey, keyA)
	}
	afterSet := store.FetchNetworkMapInputs().Seq
	if afterSet != before+1 {
		t.Fatalf("seq after set = %d, want %d", afterSet, before+1)
	}

	store.MustSetNodeWGPublicKey(node.ID, keyA)
	if seq := store.FetchNetworkMapInputs().Seq; seq != afterSet {
		t.Fatalf("unchanged key advanced seq to %d", seq)
	}

	store.MustSetNodeWGPublicKey(node.ID, keyB)
	if seq := store.FetchNetworkMapInputs().Seq; seq != afterSet+1 {
		t.Fatalf("seq after change = %d, want %d", seq, afterSet+1)
	}

	versions, err := store.q.ListNodeVersionRows(context.Background(), int64(node.ID))
	if err != nil {
		t.Fatalf("querying node versions: %v", err)
	}
	if len(versions) != 3 || versions[0].WgPublicKey != "" || versions[1].WgPublicKey != keyA || versions[2].WgPublicKey != keyB {
		t.Fatalf("version history = %+v, want keys ['' %s %s]", versions, keyA, keyB)
	}
	for i, version := range versions {
		if version.Version != int64(i+1) {
			t.Fatalf("version numbers = %+v, want dense from 1", versions)
		}
		if version.GlobalSeq <= 0 || (i > 0 && version.GlobalSeq <= versions[i-1].GlobalSeq) {
			t.Fatalf("version seq stamps = %+v, want positive and increasing", versions)
		}
	}
}

func TestAcceptEnrollmentRequestRejectsReplacedSessionRevision(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	first, firstVersion := store.MustUpsertEnrollmentRequest("127.0.0.1", "requesting-id", "v1", "10.0.0.2", "")
	second, secondVersion := store.MustUpsertEnrollmentRequest("127.0.0.2", "requesting-id", "v2", "10.0.0.3", "")
	if second.ID != first.ID {
		t.Fatalf("replacement request id = %d, want %d", second.ID, first.ID)
	}
	if secondVersion <= firstVersion {
		t.Fatalf("replacement revision %d did not advance past %d", secondVersion, firstVersion)
	}
	_, err := store.AcceptEnrollmentRequest(first.ID, "secondary-1", first.RequestingMachineID, first.UnderlayAddress, "", firstVersion)
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

	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, DefaultSpaceID, "api", node.ID, testSystemSpecWithState("v1", true))
	if cfg.NodeID != node.ID {
		t.Fatalf("created node ID = %d, want %d", cfg.NodeID, node.ID)
	}

	nextSpec := cfg.Spec
	if err := nextSpec.SetWorkloadState("v2", true); err != nil {
		t.Fatal(err)
	}
	updated, changed, versionOK := store.UpdateDeploymentSpec(apigen.Context{}, cfg.ID, DeploymentSpecUpdate{
		ExpectedSpecVersion: 2,
		Spec:                &nextSpec,
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
			t.Fatalf("history version %d node ID = %d, want %d", entry.SpecVersion, entry.NodeID, node.ID)
		}
	}
}

func TestSetDeploymentWorkloadStateReencodesSpec(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := Open(dbPath)
	node := testNode(store, "primary")
	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, DefaultSpaceID, "api", node.ID, testSpecWithState("v1", true))

	mustSetDeploymentWorkloadState(store, apigen.Context{}, cfg.ID, "v2", false)

	event, err := store.q.GetLatestDeploymentEvent(context.Background(), int64(cfg.ID))
	if err != nil {
		t.Fatalf("read updated deployment: %v", err)
	}
	latestSpec := deploymentEventToProto(event).Spec
	assertPersistedWorkloadState(t, latestSpec.Encode(), "v2", false)
	history, err := store.q.ListDeploymentEvents(context.Background(), int64(cfg.ID))
	if err != nil {
		t.Fatalf("read updated deployment history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}
	historySpec := deploymentEventToProto(history[len(history)-1]).Spec
	assertPersistedWorkloadState(t, historySpec.Encode(), "v2", false)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = Open(dbPath)
	defer store.Close()
	reloaded := store.deploymentCache[cfg.ID]
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
	configs := store.FetchDeploymentSnapshot(func(cfg apigen.Deployment) bool {
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
	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, DefaultSpaceID, "api", node.ID, testSpecWithState("v1", true))

	const callers = 16
	ids := make(chan int32, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inst, _ := store.EnsureRunScheduledInstance(cfg.ID, cfg.SpecVersion, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
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
