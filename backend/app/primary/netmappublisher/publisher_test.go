package netmappublisher

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/util/version"
)

const (
	testWGKeyA = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="
	testWGKeyB = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI="
)

func renderNI(prefix network.Prefix, nodes []*state.Node, instances []apigen.ScheduledInstanceState) (*apigen.ClusterNetMap, error) {
	return render(prefix, state.NetworkMapInputs{Nodes: nodes, Instances: instances})
}

func TestPublisherStampsAndCoalescesLatestMap(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	prefix := network.GeneratePrefix()
	store := state.Open(dbPath)
	node := store.EnsurePrimaryNode("primary", "primary-id")
	store.MustSetNodeAddresses(node.ID, []string{"192.0.2.10"})
	store.MustSetNodeWGPublicKey(node.ID, testWGKeyA)
	store.EnsureNetproxyDeployment(node.ID, version.Version)

	publisher, err := New(store, prefix)
	if err != nil {
		t.Fatal(err)
	}
	initial := publisher.SnapshotForNode(node.ID)
	if initial == nil || initial.DerivedFromSeq <= 0 || initial.TargetNodeID != node.ID {
		t.Fatalf("unexpected initial map: %+v", initial)
	}
	if len(initial.Nodes) != 1 || initial.Nodes[0].UnderlayAddress != "192.0.2.10" {
		t.Fatalf("initial nodes = %+v", initial.Nodes)
	}
	if len(initial.Routes) != 0 {
		t.Fatalf("initial routes = %+v, want none before the netproxy is scheduled", initial.Routes)
	}
	if err := publisher.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := publisher.SnapshotForNode(node.ID).DerivedFromSeq; got != initial.DerivedFromSeq {
		t.Fatalf("unchanged refresh stamp = %d, want %d", got, initial.DerivedFromSeq)
	}

	_, updates, unsubscribe := publisher.SnapshotAndSubscribe(node.ID)
	defer unsubscribe()
	store.MustSetNodeAddresses(node.ID, []string{"192.0.2.11"})
	if err := publisher.Refresh(); err != nil {
		t.Fatal(err)
	}
	store.MustSetNodeAddresses(node.ID, []string{"192.0.2.12"})
	if err := publisher.Refresh(); err != nil {
		t.Fatal(err)
	}
	latest := <-updates
	if latest.DerivedFromSeq <= initial.DerivedFromSeq || latest.Nodes[0].UnderlayAddress != "192.0.2.12" {
		t.Fatalf("coalesced map = %+v", latest)
	}

	wantContent := string(canonicalContent(latest))
	wantStamp := latest.DerivedFromSeq
	publisher.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = state.Open(dbPath)
	defer store.Close()
	restarted, err := New(store, prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	got := restarted.SnapshotForNode(node.ID)
	if string(canonicalContent(got)) != wantContent {
		t.Fatalf("restarted map content = %+v, want the same derivation", got)
	}
	if got.DerivedFromSeq < wantStamp {
		t.Fatalf("restarted stamp = %d, below %d: the counter never goes backwards", got.DerivedFromSeq, wantStamp)
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	prefix := network.GeneratePrefix()
	nodesA := []*state.Node{
		{ID: 2, Addresses: []string{"2001:db8::2"}, WGPublicKey: testWGKeyB},
		{ID: 1, Addresses: []string{"2001:db8::1"}, WGPublicKey: testWGKeyA},
	}
	instancesA := []apigen.ScheduledInstanceState{
		servingInstance(200, 20, 2, 4),
		servingInstance(100, 10, 1, 3),
	}
	nodesB := []*state.Node{nodesA[1], nodesA[0]}
	instancesB := []apigen.ScheduledInstanceState{instancesA[1], instancesA[0]}
	a, err := renderNI(prefix, nodesA, instancesA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := renderNI(prefix, nodesB, instancesB)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonicalContent(a)) != string(canonicalContent(b)) {
		t.Fatalf("render depends on input order:\n%+v\n%+v", a, b)
	}
	// One serving placement contributes its own /120 plus its instance /100.
	if len(a.Routes) != 4 {
		t.Fatalf("routes = %+v, want a placement and an instance prefix per deployment", a.Routes)
	}
}

func TestRenderOmitsHostNetworkingAndNonRunnableStates(t *testing.T) {
	prefix := network.GeneratePrefix()
	nodes := []*state.Node{{ID: 1, Addresses: []string{"192.0.2.1"}, WGPublicKey: testWGKeyA}}

	host := servingInstance(101, 11, 1, 3)
	host.Config.Spec.Networking.Mode = apigen.NetworkingMode_NETWORKING_MODE_HOST
	terminating := servingInstance(102, 12, 1, 3)
	terminating.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE
	finalized := servingInstance(103, 13, 1, 3)
	finalized.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED

	got, err := renderNI(prefix, nodes, []apigen.ScheduledInstanceState{
		host, terminating, finalized, servingInstance(100, 10, 1, 3),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Routes) != 2 {
		t.Fatalf("routes = %+v, want only the running virtual placement", got.Routes)
	}
}

// TestRenderIgnoresRunnerStatus is the property the whole design rests on:
// routing is a function of assignments alone. A restart changes the run number
// and a crash changes the runner state, and neither may move a route or mint a
// new sequence.
func TestRenderIgnoresRunnerStatus(t *testing.T) {
	prefix := network.GeneratePrefix()
	nodes := []*state.Node{{ID: 1, Addresses: []string{"192.0.2.1"}, WGPublicKey: testWGKeyA}}
	quiet := servingInstance(100, 10, 1, 3)

	restarted := servingInstance(100, 10, 1, 3)
	restarted.Status.Runner.NumberOfRestarts = 12
	crashed := servingInstance(100, 10, 1, 3)
	crashed.Status.Runner.Status = apigen.RunningStatus_CRASHED
	starting := servingInstance(100, 10, 1, 3)
	starting.Status.Runner.Status = apigen.RunningStatus_STARTING
	noStatus := servingInstance(100, 10, 1, 3)
	noStatus.Status = apigen.ScheduledInstanceStatus{}

	base, err := renderNI(prefix, nodes, []apigen.ScheduledInstanceState{quiet})
	if err != nil {
		t.Fatal(err)
	}
	for name, item := range map[string]apigen.ScheduledInstanceState{
		"restarted": restarted, "crashed": crashed, "starting": starting, "no status": noStatus,
	} {
		got, err := renderNI(prefix, nodes, []apigen.ScheduledInstanceState{item})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(canonicalContent(got)) != string(canonicalContent(base)) {
			t.Fatalf("%s changed the map:\n%+v\nwant\n%+v", name, got.Routes, base.Routes)
		}
	}
}

// TestRenderCrossNodeRolloverKeepsDrainingPlacementReachable walks the routing
// through a cross-node rollover. The point of the placement /120 is that the
// draining side keeps a path home for replies after the instance /100 has
// already moved to its replacement.
func TestRenderCrossNodeRolloverKeepsDrainingPlacementReachable(t *testing.T) {
	prefix := network.GeneratePrefix()
	nodes := []*state.Node{
		{ID: 1, Addresses: []string{"192.0.2.1"}, WGPublicKey: testWGKeyA},
		{ID: 2, Addresses: []string{"192.0.2.2"}, WGPublicKey: testWGKeyB},
	}
	instancePrefix, err := prefix.InstanceCIDR(3, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	oldPlacement, err := prefix.PlacementCIDR(3, 10, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	newPlacement, err := prefix.PlacementCIDR(3, 10, 0, 101)
	if err != nil {
		t.Fatal(err)
	}

	// Warming up: the replacement on node 2 already owns its own placement
	// prefix, so its outbound traffic is routable before it ever serves.
	old := servingInstance(100, 10, 1, 3)
	replacement := servingInstance(101, 10, 2, 3)
	replacement.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY
	warming, err := renderNI(prefix, nodes, []apigen.ScheduledInstanceState{old, replacement})
	if err != nil {
		t.Fatal(err)
	}
	assertRoutes(t, "warming up", warming, map[string]int32{
		instancePrefix.String(): 1,
		oldPlacement.String():   1,
		newPlacement.String():   2,
	})

	// Promoted: the instance prefix follows the replacement, while the draining
	// placement keeps its own prefix pointed at the node still running it.
	old.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING
	replacement.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING
	promoted, err := renderNI(prefix, nodes, []apigen.ScheduledInstanceState{old, replacement})
	if err != nil {
		t.Fatal(err)
	}
	assertRoutes(t, "promoted", promoted, map[string]int32{
		instancePrefix.String(): 2,
		oldPlacement.String():   1,
		newPlacement.String():   2,
	})

	// Retired: nothing left of the old placement.
	old.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE
	retired, err := renderNI(prefix, nodes, []apigen.ScheduledInstanceState{old, replacement})
	if err != nil {
		t.Fatal(err)
	}
	assertRoutes(t, "retired", retired, map[string]int32{
		instancePrefix.String(): 2,
		newPlacement.String():   2,
	})
}

// TestRenderSameNodeRolloverChangesNothing is why a same-node promotion needs
// no propagation barrier and no special case to detect one: the map before and
// after the flip is byte-identical, so Refresh mints no new sequence and there
// is nothing for anyone to wait on.
func TestRenderSameNodeRolloverChangesNothing(t *testing.T) {
	prefix := network.GeneratePrefix()
	nodes := []*state.Node{{ID: 1, Addresses: []string{"192.0.2.1"}, WGPublicKey: testWGKeyA}}
	old := servingInstance(100, 10, 1, 3)
	replacement := servingInstance(101, 10, 1, 3)
	replacement.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY

	before, err := renderNI(prefix, nodes, []apigen.ScheduledInstanceState{old, replacement})
	if err != nil {
		t.Fatal(err)
	}
	old.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING
	replacement.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING
	after, err := renderNI(prefix, nodes, []apigen.ScheduledInstanceState{old, replacement})
	if err != nil {
		t.Fatal(err)
	}
	if string(canonicalContent(before)) != string(canonicalContent(after)) {
		t.Fatalf("same-node promotion changed the map:\nbefore %+v\nafter  %+v", before.Routes, after.Routes)
	}
}

func TestRenderRejectsTwoServingPlacements(t *testing.T) {
	prefix := network.GeneratePrefix()
	nodes := []*state.Node{
		{ID: 1, Addresses: []string{"192.0.2.1"}, WGPublicKey: testWGKeyA},
		{ID: 2, Addresses: []string{"192.0.2.2"}, WGPublicKey: testWGKeyB},
	}
	first := servingInstance(100, 10, 1, 3)
	second := servingInstance(101, 10, 2, 3)
	if _, err := renderNI(prefix, nodes, []apigen.ScheduledInstanceState{first, second}); err == nil {
		t.Fatal("two serving placements of one ordinal accepted: the map cannot express it")
	}
}

func TestRenderRejectsUnknownNode(t *testing.T) {
	prefix := network.GeneratePrefix()
	nodes := []*state.Node{{ID: 1, Addresses: []string{"192.0.2.1"}, WGPublicKey: testWGKeyA}}
	orphan := servingInstance(100, 10, 9, 3)
	if _, err := renderNI(prefix, nodes, []apigen.ScheduledInstanceState{orphan}); err == nil {
		t.Fatal("placement on an unknown node accepted")
	}
}

func TestPublishLatestDoesNotBlockIfSubscriberDrains(t *testing.T) {
	ch := make(chan *apigen.ClusterNetMap, 1)
	ch <- &apigen.ClusterNetMap{DerivedFromSeq: 1}
	drained := make(chan struct{})
	go func() {
		<-ch
		close(drained)
	}()
	<-drained
	done := make(chan struct{})
	go func() {
		publishLatest(ch, &apigen.ClusterNetMap{DerivedFromSeq: 2})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publishLatest blocked after concurrent drain")
	}
}

func assertRoutes(t *testing.T, stage string, got *apigen.ClusterNetMap, want map[string]int32) {
	t.Helper()
	actual := make(map[string]int32, len(got.Routes))
	for _, route := range got.Routes {
		actual[route.LogicalPrefix] = route.HostingNodeID
	}
	if len(actual) != len(want) {
		t.Fatalf("%s: routes = %+v, want %d entries %+v", stage, actual, len(want), want)
	}
	for destination, nodeID := range want {
		if actual[destination] != nodeID {
			t.Fatalf("%s: route %s hosted on node %d, want %d (routes %+v)",
				stage, destination, actual[destination], nodeID, actual)
		}
	}
}

func virtualDeployment(id, nodeID, spaceID int32) apigen.DeploymentConfig {
	return apigen.DeploymentConfig{
		ID:      id,
		NodeID:  nodeID,
		SpaceID: spaceID,
		Spec: apigen.DeploymentSpec{
			Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL},
			Container1Spec: &apigen.ContainerSpec{
				Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "example/app"}},
				Running: true,
			},
		},
	}
}

// servingInstance builds a running, serving placement. Status is populated with
// a healthy runner precisely so tests that vary it can show it makes no
// difference to what gets rendered.
func servingInstance(instanceID, deploymentID, nodeID, spaceID int32) apigen.ScheduledInstanceState {
	return apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{
			ID:                instanceID,
			DeploymentID:      deploymentID,
			DeploymentVersion: 1,
			NodeID:            nodeID,
			State:             apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
		},
		Config: virtualDeployment(deploymentID, nodeID, spaceID),
		Status: apigen.ScheduledInstanceStatus{Runner: apigen.RunnerStatus{
			DeploymentConfigVersion: 1,
			Status:                  apigen.RunningStatus_RUNNING,
		}},
	}
}
