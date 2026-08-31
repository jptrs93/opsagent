package network

import (
	"net/netip"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const (
	testWGKeyA = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="
	testWGKeyB = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI="
	testWGKeyC = "Q0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0M="
)

func TestTopologyFromClusterNetMapUsesOnlyRemoteRouteHosts(t *testing.T) {
	prefix := GeneratePrefix()
	localPrefix, err := prefix.InstanceCIDR(1, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	remotePrefix, err := prefix.InstanceCIDR(1, 11, 0)
	if err != nil {
		t.Fatal(err)
	}
	clusterMap := &apigen.ClusterNetMap{
		Nodes: []*apigen.ClusterNetMapNode{
			{NodeID: 1, UnderlayAddress: "192.0.2.1", WgPublicKey: testWGKeyA, WgListenPort: 51833},
			{NodeID: 2, UnderlayAddress: "192.0.2.2", WgPublicKey: testWGKeyB, WgListenPort: 51833},
			{NodeID: 3, UnderlayAddress: "192.0.2.3", WgPublicKey: testWGKeyC, WgListenPort: 51833},
		},
		Routes: []*apigen.ClusterNetMapRoute{
			{LogicalPrefix: localPrefix.String(), HostingNodeID: 1},
			{LogicalPrefix: remotePrefix.String(), HostingNodeID: 2},
		},
	}

	topology, err := TopologyFromClusterNetMap(clusterMap, 1, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.Peers) != 1 || topology.Peers[0].NodeID != 2 {
		t.Fatalf("peers = %+v, want only node 2", topology.Peers)
	}
	if topology.Peers[0].Endpoint != netip.MustParseAddr("192.0.2.2") {
		t.Fatalf("peer endpoint = %+v", topology.Peers[0])
	}
	if topology.Peers[0].WGKey != testWGKeyB || topology.Peers[0].WGPort != 51833 {
		t.Fatalf("peer transport = %+v, want key B on 51833", topology.Peers[0])
	}
	if topology.LocalWGPort != 51833 {
		t.Fatalf("local wg port = %d, want 51833", topology.LocalWGPort)
	}
	if len(topology.Routes) != 1 || topology.Routes[0].Prefix != remotePrefix || topology.Routes[0].NodeID != 2 {
		t.Fatalf("routes = %+v, want remote route for node 2", topology.Routes)
	}
}

func TestTopologyFromClusterNetMapRejectsMissingRouteHostUnderlay(t *testing.T) {
	prefix := GeneratePrefix()
	destination, err := prefix.InstanceCIDR(1, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = TopologyFromClusterNetMap(&apigen.ClusterNetMap{
		Nodes: []*apigen.ClusterNetMapNode{
			{NodeID: 1, UnderlayAddress: "192.0.2.1", WgPublicKey: testWGKeyA, WgListenPort: 51833},
			{NodeID: 2, WgPublicKey: testWGKeyB, WgListenPort: 51833},
		},
		Routes: []*apigen.ClusterNetMapRoute{{LogicalPrefix: destination.String(), HostingNodeID: 2}},
	}, 1, prefix)
	if err == nil {
		t.Fatal("missing remote underlay was accepted")
	}
}

func TestTopologyFromClusterNetMapRequiresWireGuardTransport(t *testing.T) {
	prefix := GeneratePrefix()
	remotePrefix, err := prefix.InstanceCIDR(1, 11, 0)
	if err != nil {
		t.Fatal(err)
	}
	clusterMap := &apigen.ClusterNetMap{
		Nodes: []*apigen.ClusterNetMapNode{
			{NodeID: 1, UnderlayAddress: "192.0.2.1", WgPublicKey: testWGKeyA, WgListenPort: 51833},
			{NodeID: 2, UnderlayAddress: "192.0.2.2", WgPublicKey: testWGKeyB, WgListenPort: 51833},
		},
		Routes: []*apigen.ClusterNetMapRoute{
			{LogicalPrefix: remotePrefix.String(), HostingNodeID: 2},
		},
	}
	if _, err := TopologyFromClusterNetMap(clusterMap, 1, prefix); err != nil {
		t.Fatal(err)
	}

	// A keyless node anywhere in the map is a rendering bug: every member
	// node registers a key before it can be accepted.
	clusterMap.Nodes[0].WgPublicKey = ""
	if _, err := TopologyFromClusterNetMap(clusterMap, 1, prefix); err == nil {
		t.Fatal("keyless node was accepted")
	}
	clusterMap.Nodes[0].WgPublicKey = testWGKeyA

	// A keyed node with an invalid listen port must fail loudly rather than
	// blackhole quietly.
	clusterMap.Nodes[1].WgListenPort = 0
	if _, err := TopologyFromClusterNetMap(clusterMap, 1, prefix); err == nil {
		t.Fatal("node without listen port was accepted")
	}
	clusterMap.Nodes[1].WgListenPort = 51833

	// A map that does not contain the local node cannot yield a listen port.
	if _, err := TopologyFromClusterNetMap(clusterMap, 7, prefix); err == nil {
		t.Fatal("map without the local node was accepted")
	}
}
