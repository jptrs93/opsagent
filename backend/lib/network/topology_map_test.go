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
