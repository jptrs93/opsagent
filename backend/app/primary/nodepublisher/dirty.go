package nodepublisher

import (
	"reflect"
	"slices"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/ingressplan"
)

// dirtySet is the over-approximation of the nodes whose map a commit can
// change. The byte compare after the render decides what is published, so a
// rule that is too wide costs render time and never correctness.
type dirtySet struct {
	all   bool
	nodes map[uint64]struct{}
}

func newDirtySet() *dirtySet { return &dirtySet{nodes: make(map[uint64]struct{})} }

func (d *dirtySet) add(ids ...uint64) {
	for _, id := range ids {
		d.nodes[id] = struct{}{}
	}
}

func (d *dirtySet) has(id uint64) bool {
	if d.all {
		return true
	}
	_, ok := d.nodes[id]
	return ok
}

func (rc *renderContext) hostsInto(space uint64, into map[uint64]struct{}) {
	for _, index := range rc.bySpace[space] {
		into[rc.placements[index].nodeID] = struct{}{}
	}
}

func (rc *renderContext) hostsOfInto(deploymentIDs []uint64, into map[uint64]struct{}) {
	for _, deploymentID := range deploymentIDs {
		for _, index := range rc.byDeployment[deploymentID] {
			into[rc.placements[index].nodeID] = struct{}{}
		}
	}
}

func (rc *renderContext) publishersOfInto(deploymentID uint64, into map[uint64]struct{}) {
	for publisher, deployments := range rc.published {
		if slices.Contains(deployments, deploymentID) {
			into[publisher] = struct{}{}
		}
	}
}

func (rc *renderContext) nodesReachingInto(space uint64, into map[uint64]struct{}) {
	rc.hostsInto(space, into)
	for to := range rc.out[space] {
		rc.hostsInto(to, into)
	}
	for from := range rc.in[space] {
		rc.hostsInto(from, into)
	}
	rc.hostsInto(globalSpaceID, into)
	if space == globalSpaceID {
		for s := range rc.bySpace {
			if s != systemSpaceID {
				rc.hostsInto(s, into)
			}
		}
	}
}

func (rc *renderContext) nodesSeeingInto(nodeID uint64, into map[uint64]struct{}) {
	into[nodeID] = struct{}{}
	for _, index := range rc.byNode[nodeID] {
		p := &rc.placements[index]
		if p.spaceID == systemSpaceID {
			continue
		}
		rc.nodesReachingInto(p.spaceID, into)
		rc.publishersOfInto(p.deploymentID, into)
	}
	rc.hostsOfInto(rc.published[nodeID], into)
}

func (p *Publisher) dirtyInstance(d *dirtySet, prev *apigen.ScheduledInstance, row *apigen.ScheduledInstanceState) {
	inst := &row.Instance
	d.add(inst.NodeID)
	if inst.SpaceID == systemSpaceID {
		p.rc.hostsOfInto(p.rc.published[inst.NodeID], d.nodes)
		return
	}
	p.rc.nodesReachingInto(inst.SpaceID, d.nodes)
	p.rc.publishersOfInto(inst.Deployment.DeploymentID, d.nodes)
}

func (p *Publisher) policyReferences(deploymentID uint64) bool {
	for _, policy := range p.cache.policies {
		for _, peer := range []apigen.NetworkPolicyPeer{policy.Source, policy.Destination} {
			if target := peer.Target.Value.Deployment; target != nil && target.DeploymentID == deploymentID {
				return true
			}
		}
	}
	return false
}

func ingressInput(record *apigen.DeploymentRecord) ingressplan.Deployment {
	return ingressplan.DeploymentFromSpec(record.Deployment.ID, record.Deployment.PlacementNodeID(), record.Deployment.Name, &record.Deployment.Spec)
}

func (p *Publisher) dirtyDeployment(d *dirtySet, prev, next *apigen.DeploymentRecord, planDirty *bool) {
	id := uint64(0)
	if next != nil {
		id = next.Deployment.ID
	} else if prev != nil {
		id = prev.Deployment.ID
	}
	switch {
	case prev == nil || next == nil:
		*planDirty = true
		if p.policyReferences(id) {
			d.all = true
		}
	case prev.Deployment.SpaceID != next.Deployment.SpaceID:
		*planDirty = true
		if p.policyReferences(id) {
			d.all = true
		}
	default:
		if !reflect.DeepEqual(ingressInput(prev), ingressInput(next)) {
			*planDirty = true
		}
	}
	if d.all {
		return
	}
	if next != nil && (prev == nil || prev.Deployment.Name != next.Deployment.Name || prev.Deployment.SpaceID != next.Deployment.SpaceID) {
		p.rc.nodesReachingInto(next.Deployment.SpaceID, d.nodes)
		p.rc.publishersOfInto(id, d.nodes)
	}
	if prev != nil && next == nil {
		p.rc.nodesReachingInto(prev.Deployment.SpaceID, d.nodes)
		p.rc.publishersOfInto(id, d.nodes)
	}
}

func (p *Publisher) dirtyNode(d *dirtySet, prev, next *apigen.Node, planDirty *bool) {
	if prev == nil || next == nil || prev.Reported.WgPublicKey != next.Reported.WgPublicKey || prev.Reported.UnderlayAddress.Addr() != next.Reported.UnderlayAddress.Addr() {
		d.all = true
		*planDirty = true
		return
	}
	if !slices.Equal(ingressplan.HostAddresses(prev.Reported.HostAddresses), ingressplan.HostAddresses(next.Reported.HostAddresses)) || prev.Reported.HostAddressesUnknown != next.Reported.HostAddressesUnknown {
		*planDirty = true
	}
}

func (p *Publisher) policySpaces(policy *apigen.NetworkPolicy) ([]uint64, bool) {
	var spaces []uint64
	for _, peer := range []apigen.NetworkPolicyPeer{policy.Source, policy.Destination} {
		target := peer.Target.Value
		if target.Space != nil {
			spaces = append(spaces, target.Space.SpaceID)
			continue
		}
		record := p.cache.current[target.Deployment.DeploymentID]
		if record == nil {
			return nil, false
		}
		spaces = append(spaces, record.Deployment.SpaceID)
	}
	return spaces, true
}

func (p *Publisher) dirtyPolicy(d *dirtySet, prev, next *apigen.NetworkPolicy) {
	for _, policy := range []*apigen.NetworkPolicy{prev, next} {
		if policy == nil {
			continue
		}
		spaces, ok := p.policySpaces(policy)
		if !ok {
			continue
		}
		for _, space := range spaces {
			p.rc.nodesReachingInto(space, d.nodes)
		}
	}
}

func samePublish(a, b []apigen.IngressPublish) bool {
	return slices.EqualFunc(a, b, func(x, y apigen.IngressPublish) bool { return x.Address == y.Address && x.Port == y.Port })
}

func (p *Publisher) dirtyPlan(d *dirtySet, prev, next ingressPlan) {
	for nodeID := range prev.publish {
		if !samePublish(prev.publish[nodeID], next.publish[nodeID]) {
			p.rc.nodesSeeingInto(nodeID, d.nodes)
		}
	}
	for nodeID := range next.publish {
		if _, ok := prev.publish[nodeID]; !ok && len(next.publish[nodeID]) > 0 {
			p.rc.nodesSeeingInto(nodeID, d.nodes)
		}
	}
	for _, publish := range []map[uint64][]uint64{prev.published, next.published} {
		for nodeID := range publish {
			before, after := prev.published[nodeID], next.published[nodeID]
			if slices.Equal(before, after) {
				continue
			}
			d.add(nodeID)
			p.rc.hostsOfInto(before, d.nodes)
			p.rc.hostsOfInto(after, d.nodes)
		}
	}
}
