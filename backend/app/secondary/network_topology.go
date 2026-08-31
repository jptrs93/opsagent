package secondary

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

func reconcileClusterNetMap(clusterMap *apigen.ClusterNetMap, nodeID int32, prefix network.Prefix) error {
	return network.ApplyClusterNetMap(clusterMap, nodeID, prefix, network.Default.ReconcileTopology, network.Default.SetPolicyRules)
}
