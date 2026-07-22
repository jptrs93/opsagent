package secondary

import (
	"fmt"
	"net/netip"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

// reconcileClusterNetMap applies only remote paths. Local workload routes are
// installed by the container lifecycle and take precedence if map state lags.
func reconcileClusterNetMap(clusterMap *apigen.ClusterNetMap, nodeID int32, prefix network.Prefix) error {
	topology, err := topologyFromClusterNetMap(clusterMap, nodeID, prefix)
	if err != nil {
		return err
	}
	return network.Default.ReconcileTopology(topology)
}

func topologyFromClusterNetMap(clusterMap *apigen.ClusterNetMap, nodeID int32, prefix network.Prefix) (network.Topology, error) {
	if clusterMap == nil || nodeID <= 0 || prefix.IsZero() {
		return network.Topology{}, fmt.Errorf("network map topology is missing map, prefix, or local node")
	}
	underlays := make(map[int32]netip.Addr, len(clusterMap.Nodes))
	for _, node := range clusterMap.Nodes {
		if node == nil || node.NodeID <= 0 {
			return network.Topology{}, fmt.Errorf("network map topology has invalid node")
		}
		if node.UnderlayAddress == "" {
			continue
		}
		addr, err := netip.ParseAddr(node.UnderlayAddress)
		if err != nil || addr.Zone() != "" {
			return network.Topology{}, fmt.Errorf("network map topology has invalid underlay for node %d", node.NodeID)
		}
		underlays[node.NodeID] = addr.Unmap()
	}

	topology := network.Topology{Prefix: prefix, LocalNodeID: nodeID}
	remoteHosts := make(map[int32]struct{})
	for _, route := range clusterMap.Routes {
		if route == nil || route.HostingNodeID == nodeID {
			continue
		}
		addr, err := netip.ParseAddr(route.LogicalAddress)
		if err != nil {
			return network.Topology{}, fmt.Errorf("parsing logical route address %q: %w", route.LogicalAddress, err)
		}
		topology.Routes = append(topology.Routes, network.RemoteRoute{Addr: addr, NodeID: route.HostingNodeID})
		remoteHosts[route.HostingNodeID] = struct{}{}
	}
	if len(remoteHosts) == 0 {
		return topology, nil
	}
	local, ok := underlays[nodeID]
	if !ok {
		return network.Topology{}, fmt.Errorf("local node %d has no underlay address", nodeID)
	}
	for remoteNodeID := range remoteHosts {
		remote, ok := underlays[remoteNodeID]
		if !ok {
			return network.Topology{}, fmt.Errorf("remote node %d has no underlay address", remoteNodeID)
		}
		if local.BitLen() != remote.BitLen() {
			return network.Topology{}, fmt.Errorf("remote node %d underlay family differs from local node", remoteNodeID)
		}
		topology.Tunnels = append(topology.Tunnels, network.Tunnel{NodeID: remoteNodeID, Local: local, Remote: remote})
	}
	return topology, nil
}
