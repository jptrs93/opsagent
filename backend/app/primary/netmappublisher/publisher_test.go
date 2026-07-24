package netmappublisher

import (
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
	"github.com/jptrs93/opsagent/backend/util/version"
)

func TestPublisherPersistsAndCoalescesLatestMap(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	prefix := network.GeneratePrefix()
	store := sqlite.NewPrimaryStorage(dbPath)
	node := store.EnsurePrimaryNode("primary", "primary-id")
	store.MustSetNodeAddresses(node.ID, []string{"192.0.2.10"})
	store.EnsureNetproxyDeployment(node.ID, version.Version)

	publisher, err := New(store, prefix)
	if err != nil {
		t.Fatal(err)
	}
	initial := publisher.SnapshotForNode(node.ID)
	if initial == nil || initial.Generation == "" || initial.Sequence != 1 || initial.TargetNodeID != node.ID {
		t.Fatalf("unexpected initial map: %+v", initial)
	}
	if len(initial.Nodes) != 1 || initial.Nodes[0].UnderlayAddress != "192.0.2.10" {
		t.Fatalf("initial nodes = %+v", initial.Nodes)
	}
	if len(initial.Routes) != 0 {
		t.Fatalf("initial routes = %+v, want none before the netproxy runner starts", initial.Routes)
	}
	persistedBytes, ok := store.FetchLocalKV(sqlite.LocalKVPrimaryClusterNetMap)
	if !ok {
		t.Fatal("published map was not persisted")
	}
	persisted, err := apigen.DecodeClusterNetMap(persistedBytes)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.TargetNodeID != 0 {
		t.Fatalf("persisted target = %d, want neutral target", persisted.TargetNodeID)
	}
	if err := publisher.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := publisher.SnapshotForNode(node.ID).Sequence; got != initial.Sequence {
		t.Fatalf("unchanged refresh sequence = %d, want %d", got, initial.Sequence)
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
	if latest.Sequence != initial.Sequence+2 || latest.Nodes[0].UnderlayAddress != "192.0.2.12" {
		t.Fatalf("coalesced map = %+v", latest)
	}

	wantGeneration, wantSequence := latest.Generation, latest.Sequence
	publisher.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = sqlite.NewPrimaryStorage(dbPath)
	defer store.Close()
	restarted, err := New(store, prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	got := restarted.SnapshotForNode(node.ID)
	if got.Generation != wantGeneration || got.Sequence != wantSequence {
		t.Fatalf("restarted publication = %s/%d, want %s/%d", got.Generation, got.Sequence, wantGeneration, wantSequence)
	}
}

func TestRenderIsDeterministicAndUsesObservedRoutes(t *testing.T) {
	prefix := network.GeneratePrefix()
	nodesA := []*sqlite.Node{
		{ID: 2, Addresses: []string{"2001:db8::2"}},
		{ID: 1, Addresses: []string{"2001:db8::1"}},
	}
	deploymentsA := []apigen.DeploymentWithStatus{
		runningVirtualDeployment(t, prefix, 20, 2, 4, 1, 0),
		runningVirtualDeployment(t, prefix, 10, 1, 3, 1, 0),
	}
	nodesB := []*sqlite.Node{nodesA[1], nodesA[0]}
	deploymentsB := []apigen.DeploymentWithStatus{deploymentsA[1], deploymentsA[0]}
	a, err := render(prefix, nodesA, deploymentsA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := render(prefix, nodesB, deploymentsB)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonicalContent(a)) != string(canonicalContent(b)) {
		t.Fatalf("render depends on input order:\n%+v\n%+v", a, b)
	}
	want, err := prefix.InboundAddr(3, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, route := range a.Routes {
		found = found || route.LogicalAddress == want.String()
	}
	if len(a.Routes) != 4 || !found {
		t.Fatalf("routes do not contain derived address %s", want)
	}
}

func TestRenderOmitsDeploymentsWithoutObservedVirtualRunner(t *testing.T) {
	prefix := network.GeneratePrefix()
	nodes := []*sqlite.Node{{ID: 1, Addresses: []string{"192.0.2.1"}}}
	virtual := runningVirtualDeployment(t, prefix, 10, 1, 3, 1, 0)
	host := virtualDeployment(11, 1, 3)
	host.Spec.Networking.Mode = apigen.NetworkingMode_NETWORKING_MODE_HOST
	stopped := virtualDeployment(12, 1, 3)
	stopped.Spec.Container1Spec.Running = false
	deleted := virtualDeployment(13, 1, 3)
	deleted.Deleted = true
	got, err := render(prefix, nodes, []apigen.DeploymentWithStatus{
		{Config: host}, {Config: stopped}, {Config: deleted}, virtual,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Routes) != 2 || got.Routes[0].HostingNodeID != 1 || got.Routes[1].HostingNodeID != 1 {
		t.Fatalf("routes = %+v, want only running virtual deployment", got.Routes)
	}
}

func TestRenderIncludesRunningOutboundRoute(t *testing.T) {
	prefix := network.GeneratePrefix()
	config := virtualDeployment(10, 1, 3)
	status := apigen.DeploymentStatus{Runner: apigen.RunnerStatus{
		DeploymentConfigVersion: 7,
		NumberOfRestarts:        2,
		Status:                  apigen.RunningStatus_RUNNING,
		Endpoints:               []*apigen.Endpoint{{Ordinal: 0, Address: mustInboundAddr(t, prefix, 3, 10).String(), NodeID: 1, State: apigen.EndpointState_ENDPOINT_READY}},
	}}
	got, err := render(prefix, []*sqlite.Node{{ID: 1, Addresses: []string{"192.0.2.1"}}}, []apigen.DeploymentWithStatus{{Config: config, Status: status}})
	if err != nil {
		t.Fatal(err)
	}
	inbound, err := prefix.InboundAddr(3, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	outbound, err := prefix.OutboundAddr(3, 10, 0, 7, 3)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, route := range got.Routes {
		found[route.LogicalAddress] = true
	}
	if len(got.Routes) != 2 || !found[inbound.String()] || !found[outbound.String()] {
		t.Fatalf("routes = %+v, want inbound %s and outbound %s", got.Routes, inbound, outbound)
	}
}

func TestRenderUsesRunnerIdentityWhileDesiredConfigIsAhead(t *testing.T) {
	prefix := network.GeneratePrefix()
	item := runningVirtualDeployment(t, prefix, 10, 1, 3, 7, 2)
	item.Config.NodeID = 2
	item.Config.Identity.SpaceID = 4
	item.Config.Spec.Networking.Mode = apigen.NetworkingMode_NETWORKING_MODE_HOST
	got, err := render(prefix, []*sqlite.Node{
		{ID: 1, Addresses: []string{"192.0.2.1"}},
		{ID: 2, Addresses: []string{"192.0.2.2"}},
	}, []apigen.DeploymentWithStatus{item})
	if err != nil {
		t.Fatal(err)
	}
	wantInbound := mustInboundAddr(t, prefix, 3, 10)
	wantOutbound, err := prefix.OutboundAddr(3, 10, 0, 7, 3)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]int32{}
	for _, route := range got.Routes {
		found[route.LogicalAddress] = route.HostingNodeID
	}
	if len(got.Routes) != 2 || found[wantInbound.String()] != 1 || found[wantOutbound.String()] != 1 {
		t.Fatalf("routes = %+v, want observed runner routes on node 1", got.Routes)
	}
}

func TestRenderOmitsOutboundRouteWhenRunnerIsNotRunning(t *testing.T) {
	prefix := network.GeneratePrefix()
	config := virtualDeployment(10, 1, 3)
	status := apigen.DeploymentStatus{Runner: apigen.RunnerStatus{
		DeploymentConfigVersion: 7,
		NumberOfRestarts:        2,
		Status:                  apigen.RunningStatus_CRASHED,
	}}
	got, err := render(prefix, []*sqlite.Node{{ID: 1, Addresses: []string{"192.0.2.1"}}}, []apigen.DeploymentWithStatus{{Config: config, Status: status}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Routes) != 0 {
		t.Fatalf("routes = %+v, want none for crashed runner", got.Routes)
	}
}

func TestPublishLatestDoesNotBlockIfSubscriberDrains(t *testing.T) {
	ch := make(chan *apigen.ClusterNetMap, 1)
	ch <- &apigen.ClusterNetMap{Sequence: 1}
	drained := make(chan struct{})
	go func() {
		<-ch
		close(drained)
	}()
	<-drained
	done := make(chan struct{})
	go func() {
		publishLatest(ch, &apigen.ClusterNetMap{Sequence: 2})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publishLatest blocked after concurrent drain")
	}
}

func virtualDeployment(id, nodeID, spaceID int32) apigen.DeploymentConfig {
	return apigen.DeploymentConfig{
		ID:       id,
		NodeID:   nodeID,
		Identity: apigen.DeploymentIdentity{SpaceID: spaceID},
		Spec: apigen.DeploymentSpec{
			Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL},
			Container1Spec: &apigen.ContainerSpec{
				Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "example/app"}},
				Running: true,
			},
		},
	}
}

func runningVirtualDeployment(t *testing.T, prefix network.Prefix, id, nodeID, spaceID, configVersion, restarts int32) apigen.DeploymentWithStatus {
	t.Helper()
	return apigen.DeploymentWithStatus{
		Config: virtualDeployment(id, nodeID, spaceID),
		Status: apigen.DeploymentStatus{Runner: apigen.RunnerStatus{
			DeploymentConfigVersion: configVersion,
			NumberOfRestarts:        restarts,
			Status:                  apigen.RunningStatus_RUNNING,
			Endpoints: []*apigen.Endpoint{{
				Ordinal: 0,
				Address: mustInboundAddr(t, prefix, spaceID, id).String(),
				NodeID:  nodeID,
				State:   apigen.EndpointState_ENDPOINT_READY,
			}},
		}},
	}
}

func mustInboundAddr(t *testing.T, prefix network.Prefix, spaceID, deploymentID int32) netip.Addr {
	t.Helper()
	addr, err := prefix.InboundAddr(spaceID, deploymentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return addr
}
