package nodepublisher

import (
	"slices"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type nodeWatcher struct {
	nodeID uint64
	fn     func(*apigen.NodeProjection)
}

func (c *cache) liveDeploymentsOn(nodeID uint64) apigen.NodeDeployments {
	out := apigen.NodeDeployments{LiveIds: []uint64{}}
	for id, record := range c.current {
		if record.Deployment.PlacementNodeID() == nodeID {
			out.LiveIds = append(out.LiveIds, id)
		}
	}
	slices.Sort(out.LiveIds)
	return out
}

func placementChange(into map[uint64]struct{}, prev, next *apigen.DeploymentRecord) {
	var from, to uint64
	if prev != nil {
		from = prev.Deployment.PlacementNodeID()
	}
	if next != nil {
		to = next.Deployment.PlacementNodeID()
	}
	if from == to {
		return
	}
	into[from] = struct{}{}
	into[to] = struct{}{}
}

func (p *Publisher) liveDeploymentsLocked(nodeIDs map[uint64]struct{}) map[uint64]*apigen.NodeDeployments {
	out := make(map[uint64]*apigen.NodeDeployments, len(nodeIDs))
	for nodeID := range nodeIDs {
		if _, member := p.cache.nodes[nodeID]; !member {
			continue
		}
		live := p.cache.liveDeploymentsOn(nodeID)
		out[nodeID] = &live
	}
	return out
}

// WatchNode calls fn with the node's projection and then with every update
// for it. fn runs inside the publisher lock, so it must not block or call the
// publisher.
func (p *Publisher) WatchNode(nodeID uint64, fn func(*apigen.NodeProjection)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	snapshot, err := p.snapshotLocked(nodeID)
	if err != nil {
		return err
	}
	fn(&snapshot)
	p.watchers = append(p.watchers, nodeWatcher{nodeID: nodeID, fn: fn})
	return nil
}
