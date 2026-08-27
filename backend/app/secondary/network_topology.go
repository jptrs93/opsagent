package secondary

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

// reconcileClusterNetMap applies only remote paths. Local workload routes are
// installed by the container lifecycle and take precedence if map state lags.
func reconcileClusterNetMap(clusterMap *apigen.ClusterNetMap, nodeID int32, prefix network.Prefix) error {
	topology, err := network.TopologyFromClusterNetMap(clusterMap, nodeID, prefix)
	if err != nil {
		return err
	}
	return network.Default.ReconcileTopology(topology)
}
