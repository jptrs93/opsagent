package network

import (
	"net/netip"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
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
			{NodeID: 1, UnderlayAddress: "192.0.2.1"},
			{NodeID: 2, UnderlayAddress: "192.0.2.2"},
			{NodeID: 3, UnderlayAddress: "192.0.2.3"},
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
	if len(topology.Tunnels) != 1 || topology.Tunnels[0].NodeID != 2 {
		t.Fatalf("tunnels = %+v, want only node 2", topology.Tunnels)
	}
	if topology.Tunnels[0].Local != netip.MustParseAddr("192.0.2.1") || topology.Tunnels[0].Remote != netip.MustParseAddr("192.0.2.2") {
		t.Fatalf("tunnel endpoints = %+v", topology.Tunnels[0])
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
			{NodeID: 1, UnderlayAddress: "192.0.2.1"},
			{NodeID: 2},
		},
		Routes: []*apigen.ClusterNetMapRoute{{LogicalPrefix: destination.String(), HostingNodeID: 2}},
	}, 1, prefix)
	if err == nil {
		t.Fatal("missing remote underlay was accepted")
	}
}

func TestTopologyFromClusterNetMapCarriesWireGuardTransport(t *testing.T) {
	prefix := GeneratePrefix()
	remotePrefix, err := prefix.InstanceCIDR(1, 11, 0)
	if err != nil {
		t.Fatal(err)
	}
	const keyA = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="
	const keyB = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI="
	clusterMap := &apigen.ClusterNetMap{
		Nodes: []*apigen.ClusterNetMapNode{
			{NodeID: 1, UnderlayAddress: "192.0.2.1", WgPublicKey: keyA, WgListenPort: 51833},
			{NodeID: 2, UnderlayAddress: "192.0.2.2", WgPublicKey: keyB, WgListenPort: 51833},
			{NodeID: 3, UnderlayAddress: "192.0.2.3"},
		},
		Routes: []*apigen.ClusterNetMapRoute{
			{LogicalPrefix: remotePrefix.String(), HostingNodeID: 2},
		},
	}
	topology, err := TopologyFromClusterNetMap(clusterMap, 1, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if !topology.LocalWGCapable || topology.LocalWGPort != 51833 {
		t.Fatalf("local wg capability = %v port %d, want capable on 51833", topology.LocalWGCapable, topology.LocalWGPort)
	}
	if len(topology.Tunnels) != 1 || topology.Tunnels[0].RemoteWGKey != keyB || topology.Tunnels[0].RemoteWGPort != 51833 {
		t.Fatalf("tunnels = %+v, want node 2 with wg key", topology.Tunnels)
	}

	// A keyless local node derives no WireGuard capability regardless of what
	// remote entries carry.
	clusterMap.Nodes[0].WgPublicKey = ""
	clusterMap.Nodes[0].WgListenPort = 0
	topology, err = TopologyFromClusterNetMap(clusterMap, 1, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if topology.LocalWGCapable {
		t.Fatal("keyless local node reported wg-capable")
	}
	if topology.Tunnels[0].RemoteWGKey != keyB {
		t.Fatal("remote capability should still be carried for the reconciler")
	}

	// A keyed node with an invalid listen port is a rendering bug and must
	// fail loudly rather than blackhole quietly.
	clusterMap.Nodes[1].WgListenPort = 0
	if _, err := TopologyFromClusterNetMap(clusterMap, 1, prefix); err == nil {
		t.Fatal("keyed node without listen port was accepted")
	}
}
