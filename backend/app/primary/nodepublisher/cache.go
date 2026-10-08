package nodepublisher

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

type versionKey struct {
	id      uint64
	version uint32
}

type versionEntry struct {
	record *apigen.DeploymentRecord
	pins   int
}

type cache struct {
	nodes      map[uint64]*apigen.Node
	instances  map[uint64]*apigen.ScheduledInstance
	versions   map[versionKey]*versionEntry
	current    map[uint64]*apigen.DeploymentRecord
	policies   map[uint64]*apigen.NetworkPolicy
	watermarks map[uint64]time.Time
}

func newCache() *cache {
	return &cache{
		nodes:      make(map[uint64]*apigen.Node),
		instances:  make(map[uint64]*apigen.ScheduledInstance),
		versions:   make(map[versionKey]*versionEntry),
		current:    make(map[uint64]*apigen.DeploymentRecord),
		policies:   make(map[uint64]*apigen.NetworkPolicy),
		watermarks: make(map[uint64]time.Time),
	}
}

func isMember(status apigen.NodeLifecycleStatus) bool {
	return slices.Contains(pq.MemberNodeStatuses, int64(status))
}

func seedCache(ctx context.Context, q *pq.Queries) (*cache, int64, error) {
	c := newCache()
	seq, err := q.GetGlobalSeq(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.ListNodeRows(ctx, pq.MemberNodeStatuses)
	if err != nil {
		return nil, 0, err
	}
	for i := range rows {
		node := rows[i].Event.Value
		node.ID = rows[i].Event.NodeID
		c.nodes[node.ID] = &node
	}
	deployments, err := q.ListActiveDeployments(ctx)
	if err != nil {
		return nil, 0, err
	}
	for _, record := range deployments {
		c.current[record.Deployment.ID] = record
		c.versions[versionKey{record.Deployment.ID, record.Meta.Version}] = &versionEntry{record: record}
	}
	states, err := q.ListLiveScheduledInstanceStates(ctx)
	if err != nil {
		return nil, 0, err
	}
	for i := range states {
		st := &states[i]
		inst := st.Instance
		c.instances[inst.ID] = &inst
		key := versionKey{inst.Deployment.DeploymentID, inst.Deployment.Version}
		entry := c.versions[key]
		if entry == nil {
			record := st.Config
			entry = &versionEntry{record: &record}
			c.versions[key] = entry
		}
		entry.pins++
		if st.Status.Present {
			c.watermarks[inst.ID] = st.Status.Value.UpdatedAt.Value
		}
	}
	policies, err := q.ListNetworkPolicies(ctx)
	if err != nil {
		return nil, 0, err
	}
	for _, event := range policies {
		policy := event.Value
		policy.ID = event.NetworkPolicyID
		c.policies[policy.ID] = &policy
	}
	return c, seq, nil
}

func (c *cache) pin(key versionKey) {
	c.versions[key].pins++
}

func (c *cache) unpin(key versionKey) {
	entry := c.versions[key]
	entry.pins--
	if entry.pins <= 0 && c.current[key.id] != entry.record {
		delete(c.versions, key)
	}
}

func (c *cache) state(inst *apigen.ScheduledInstance) apigen.ScheduledInstanceState {
	return apigen.ScheduledInstanceState{Instance: *inst, Config: *c.versions[versionKey{inst.Deployment.DeploymentID, inst.Deployment.Version}].record}
}

func (c *cache) applyDeployment(m *apigen.CoreMutation) (prev, next *apigen.DeploymentRecord) {
	id := m.EntityID()
	prev = c.current[id]
	if m.Kind() == apigen.AuthzVerb_AUTHZ_VERB_DELETE {
		delete(c.current, id)
		for key, entry := range c.versions {
			if key.id == id && entry.pins <= 0 {
				delete(c.versions, key)
			}
		}
		return prev, nil
	}
	meta := m.Meta()
	value := *m.Entity().Value.Deployment
	value.ID = id
	next = &apigen.DeploymentRecord{Deployment: value, Meta: *meta}
	c.versions[versionKey{id, meta.Version}] = &versionEntry{record: next}
	c.current[id] = next
	if prev != nil && prev.Meta.Version != meta.Version {
		if entry := c.versions[versionKey{id, prev.Meta.Version}]; entry != nil && entry.pins <= 0 {
			delete(c.versions, versionKey{id, prev.Meta.Version})
		}
	}
	return prev, next
}

func (c *cache) applyInstance(m *apigen.CoreMutation) (row *apigen.ScheduledInstanceState, prev *apigen.ScheduledInstance) {
	id := m.EntityID()
	prev = c.instances[id]
	inst := *m.Entity().Value.ScheduledInstance
	inst.ID = id
	final := inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED
	state := c.state(&inst)
	key := versionKey{inst.Deployment.DeploymentID, inst.Deployment.Version}
	switch {
	case final:
		delete(c.instances, id)
		delete(c.watermarks, id)
		c.unpin(versionKey{prev.Deployment.DeploymentID, prev.Deployment.Version})
	case prev == nil:
		c.pin(key)
		c.instances[id] = &inst
	default:
		if prev.Deployment.DeploymentID != inst.Deployment.DeploymentID || prev.Deployment.Version != inst.Deployment.Version {
			c.unpin(versionKey{prev.Deployment.DeploymentID, prev.Deployment.Version})
			c.pin(key)
		}
		c.instances[id] = &inst
	}
	return &state, prev
}

func (c *cache) applyNode(m *apigen.CoreMutation) (prev, next *apigen.Node) {
	id := m.EntityID()
	prev = c.nodes[id]
	if m.Kind() == apigen.AuthzVerb_AUTHZ_VERB_DELETE {
		delete(c.nodes, id)
		return prev, nil
	}
	value := *m.Entity().Value.Node
	value.ID = id
	if !isMember(value.Status) {
		delete(c.nodes, id)
		return prev, nil
	}
	c.nodes[id] = &value
	return prev, &value
}

func (c *cache) applyPolicy(m *apigen.CoreMutation) (prev, next *apigen.NetworkPolicy) {
	id := m.EntityID()
	prev = c.policies[id]
	if m.Kind() == apigen.AuthzVerb_AUTHZ_VERB_DELETE {
		delete(c.policies, id)
		return prev, nil
	}
	value := *m.Entity().Value.NetworkPolicy
	value.ID = id
	c.policies[id] = &value
	return prev, &value
}

func (c *cache) applyStatus(m *apigen.CoreMutation) {
	id := m.EntityID()
	if m.Kind() == apigen.AuthzVerb_AUTHZ_VERB_DELETE {
		delete(c.watermarks, id)
		return
	}
	if _, live := c.instances[id]; !live {
		return
	}
	c.watermarks[id] = m.Entity().Value.ScheduledInstanceStatus.UpdatedAt.Value
}

func (c *cache) inputs() renderInputs {
	in := renderInputs{deploymentSpaces: make(map[uint64]uint64, len(c.current))}
	for _, node := range c.nodes {
		in.nodes = append(in.nodes, node)
	}
	slices.SortFunc(in.nodes, func(a, b *apigen.Node) int { return cmp.Compare(a.ID, b.ID) })
	for _, inst := range c.instances {
		in.instances = append(in.instances, c.state(inst))
	}
	slices.SortFunc(in.instances, func(a, b apigen.ScheduledInstanceState) int { return cmp.Compare(a.Instance.ID, b.Instance.ID) })
	for id, record := range c.current {
		in.deployments = append(in.deployments, record)
		in.deploymentSpaces[id] = record.Deployment.SpaceID
	}
	slices.SortFunc(in.deployments, func(a, b *apigen.DeploymentRecord) int { return cmp.Compare(a.Deployment.ID, b.Deployment.ID) })
	for _, policy := range c.policies {
		in.policies = append(in.policies, policy)
	}
	slices.SortFunc(in.policies, func(a, b *apigen.NetworkPolicy) int { return cmp.Compare(a.ID, b.ID) })
	return in
}

func (c *cache) nodeInstance(row *apigen.ScheduledInstanceState) apigen.NodeInstance {
	out := apigen.NodeInstance{Instance: row.Instance, Config: row.Config}
	if at, ok := c.watermarks[row.Instance.ID]; ok {
		out.StatusWatermark = apigen.Some(at)
	}
	return out
}

func (c *cache) instancesOn(nodeID uint64) []apigen.NodeInstance {
	var out []apigen.NodeInstance
	for _, inst := range c.instances {
		if inst.NodeID != nodeID {
			continue
		}
		row := c.state(inst)
		out = append(out, c.nodeInstance(&row))
	}
	slices.SortFunc(out, func(a, b apigen.NodeInstance) int { return cmp.Compare(a.Instance.ID, b.Instance.ID) })
	return out
}
