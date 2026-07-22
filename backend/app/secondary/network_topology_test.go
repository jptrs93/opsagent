package secondary

import (
	"net/netip"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

func TestTopologyFromClusterNetMapUsesOnlyRemoteRouteHosts(t *testing.T) {
	prefix := network.GeneratePrefix()
	localAddr, err := prefix.InstanceAddr(1, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	remoteAddr, err := prefix.InstanceAddr(1, 11, 0)
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
			{LogicalAddress: localAddr.String(), HostingNodeID: 1},
			{LogicalAddress: remoteAddr.String(), HostingNodeID: 2},
		},
	}

	topology, err := topologyFromClusterNetMap(clusterMap, 1, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.Tunnels) != 1 || topology.Tunnels[0].NodeID != 2 {
		t.Fatalf("tunnels = %+v, want only node 2", topology.Tunnels)
	}
	if topology.Tunnels[0].Local != netip.MustParseAddr("192.0.2.1") || topology.Tunnels[0].Remote != netip.MustParseAddr("192.0.2.2") {
		t.Fatalf("tunnel endpoints = %+v", topology.Tunnels[0])
	}
	if len(topology.Routes) != 1 || topology.Routes[0].Addr != remoteAddr || topology.Routes[0].NodeID != 2 {
		t.Fatalf("routes = %+v, want remote route for node 2", topology.Routes)
	}
}

func TestTopologyFromClusterNetMapRejectsMissingRouteHostUnderlay(t *testing.T) {
	prefix := network.GeneratePrefix()
	addr, err := prefix.InstanceAddr(1, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = topologyFromClusterNetMap(&apigen.ClusterNetMap{
		Nodes: []*apigen.ClusterNetMapNode{
			{NodeID: 1, UnderlayAddress: "192.0.2.1"},
			{NodeID: 2},
		},
		Routes: []*apigen.ClusterNetMapRoute{{LogicalAddress: addr.String(), HostingNodeID: 2}},
	}, 1, prefix)
	if err == nil {
		t.Fatal("missing remote underlay was accepted")
	}
}
