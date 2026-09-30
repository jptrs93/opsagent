package webuihandler

import (
	"context"
	"slices"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
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

type entityKey struct {
	t  apigen.CoreEntityType
	id int64
}

// streamVisibility is one connection's view of the event stream. It keeps
// the parent identities and spaces it has seen, so filtering an observed
// status or a delete never depends on a lookup racing the write, and the set
// of entities it has forwarded, so a delete reaches the browser exactly when
// the browser holds the entity.
type streamVisibility struct {
	h                        *Handler
	ctx                      apigen.Context
	deployments              map[int32]int32
	instances                map[int32]int32
	nodes                    map[int32][]int32
	secrets, configs, assets map[int32]int32
	policies                 map[int32]*apigen.NetworkPolicy
	policyVisibility         map[int32]bool
	grants                   map[int64]int64
	sent                     map[entityKey]bool
}

func newStreamVisibility(h *Handler, ctx apigen.Context) *streamVisibility {
	v := &streamVisibility{h: h, ctx: ctx}
	v.reset()
	return v
}

func (v *streamVisibility) reset() {
	v.deployments = map[int32]int32{}
	v.instances = map[int32]int32{}
	v.nodes = map[int32][]int32{}
	v.secrets = map[int32]int32{}
	v.configs = map[int32]int32{}
	v.assets = map[int32]int32{}
	v.policies = map[int32]*apigen.NetworkPolicy{}
	v.policyVisibility = map[int32]bool{}
	v.grants = map[int64]int64{}
	v.sent = map[entityKey]bool{}
}

func (v *streamVisibility) userID() int64 {
	if v.ctx.User == nil {
		return 0
	}
	return int64(v.ctx.User.ID)
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

// needsReset reports whether the commit can change what this viewer may see:
// authorization changes, a space move, a node allowed-space change, or a
// network policy whose visibility flips. The stream answers with the compacted
// history rather than forwarding the commit.
func (v *streamVisibility) needsReset(u *state.WriteUpdate) bool {
	for _, m := range u.Mutations {
		e := m.Entity()
		id := m.EntityID()
		switch m.Type() {
		case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE, apigen.CoreEntityType_CORE_ENTITY_SPACE:
			return true
		case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT:
			if e != nil && e.AuthzGrant != nil {
				if e.AuthzGrant.UserID == v.userID() {
					return true
				}
				continue
			}
			if user, ok := v.grants[id]; !ok || user == v.userID() {
				return true
			}
		case apigen.CoreEntityType_CORE_ENTITY_NODE:
			if e != nil && e.Node != nil {
				if old, ok := v.nodes[int32(id)]; ok && !slices.Equal(old, e.Node.Operator.AllowedSpaces) {
					return true
				}
			}
		case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:
			if e != nil && e.Deployment != nil {
				if old, ok := v.deployments[int32(id)]; ok && old != e.Deployment.SpaceID {
					return true
				}
			}
		case apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:
			if e != nil && e.NetworkPolicy != nil {
				if old, ok := v.policyVisibility[int32(id)]; ok && old != v.h.networkPolicyVisible(v.ctx, e.NetworkPolicy) {
					return true
				}
			}
		case apigen.CoreEntityType_CORE_ENTITY_SECRET:
			if e != nil && e.Secret != nil {
				if old, ok := v.secrets[int32(id)]; ok && old != e.Secret.SpaceID {
					return true
				}
			}
		case apigen.CoreEntityType_CORE_ENTITY_CONFIG:
			if e != nil && e.Config != nil {
				if old, ok := v.configs[int32(id)]; ok && old != e.Config.SpaceID {
					return true
				}
			}
		case apigen.CoreEntityType_CORE_ENTITY_ASSET:
			if e != nil && e.Asset != nil {
				if old, ok := v.assets[int32(id)]; ok && old != e.Asset.SpaceID {
					return true
				}
			}
		}
	}
	// A policy scoped to a deployment follows that deployment's space.
	if u.Has(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT) {
		for id, policy := range v.policies {
			if v.policyVisibility[id] != v.h.networkPolicyVisible(v.ctx, policy) {
				return true
			}
		}
	}
	return false
}

// observe records the identities a mutation carries whether or not the
// viewer may see it, so later statuses and deletes resolve without a lookup.
func (v *streamVisibility) observe(m *apigen.CoreMutation) {
	e := m.Entity()
	id := m.EntityID()
	if e == nil {
		return
	}
	switch m.Type() {
	case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:
		if e.Deployment != nil {
			v.deployments[int32(id)] = e.Deployment.SpaceID
		}
	case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:
		if e.ScheduledInstance != nil {
			v.instances[int32(id)] = e.ScheduledInstance.DeploymentID
		}
	case apigen.CoreEntityType_CORE_ENTITY_NODE:
		if e.Node != nil {
			v.nodes[int32(id)] = e.Node.Operator.AllowedSpaces
		}
	case apigen.CoreEntityType_CORE_ENTITY_SECRET:
		if e.Secret != nil {
			v.secrets[int32(id)] = e.Secret.SpaceID
		}
	case apigen.CoreEntityType_CORE_ENTITY_CONFIG:
		if e.Config != nil {
			v.configs[int32(id)] = e.Config.SpaceID
		}
	case apigen.CoreEntityType_CORE_ENTITY_ASSET:
		if e.Asset != nil {
			v.assets[int32(id)] = e.Asset.SpaceID
		}
	case apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:
		if e.NetworkPolicy != nil {
			v.policies[int32(id)] = e.NetworkPolicy
			v.policyVisibility[int32(id)] = v.h.networkPolicyVisible(v.ctx, e.NetworkPolicy)
		}
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT:
		if e.AuthzGrant != nil {
			v.grants[id] = e.AuthzGrant.UserID
		}
	}
}

func (v *streamVisibility) forget(m *apigen.CoreMutation) {
	id := m.EntityID()
	switch m.Type() {
	case apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:
		delete(v.policies, int32(id))
		delete(v.policyVisibility, int32(id))
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT:
		delete(v.grants, id)
	}
}

// entityVisible decides whether the viewer may see an entity in the state
// the payload describes. For observed statuses the payload is not needed;
// the parent identity decides.
func (v *streamVisibility) entityVisible(t apigen.CoreEntityType, id int64, e *apigen.CoreEntity) bool {
	h, ctx := v.h, v.ctx
	if e == nil {
		e = &apigen.CoreEntity{}
	}
	switch t {
	case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:
		return e.Deployment != nil && h.canAccess(ctx, vView, eDeployment, int64(e.Deployment.SpaceID), id)
	case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:
		return e.ScheduledInstance != nil && v.deploymentVisible(e.ScheduledInstance.DeploymentID)
	case apigen.CoreEntityType_CORE_ENTITY_NODE:
		return e.Node != nil && h.nodeVisible(ctx, id, e.Node.Operator.AllowedSpaces)
	case apigen.CoreEntityType_CORE_ENTITY_SECRET:
		return e.Secret != nil && h.canAccess(ctx, vView, eSecret, int64(e.Secret.SpaceID), id)
	case apigen.CoreEntityType_CORE_ENTITY_CONFIG:
		return e.Config != nil && h.canAccess(ctx, vView, eConfig, int64(e.Config.SpaceID), id)
	case apigen.CoreEntityType_CORE_ENTITY_ASSET:
		return e.Asset != nil && h.canAccess(ctx, vView, eAsset, int64(e.Asset.SpaceID), id)
	case apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:
		return e.NetworkPolicy != nil && h.networkPolicyVisible(ctx, e.NetworkPolicy)
	case apigen.CoreEntityType_CORE_ENTITY_SPACE:
		return h.spaceVisible(ctx, id)
	case apigen.CoreEntityType_CORE_ENTITY_USER:
		return id == v.userID() || h.canAccess(ctx, vView, eUser, 0, id)
	case apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY:
		return e.ValueDirectory != nil && h.canAccessAny(ctx, vView, eValues, int64(e.ValueDirectory.SpaceID), 0)
	case apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY:
		return e.AssetDirectory != nil && h.canAccess(ctx, vView, eAsset, int64(e.AssetDirectory.SpaceID), 0)
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE:
		return true
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT:
		return e.AuthzGrant != nil && (e.AuthzGrant.UserID == v.userID() || h.canAccess(ctx, vView, eAccess, 0, 0))
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE:
		return h.canAccess(ctx, vView, eAccess, 0, 0)
	case apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG, apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET:
		return h.canAccess(ctx, vView, eCluster, 0, 0)
	case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS:
		return v.instanceVisible(int32(id))
	case apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS:
		return h.nodeVisible(ctx, id, v.nodes[int32(id)])
	// Sessions are owner-only: they reach the browser that holds them and no one else.
	case apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION:
		return e.AgentSession != nil && int64(e.AgentSession.UserID) == v.userID()
	case apigen.CoreEntityType_CORE_ENTITY_USER_SESSION:
		return e.UserSession != nil && int64(e.UserSession.UserID) == v.userID()
	}
	return false
}

// visible decides one mutation and updates the forwarded set. The current
// payload decides a create or update: the mutation's own in a live commit,
// the entity's newest in an opening. A delete is forwarded when this
// connection forwarded the entity; when it never did (a replay after a
// reconnect), the entity's final row decides.
func (v *streamVisibility) visible(m *apigen.CoreMutation, current *apigen.CoreEntity) bool {
	key := entityKey{m.Type(), m.EntityID()}
	if m.Delete != nil {
		defer v.forget(m)
		if v.sent[key] {
			delete(v.sent, key)
			return true
		}
		last, err := v.h.Queries.LatestMutation(context.Background(), key.t, key.id)
		return err == nil && v.entityVisible(key.t, key.id, &last.Entity)
	}
	ok := v.entityVisible(key.t, key.id, current)
	if ok {
		v.sent[key] = true
	} else {
		delete(v.sent, key)
	}
	return ok
}

// visibleUpdate returns a live commit with only the mutations the viewer may
// see and with server-only fields cleared, or nil when nothing remains.
func (v *streamVisibility) visibleUpdate(u *state.WriteUpdate) *state.WriteUpdate {
	if u == nil {
		return nil
	}
	for _, m := range u.Mutations {
		v.observe(m)
	}
	return v.filterUpdate(u, (*apigen.CoreMutation).Entity)
}

func (v *streamVisibility) filterUpdate(u *state.WriteUpdate, current func(*apigen.CoreMutation) *apigen.CoreEntity) *state.WriteUpdate {
	out := &state.WriteUpdate{Seq: u.Seq, Time: u.Time, Actor: u.Actor}
	for _, m := range u.Mutations {
		if !v.visible(m, current(m)) {
			continue
		}
		out.Mutations = append(out.Mutations, browserMutation(m))
	}
	if len(out.Mutations) == 0 {
		return nil
	}
	return out
}

// visibleEvents filters an opening. An entity's rows stand or fall together:
// its newest payload in the batch decides, so a value moved out of the
// viewer's space ships none of its history and one moved in ships all of
// it. Every identity is observed first, so a child never resolves against a
// parent row the batch has not reached yet.
func (v *streamVisibility) visibleEvents(events []*apigen.CoreWriteUpdate) []*apigen.CoreWriteUpdate {
	current := map[entityKey]*apigen.CoreEntity{}
	for _, e := range events {
		for _, m := range e.Mutations {
			v.observe(m)
			if m.Delete == nil {
				current[entityKey{m.Type(), m.EntityID()}] = m.Entity()
			}
		}
	}
	newest := func(m *apigen.CoreMutation) *apigen.CoreEntity { return current[entityKey{m.Type(), m.EntityID()}] }
	out := make([]*apigen.CoreWriteUpdate, 0, len(events))
	for _, e := range events {
		if visible := v.filterUpdate(e, newest); visible != nil {
			out = append(out, visible)
		}
	}
	return out
}

func (h *Handler) visibleUpdate(ctx apigen.Context, u *state.WriteUpdate) *state.WriteUpdate {
	return newStreamVisibility(h, ctx).visibleUpdate(u)
}

// browserMutation copies a mutation with the fields a browser never receives
// cleared: sealed secret bytes, credential blobs, token hashes, and the
// master password hash. Keyslots never reach a browser at all, but the
// visibility switch already drops them.
func browserMutation(m *apigen.CoreMutation) *apigen.CoreMutation {
	switch {
	case m.Create != nil:
		c := *m.Create
		c.Entity = browserEntity(c.Entity)
		return &apigen.CoreMutation{Create: &c}
	case m.Update != nil:
		u := *m.Update
		u.Entity = browserEntity(u.Entity)
		return &apigen.CoreMutation{Update: &u}
	}
	return m
}

func browserEntity(e *apigen.CoreEntity) *apigen.CoreEntity {
	if e == nil {
		return nil
	}
	out := *e
	if out.Secret != nil {
		s := *out.Secret
		s.SmkVersion, s.Ciphertext, s.Nonce = 0, nil, nil
		out.Secret = &s
	}
	if out.User != nil {
		u := *out.User
		u.Credentials = nil
		out.User = &u
	}
	if out.AgentSession != nil {
		s := *out.AgentSession
		s.TokenHash = nil
		out.AgentSession = &s
	}
	if out.UserSession != nil {
		s := *out.UserSession
		s.TokenHash = nil
		out.UserSession = &s
	}
	if out.SystemConfig != nil {
		c := *out.SystemConfig
		c.MasterPasswordHash = ""
		out.SystemConfig = &c
	}
	out.SecretKeyslot = nil
	return &out
}
