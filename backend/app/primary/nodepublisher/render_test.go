package nodepublisher

import (
	"net/netip"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

const (
	testWGKeyA = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="
	testWGKeyB = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI="
)

func renderNI(prefix network.Prefix, nodeList []*apigen.Node, instances []apigen.ScheduledInstanceState) *apigen.ClusterNetMap {
	got, _ := render(prefix, renderInputs{nodes: nodeList, instances: instances})
	return got
}

func TestRenderDnsCatalog(t *testing.T) {
	prefix := network.GeneratePrefix()
	nodeList := []*apigen.Node{
		testNode(1, "192.0.2.1", testWGKeyA),
		testNode(2, "192.0.2.2", testWGKeyB),
	}

	serving := servingInstance(100, 10, 2, 3)
	serving.Config.Deployment.Name = "database"
	standbyOnly := servingInstance(101, 11, 1, 3)
	standbyOnly.Config.Deployment.Name = "webapp"
	standbyOnly.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY
	promotingOld := servingInstance(104, 14, 1, 3)
	promotingOld.Config.Deployment.Name = "promoting"
	promotingOld.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING
	promotingNew := servingInstance(105, 14, 1, 3)
	promotingNew.Config.Deployment.Name = "promoting"
	promotingNew.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY
	hostMode := servingInstance(102, 12, 1, 3)
	hostMode.Config.Deployment.Name = "hosty"
	hostMode.Config.Deployment.Spec.Networking.Mode = apigen.NetworkingMode_NETWORKING_MODE_HOST
	unnamed := servingInstance(103, 13, 1, 3)

	got := renderNI(prefix, nodeList, []apigen.ScheduledInstanceState{
		unnamed, hostMode, standbyOnly, serving, promotingOld, promotingNew,
	})
	if len(got.DnsServices) != 4 {
		t.Fatalf("dns services = %+v, want database, webapp, promoting, and the fallback-labelled deployment", got.DnsServices)
	}
	byName := map[string]*apigen.ClusterNetMapService{}
	for i := range got.DnsServices {
		svc := &got.DnsServices[i]
		byName[svc.Name] = svc
	}
	promoting := byName["promoting"]
	if promoting == nil || len(promoting.Ordinals) != 1 || promoting.Ordinals[0].Ordinal != 0 {
		t.Fatalf("promoting service = %+v, want the ordinal held established by its standby+draining pair", promoting)
	}
	if fallback := byName["deployment"]; fallback == nil || fallback.DeploymentID != 13 {
		t.Fatalf("fallback-labelled service = %+v, want the unnamed deployment", fallback)
	}
	database := byName["database"]
	if database == nil || database.SpaceID != 3 || database.DeploymentID != 10 {
		t.Fatalf("database service = %+v", database)
	}
	if len(database.Ordinals) != 1 || database.Ordinals[0].Ordinal != 0 {
		t.Fatalf("database ordinals = %+v, want ordinal 0", database.Ordinals)
	}
	if webapp := byName["webapp"]; webapp == nil || len(webapp.Ordinals) != 0 {
		t.Fatalf("webapp service = %+v, want no established ordinals", webapp)
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	prefix := network.GeneratePrefix()
	nodesA := []*apigen.Node{
		testNode(2, "2001:db8::2", testWGKeyB),
		testNode(1, "2001:db8::1", testWGKeyA),
	}
	instancesA := []apigen.ScheduledInstanceState{
		servingInstance(200, 20, 2, 4),
		servingInstance(100, 10, 1, 3),
	}
	nodesB := []*apigen.Node{nodesA[1], nodesA[0]}
	instancesB := []apigen.ScheduledInstanceState{instancesA[1], instancesA[0]}
	a := renderNI(prefix, nodesA, instancesA)
	b := renderNI(prefix, nodesB, instancesB)
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
	nodeList := []*apigen.Node{testNode(1, "192.0.2.1", testWGKeyA)}

	host := servingInstance(101, 11, 1, 3)
	host.Config.Deployment.Spec.Networking.Mode = apigen.NetworkingMode_NETWORKING_MODE_HOST
	terminating := servingInstance(102, 12, 1, 3)
	terminating.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE
	finalized := servingInstance(103, 13, 1, 3)
	finalized.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED

	got := renderNI(prefix, nodeList, []apigen.ScheduledInstanceState{
		host, terminating, finalized, servingInstance(100, 10, 1, 3),
	})
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
	nodeList := []*apigen.Node{testNode(1, "192.0.2.1", testWGKeyA)}
	quiet := servingInstance(100, 10, 1, 3)

	restarted := servingInstance(100, 10, 1, 3)
	restarted.Status.Value.Runner.Value.NumberOfRestarts = 12
	crashed := servingInstance(100, 10, 1, 3)
	crashed.Status.Value.Runner.Value.Status = apigen.RunningStatus_RUNNING_STATUS_CRASHED
	starting := servingInstance(100, 10, 1, 3)
	starting.Status.Value.Runner.Value.Status = apigen.RunningStatus_RUNNING_STATUS_STARTING
	noStatus := servingInstance(100, 10, 1, 3)
	noStatus.Status = apigen.Maybe[apigen.ScheduledInstanceStatus]{}

	base := renderNI(prefix, nodeList, []apigen.ScheduledInstanceState{quiet})
	for name, item := range map[string]apigen.ScheduledInstanceState{
		"restarted": restarted, "crashed": crashed, "starting": starting, "no status": noStatus,
	} {
		got := renderNI(prefix, nodeList, []apigen.ScheduledInstanceState{item})
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
	nodeList := []*apigen.Node{
		testNode(1, "192.0.2.1", testWGKeyA),
		testNode(2, "192.0.2.2", testWGKeyB),
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
	warming := renderNI(prefix, nodeList, []apigen.ScheduledInstanceState{old, replacement})
	assertRoutes(t, "warming up", warming, map[string]uint64{
		instancePrefix.String(): 1,
		oldPlacement.String():   1,
		newPlacement.String():   2,
	})

	// Promoted: the instance prefix follows the replacement, while the draining
	// placement keeps its own prefix pointed at the node still running it.
	old.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING
	replacement.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING
	promoted := renderNI(prefix, nodeList, []apigen.ScheduledInstanceState{old, replacement})
	assertRoutes(t, "promoted", promoted, map[string]uint64{
		instancePrefix.String(): 2,
		oldPlacement.String():   1,
		newPlacement.String():   2,
	})

	// Retired: nothing left of the old placement.
	old.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE
	retired := renderNI(prefix, nodeList, []apigen.ScheduledInstanceState{old, replacement})
	assertRoutes(t, "retired", retired, map[string]uint64{
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
	nodeList := []*apigen.Node{testNode(1, "192.0.2.1", testWGKeyA)}
	old := servingInstance(100, 10, 1, 3)
	replacement := servingInstance(101, 10, 1, 3)
	replacement.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY

	before := renderNI(prefix, nodeList, []apigen.ScheduledInstanceState{old, replacement})
	old.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING
	replacement.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING
	after := renderNI(prefix, nodeList, []apigen.ScheduledInstanceState{old, replacement})
	if string(canonicalContent(before)) != string(canonicalContent(after)) {
		t.Fatalf("same-node promotion changed the map:\nbefore %+v\nafter  %+v", before.Routes, after.Routes)
	}
}

func assertRoutes(t *testing.T, stage string, got *apigen.ClusterNetMap, want map[string]uint64) {
	t.Helper()
	actual := make(map[string]uint64, len(got.Routes))
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

func virtualDeployment(id, nodeID, spaceID uint64) apigen.DeploymentRecord {
	return apigen.DeploymentRecord{
		Deployment: apigen.Deployment{ID: id, Scheduling: apigen.DedicatedScheduling(true, nodeID), SpaceID: spaceID, Spec: apigen.DeploymentSpec{Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL}, Workload: containerWorkload()}},
		Meta:       apigen.EntityMeta{Version: 1, SpecVersion: 1},
	}
}

func containerWorkload() apigen.Workload {
	return apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
		Source:          apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{RemoteImage: &apigen.RemoteImage{Image: "example/app"}}},
		Runtime:         apigen.ContainerRuntime{User: "1000"},
		UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE,
	}}}
}

func mustAddr(s string) apigen.IpAddress {
	return apigen.AddrOf(netip.MustParseAddr(s))
}

func testNode(id uint64, underlay, wgKey string, hostAddresses ...string) *apigen.Node {
	node := &apigen.Node{ID: id, Status: apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_NORMAL, Reported: apigen.NodeReported{UnderlayAddress: mustAddr(underlay), WgPublicKey: wgKey}}
	for _, value := range hostAddresses {
		node.Reported.HostAddresses = append(node.Reported.HostAddresses, mustAddr(value))
	}
	return node
}

// servingInstance builds a running, serving placement. Status is populated with
// a healthy runner precisely so tests that vary it can show it makes no
// difference to what gets rendered.
func servingInstance(instanceID, deploymentID, nodeID, spaceID uint64) apigen.ScheduledInstanceState {
	return apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{
			ID:         instanceID,
			Deployment: apigen.DeploymentRef{DeploymentID: deploymentID, Version: 1},
			NodeID:     nodeID,
			SpaceID:    spaceID,
			State:      apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
		},
		Config: virtualDeployment(deploymentID, nodeID, spaceID),
		Status: apigen.Some(apigen.ScheduledInstanceStatus{ScheduledInstanceID: instanceID, UpdatedAt: apigen.TimeOf(time.Unix(1, 0)), Runner: apigen.Some(apigen.RunnerStatus{
			DeploymentSpecVersion: 1,
			Status:                apigen.RunningStatus_RUNNING_STATUS_RUNNING,
		})}),
	}
}

func TestRenderIngressPublishPerNode(t *testing.T) {
	prefix := network.GeneratePrefix()
	nodeList := []*apigen.Node{
		testNode(1, "192.0.2.10", testWGKeyA, "192.0.2.10", "2001:db8::10"),
		testNode(2, "192.0.2.20", testWGKeyB, "192.0.2.20"),
	}
	virtual := func(id, nodeID uint64, listen ...apigen.IngressListen) *apigen.DeploymentRecord {
		return &apigen.DeploymentRecord{Deployment: apigen.Deployment{ID: id, Scheduling: apigen.DedicatedScheduling(false, nodeID), SpaceID: 1, Name: "d", Spec: apigen.DeploymentSpec{Workload: containerWorkload(), Networking: apigen.NetworkingConfig{
			Mode:    apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
			Ingress: []apigen.Ingress{{Hostname: "app.example.test", Config: apigen.IngressConfig{Value: apigen.IngressConfigValueOneof{Https: &apigen.HttpsConfig{ContainerPort: 8080, PathPrefix: "/"}}}, Listen: listen}},
		}}}, Meta: apigen.EntityMeta{Version: 1, SpecVersion: 1}}
	}
	ipv6 := apigen.IngressListen{Addresses: []apigen.IpPrefix{apigen.PrefixOf(netip.MustParsePrefix("::/0"))}}
	inputs := renderInputs{nodes: nodeList, deployments: []*apigen.DeploymentRecord{virtual(10, 1, ipv6), virtual(11, 2)}}
	got, diagnostics := render(prefix, inputs)
	publish := func(nodeID uint64) []string {
		for _, node := range got.Nodes {
			if node.NodeID == nodeID {
				out := make([]string, 0, len(node.IngressPublish))
				for _, entry := range node.IngressPublish {
					out = append(out, entry.Address+":"+strconv.Itoa(int(entry.Port)))
				}
				return out
			}
		}
		return nil
	}
	if want := []string{"2001:db8::10:80", "2001:db8::10:443"}; !slices.Equal(publish(1), want) {
		t.Fatalf("node 1 publish = %v, want %v (ipv6 only)", publish(1), want)
	}
	if want := []string{"192.0.2.20:80", "192.0.2.20:443"}; !slices.Equal(publish(2), want) {
		t.Fatalf("node 2 publish = %v, want %v", publish(2), want)
	}
	if len(diagnostics.Items) != 0 {
		t.Fatalf("diagnostics = %+v, want none", diagnostics.Items)
	}
}
