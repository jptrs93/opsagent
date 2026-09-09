package webuihandler

import (
	"context"
	"slices"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func filterVisible[T any](items []T, keep func(T) bool) []T {
	out := make([]T, 0, len(items))
	for _, item := range items {
		if keep(item) {
			out = append(out, item)
		}
	}
	return out
}

func (h *Handler) filterSpaces(ctx apigen.Context, items []*apigen.Space) []*apigen.Space {
	return filterVisible(items, func(s *apigen.Space) bool {
		return h.spaceVisible(ctx, int64(s.ID))
	})
}

func (h *Handler) filterSecrets(ctx apigen.Context, items []*apigen.SecretEvent) []*apigen.SecretEvent {
	return filterVisible(items, func(m *apigen.SecretEvent) bool {
		return h.canAccess(ctx, vView, eSecret, int64(m.SpaceID()), int64(m.SecretID))
	})
}

func (h *Handler) filterConfigs(ctx apigen.Context, items []*apigen.ConfigEvent) []*apigen.ConfigEvent {
	return filterVisible(items, func(m *apigen.ConfigEvent) bool {
		return h.canAccess(ctx, vView, eConfig, int64(m.SpaceID()), int64(m.ConfigID))
	})
}

func (h *Handler) filterAssets(ctx apigen.Context, items []*apigen.AssetEvent) []*apigen.AssetEvent {
	return filterVisible(items, func(a *apigen.AssetEvent) bool {
		return h.canAccess(ctx, vView, eAsset, int64(a.SpaceID()), int64(a.AssetID))
	})
}

func (h *Handler) filterValueDirectories(ctx apigen.Context, items []*apigen.ValueDirectory) []*apigen.ValueDirectory {
	return filterVisible(items, func(d *apigen.ValueDirectory) bool {
		return h.canAccessAny(ctx, vView, eValues, int64(d.SpaceID), 0)
	})
}

func (h *Handler) filterAssetDirectories(ctx apigen.Context, items []*apigen.AssetDirectory) []*apigen.AssetDirectory {
	return filterVisible(items, func(d *apigen.AssetDirectory) bool {
		return h.canAccess(ctx, vView, eAsset, int64(d.SpaceID), 0)
	})
}

func (h *Handler) filterDeployments(ctx apigen.Context, items []*apigen.DeploymentEvent) []*apigen.DeploymentEvent {
	return filterVisible(items, func(cfg *apigen.DeploymentEvent) bool {
		return h.canAccess(ctx, vView, eDeployment, int64(cfg.Value.SpaceID), int64(cfg.DeploymentID))
	})
}

func (h *Handler) filterNodes(ctx apigen.Context, items []*apigen.NodeEvent) []*apigen.NodeEvent {
	return filterVisible(items, func(n *apigen.NodeEvent) bool {
		return h.nodeVisible(ctx, int64(n.NodeID), n.Value.Operator.AllowedSpaces)
	})
}

func (h *Handler) filterNodeStatuses(ctx apigen.Context, items []*apigen.NodeStatus) []*apigen.NodeStatus {
	allowed := h.nodeAllowedSpaces()
	return filterVisible(items, func(n *apigen.NodeStatus) bool {
		return h.nodeVisible(ctx, int64(n.NodeID), allowed[n.NodeID])
	})
}

func (h *Handler) filterUsers(ctx apigen.Context, items []*apigen.User) []*apigen.User {
	return filterVisible(items, func(u *apigen.User) bool {
		if ctx.User != nil && u.ID == ctx.User.ID {
			return true
		}
		return h.canAccess(ctx, vView, eUser, 0, int64(u.ID))
	})
}

// filterIngressDiagnostics keeps the diagnostics of deployments the caller may
// view. The result is never nil so the client can treat it as a snapshot.
func (h *Handler) filterIngressDiagnostics(ctx apigen.Context, list *apigen.IngressDiagnosticList) *apigen.IngressDiagnosticList {
	out := &apigen.IngressDiagnosticList{Items: []*apigen.IngressDiagnostic{}}
	if list == nil {
		return out
	}
	for _, item := range list.Items {
		if item == nil {
			continue
		}
		cfg := h.findConfigByID(item.DeploymentID)
		if cfg == nil || !h.canAccess(ctx, vView, eDeployment, int64(cfg.Value.SpaceID), int64(cfg.DeploymentID)) {
			continue
		}
		out.Items = append(out.Items, item)
	}
	return out
}

// streamVisibility keeps parent identities from the event tree, so observed
// status filtering never depends on the arrival order of separate channels.
type streamVisibility struct {
	h                        *Handler
	ctx                      apigen.Context
	deployments              map[int32]int32
	instances                map[int32]int32
	nodes                    map[int32][]int32
	secrets, configs, assets map[int32]int32
	policies                 map[int32]*apigen.NetworkPolicy
	policyVisibility         map[int32]bool
}

func newStreamVisibility(h *Handler, ctx apigen.Context) *streamVisibility {
	return &streamVisibility{h: h, ctx: ctx, deployments: map[int32]int32{}, instances: map[int32]int32{}, nodes: map[int32][]int32{}, secrets: map[int32]int32{}, configs: map[int32]int32{}, assets: map[int32]int32{}, policies: map[int32]*apigen.NetworkPolicy{}, policyVisibility: map[int32]bool{}}
}

func (v *streamVisibility) deploymentVisible(id int32) bool {
	space, ok := v.deployments[id]
	if !ok {
		cfg := v.h.deploymentByID(id)
		if cfg == nil {
			return false
		}
		space = cfg.Value.SpaceID
		v.deployments[id] = space
	}
	return v.h.canAccess(v.ctx, vView, eDeployment, int64(space), int64(id))
}
func (v *streamVisibility) instanceVisible(id int32) bool {
	deployment, ok := v.instances[id]
	if !ok {
		event, err := v.h.Queries.GetScheduledInstance(context.Background(), id)
		if err != nil {
			return false
		}
		deployment = event.Value.DeploymentID
		v.instances[id] = deployment
	}
	return v.deploymentVisible(deployment)
}

func (v *streamVisibility) needsReset(update apigen.CoreUpdate) bool {
	u := &update
	if u.AuthzRuleTemplates != nil || u.AuthzGlobalRules != nil || len(u.Spaces) > 0 {
		return true
	}
	for _, event := range u.AuthzGrantEvents {
		if v.ctx.User != nil && event.Value.UserID == int64(v.ctx.User.ID) {
			return true
		}
	}
	for _, node := range u.NodeEvents {
		if old, ok := v.nodes[node.NodeID]; ok && !slices.Equal(old, node.Value.Operator.AllowedSpaces) {
			return true
		}
	}
	for _, event := range u.DeploymentEvents {
		if old, ok := v.deployments[event.DeploymentID]; ok && old != event.Value.SpaceID {
			return true
		}
	}
	// Policies can change scope directly or through a referenced deployment.
	// A hidden policy needs a snapshot reset to remove its previous client value.
	for _, event := range u.NetworkPolicyEvents {
		if old, ok := v.policyVisibility[event.NetworkPolicyID]; ok && old != v.h.networkPolicyVisible(v.ctx, &event.Value) {
			return true
		}
	}
	if len(u.DeploymentEvents) > 0 {
		for id, policy := range v.policies {
			if v.policyVisibility[id] != v.h.networkPolicyVisible(v.ctx, policy) {
				return true
			}
		}
	}
	// Space moves can reveal or hide the full history of a value.
	for _, e := range u.SecretEvents {
		if old, ok := v.secrets[e.SecretID]; ok && old != e.Value.SpaceID {
			return true
		}
	}
	for _, e := range u.ConfigEvents {
		if old, ok := v.configs[e.ConfigID]; ok && old != e.Value.SpaceID {
			return true
		}
	}
	for _, e := range u.AssetEvents {
		if old, ok := v.assets[e.AssetID]; ok && old != e.Value.SpaceID {
			return true
		}
	}
	return false
}

func (v *streamVisibility) visibleUpdate(u *apigen.CoreUpdate) *apigen.CoreUpdate {
	if u == nil {
		return nil
	}
	h, ctx := v.h, v.ctx
	out := *u
	for _, e := range u.DeploymentEvents {
		v.deployments[e.DeploymentID] = e.Value.SpaceID
	}
	for _, e := range u.ScheduledInstanceEvents {
		v.instances[e.ScheduledInstanceID] = e.Value.DeploymentID
	}
	for _, e := range u.NodeEvents {
		v.nodes[e.NodeID] = e.Value.Operator.AllowedSpaces
	}
	out.DeploymentEvents = filterVisible(u.DeploymentEvents, func(e *apigen.DeploymentEvent) bool { return v.deploymentVisible(e.DeploymentID) })
	out.ScheduledInstanceEvents = filterVisible(u.ScheduledInstanceEvents, func(e *apigen.ScheduledInstanceEvent) bool { return v.deploymentVisible(e.Value.DeploymentID) })
	out.NodeEvents = h.filterNodes(ctx, u.NodeEvents)
	for _, e := range u.SecretEvents {
		v.secrets[e.SecretID] = e.Value.SpaceID
	}
	out.SecretEvents = filterVisible(u.SecretEvents, func(e *apigen.SecretEvent) bool {
		return h.canAccess(ctx, vView, eSecret, int64(v.secrets[e.SecretID]), int64(e.SecretID))
	})
	for _, e := range u.ConfigEvents {
		v.configs[e.ConfigID] = e.Value.SpaceID
	}
	out.ConfigEvents = filterVisible(u.ConfigEvents, func(e *apigen.ConfigEvent) bool {
		return h.canAccess(ctx, vView, eConfig, int64(v.configs[e.ConfigID]), int64(e.ConfigID))
	})
	for _, e := range u.AssetEvents {
		v.assets[e.AssetID] = e.Value.SpaceID
	}
	out.AssetEvents = filterVisible(u.AssetEvents, func(e *apigen.AssetEvent) bool {
		return h.canAccess(ctx, vView, eAsset, int64(v.assets[e.AssetID]), int64(e.AssetID))
	})
	out.Spaces = h.filterSpaces(ctx, u.Spaces)
	out.ValueDirectories = h.filterValueDirectories(ctx, u.ValueDirectories)
	out.AssetDirectories = h.filterAssetDirectories(ctx, u.AssetDirectories)
	out.Users = h.filterUsers(ctx, u.Users)
	out.NetworkPolicyEvents = filterVisible(u.NetworkPolicyEvents, func(e *apigen.NetworkPolicyEvent) bool {
		visible := h.networkPolicyVisible(ctx, &e.Value)
		v.policies[e.NetworkPolicyID] = &e.Value
		v.policyVisibility[e.NetworkPolicyID] = visible
		if e.EventType == apigen.EventType_EVENT_TYPE_DELETE {
			delete(v.policies, e.NetworkPolicyID)
			delete(v.policyVisibility, e.NetworkPolicyID)
		}
		return visible
	})
	out.InstanceStatuses = filterVisible(u.InstanceStatuses, func(s *apigen.ScheduledInstanceStatus) bool { return v.instanceVisible(s.ScheduledInstanceID) })
	out.NodeStatuses = filterVisible(u.NodeStatuses, func(s *apigen.NodeStatus) bool { return h.nodeVisible(ctx, int64(s.NodeID), v.nodes[s.NodeID]) })
	if !h.canAccess(ctx, vView, eCluster, 0, 0) {
		out.SystemConfig = nil
	}
	if !h.canAccess(ctx, vView, eAccess, 0, 0) {
		out.AuthzGrantEvents = filterVisible(out.AuthzGrantEvents, func(e *apigen.AuthzGrantEvent) bool { return ctx.User != nil && e.Value.UserID == int64(ctx.User.ID) })
	}
	if out.AuthzGlobalRules != nil && !h.canAccess(ctx, vView, eAccess, 0, 0) {
		out.AuthzGlobalRules = nil
	}
	if out.IsEmpty() {
		return nil
	}
	return &out
}

func (h *Handler) visibleUpdate(ctx apigen.Context, u *apigen.CoreUpdate) *apigen.CoreUpdate {
	return newStreamVisibility(h, ctx).visibleUpdate(u)
}

func (h *Handler) visibleSnapshot(ctx apigen.Context, snapshot *apigen.Snapshot) *apigen.Snapshot {
	return newStreamVisibility(h, ctx).visibleSnapshot(snapshot)
}

func (v *streamVisibility) visibleSnapshot(snapshot *apigen.Snapshot) *apigen.Snapshot {
	v.deployments = map[int32]int32{}
	v.instances = map[int32]int32{}
	v.nodes = map[int32][]int32{}
	v.secrets = map[int32]int32{}
	v.configs = map[int32]int32{}
	v.assets = map[int32]int32{}
	v.policies = map[int32]*apigen.NetworkPolicy{}
	v.policyVisibility = map[int32]bool{}
	u := snapshotUpdate(snapshot)
	filtered := v.visibleUpdate(u)
	if filtered == nil {
		filtered = &apigen.CoreUpdate{Seq: snapshot.Seq}
	}
	out := updateSnapshot(filtered)
	out.SecretsStatus = snapshot.SecretsStatus
	out.BackupStatus = snapshot.BackupStatus
	if !v.h.canAccess(v.ctx, vView, eCluster, 0, 0) {
		out.BackupStatus = &apigen.BackupStatus{}
	}
	out.AgentSessions = filterVisible(snapshot.AgentSessions, func(s *apigen.AgentSession) bool { return v.ctx.User != nil && s.UserID == v.ctx.User.ID })
	out.IngressDiagnostics = v.h.filterIngressDiagnostics(v.ctx, snapshot.IngressDiagnostics)
	return out
}

func snapshotUpdate(s *apigen.Snapshot) *apigen.CoreUpdate {
	return &apigen.CoreUpdate{Seq: s.Seq,
		DeploymentEvents:        s.DeploymentEvents,
		ScheduledInstanceEvents: s.ScheduledInstanceEvents,
		NodeEvents:              s.NodeEvents,
		SecretEvents:            s.SecretEvents,
		ConfigEvents:            s.ConfigEvents,
		AssetEvents:             s.AssetEvents,
		ValueDirectories:        s.ValueDirectories,
		AssetDirectories:        s.AssetDirectories,
		Spaces:                  s.Spaces,
		NetworkPolicyEvents:     s.NetworkPolicyEvents,
		Users:                   s.Users,
		SystemConfig:            s.SystemConfig,
		AuthzRuleTemplates:      &apigen.AuthzRuleTemplateList{Items: s.AuthzRuleTemplates},
		AuthzGrantEvents:        s.AuthzGrantEvents,
		AuthzGlobalRules:        &apigen.AuthzGlobalRuleList{Items: s.AuthzGlobalRules},
		InstanceStatuses:        s.InstanceStatuses,
		NodeStatuses:            s.NodeStatuses,
	}
}

func updateSnapshot(u *apigen.CoreUpdate) *apigen.Snapshot {
	s := &apigen.Snapshot{Seq: u.Seq,
		DeploymentEvents:        u.DeploymentEvents,
		ScheduledInstanceEvents: u.ScheduledInstanceEvents,
		NodeEvents:              u.NodeEvents,
		SecretEvents:            u.SecretEvents,
		ConfigEvents:            u.ConfigEvents,
		AssetEvents:             u.AssetEvents,
		ValueDirectories:        u.ValueDirectories,
		AssetDirectories:        u.AssetDirectories,
		Spaces:                  u.Spaces,
		NetworkPolicyEvents:     u.NetworkPolicyEvents,
		Users:                   u.Users,
		SystemConfig:            u.SystemConfig,
		InstanceStatuses:        u.InstanceStatuses,
		NodeStatuses:            u.NodeStatuses,
	}
	if u.AuthzRuleTemplates != nil {
		s.AuthzRuleTemplates = u.AuthzRuleTemplates.Items
	}
	s.AuthzGrantEvents = u.AuthzGrantEvents
	if u.AuthzGlobalRules != nil {
		s.AuthzGlobalRules = u.AuthzGlobalRules.Items
	}
	return s
}
