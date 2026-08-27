package network

import (
	"fmt"
	"net/netip"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TopologyFromClusterNetMap(clusterMap *apigen.ClusterNetMap, nodeID int32, prefix Prefix) (Topology, error) {
	if clusterMap == nil || nodeID <= 0 || prefix.IsZero() {
		return Topology{}, fmt.Errorf("network map topology is missing map, prefix, or local node")
	}
	underlays := make(map[int32]netip.Addr, len(clusterMap.Nodes))
	for _, node := range clusterMap.Nodes {
		if node == nil || node.NodeID <= 0 {
			return Topology{}, fmt.Errorf("network map topology has invalid node")
		}
		if node.UnderlayAddress == "" {
			continue
		}
		addr, err := netip.ParseAddr(node.UnderlayAddress)
		if err != nil || addr.Zone() != "" {
			return Topology{}, fmt.Errorf("network map topology has invalid underlay for node %d", node.NodeID)
		}
		underlays[node.NodeID] = addr.Unmap()
	}

	topology := Topology{Prefix: prefix, LocalNodeID: nodeID}
	remoteHosts := make(map[int32]struct{})
	for _, route := range clusterMap.Routes {
		if route == nil || route.HostingNodeID == nodeID {
			continue
		}
		destination, err := netip.ParsePrefix(route.LogicalPrefix)
		if err != nil {
			return Topology{}, fmt.Errorf("parsing logical route prefix %q: %w", route.LogicalPrefix, err)
		}
		topology.Routes = append(topology.Routes, RemoteRoute{Prefix: destination, NodeID: route.HostingNodeID})
		remoteHosts[route.HostingNodeID] = struct{}{}
	}
	if len(remoteHosts) == 0 {
		return topology, nil
	}
	local, ok := underlays[nodeID]
	if !ok {
		return Topology{}, fmt.Errorf("local node %d has no underlay address", nodeID)
	}
	for remoteNodeID := range remoteHosts {
		remote, ok := underlays[remoteNodeID]
		if !ok {
			return Topology{}, fmt.Errorf("remote node %d has no underlay address", remoteNodeID)
		}
		if local.BitLen() != remote.BitLen() {
			return Topology{}, fmt.Errorf("remote node %d underlay family differs from local node", remoteNodeID)
		}
		topology.Tunnels = append(topology.Tunnels, Tunnel{NodeID: remoteNodeID, Local: local, Remote: remote})
	}
	return topology, nil
}
