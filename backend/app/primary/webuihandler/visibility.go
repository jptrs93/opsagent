package webuihandler

import (
	"context"
	"fmt"
	"slices"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
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

func (h *Handler) filterValueDirectories(ctx apigen.Context, items []*apigen.ValueDirectory) []*apigen.ValueDirectory {
	return filterVisible(items, func(d *apigen.ValueDirectory) bool {
		return h.canAccessAny(ctx, vView, eValues, d.SpaceID, 0)
	})
}

func (h *Handler) filterAssetDirectories(ctx apigen.Context, items []*apigen.AssetDirectory) []*apigen.AssetDirectory {
	return filterVisible(items, func(d *apigen.AssetDirectory) bool {
		return h.canAccess(ctx, vView, eAsset, d.SpaceID, 0)
	})
}

// filterIngressDiagnostics keeps the diagnostics of deployments the caller may
// view. The result always carries a list so the client can treat it as a
// snapshot.
func (h *Handler) filterIngressDiagnostics(ctx apigen.Context, list *apigen.IngressDiagnosticList) apigen.Maybe[apigen.IngressDiagnosticList] {
	out := apigen.IngressDiagnosticList{Items: []apigen.IngressDiagnostic{}}
	if list == nil {
		return apigen.Some(out)
	}
	for _, item := range list.Items {
		cfg := h.findConfigByID(item.DeploymentID)
		if cfg == nil || !h.canAccess(ctx, vView, eDeployment, cfg.Deployment.SpaceID, cfg.Deployment.ID) {
			continue
		}
		out.Items = append(out.Items, item)
	}
	return apigen.Some(out)
}

type entityKey struct {
	t  apigen.CoreEntityType
	id uint64
}

// streamVisibility is one connection's view of the event stream. It keeps
// the parent identities and spaces it has seen, so filtering an observed
// status or a delete never depends on a lookup racing the write, and the set
// of entities it has forwarded, so a delete reaches the browser exactly when
// the browser holds the entity.
type streamVisibility struct {
	h                        *Handler
	ctx                      apigen.Context
	deployments              map[uint64]uint64
	instances                map[uint64]uint64
	nodes                    map[uint64][]uint64
	secrets, configs, assets map[uint64]uint64
	policies                 map[uint64]*apigen.NetworkPolicy
	policyVisibility         map[uint64]bool
	grants                   map[uint64]uint64
	sent                     map[entityKey]bool
}

func newStreamVisibility(h *Handler, ctx apigen.Context) *streamVisibility {
	v := &streamVisibility{h: h, ctx: ctx}
	v.reset()
	return v
}

func (v *streamVisibility) reset() {
	v.deployments = map[uint64]uint64{}
	v.instances = map[uint64]uint64{}
	v.nodes = map[uint64][]uint64{}
	v.secrets = map[uint64]uint64{}
	v.configs = map[uint64]uint64{}
	v.assets = map[uint64]uint64{}
	v.policies = map[uint64]*apigen.NetworkPolicy{}
	v.policyVisibility = map[uint64]bool{}
	v.grants = map[uint64]uint64{}
	v.sent = map[entityKey]bool{}
}

func (v *streamVisibility) userID() uint64 {
	if v.ctx.User == nil {
		return 0
	}
	return v.ctx.User.ID
}

func (v *streamVisibility) deploymentVisible(id uint64) bool {
	space, ok := v.deployments[id]
	if !ok {
		cfg := v.h.deploymentByID(id)
		if cfg == nil {
			return false
		}
		space = cfg.Deployment.SpaceID
		v.deployments[id] = space
	}
	return v.h.canAccess(v.ctx, vView, eDeployment, space, id)
}

func (v *streamVisibility) instanceVisible(id uint64) bool {
	deployment, ok := v.instances[id]
	if !ok {
		event, err := v.h.Queries.GetScheduledInstance(context.Background(), id)
		if err != nil {
			return false
		}
		deployment = event.Value.Deployment.DeploymentID
		v.instances[id] = deployment
	}
	return v.deploymentVisible(deployment)
}

// needsReset reports whether the commit can change what this viewer may see:
// authorization changes, a space move, a node allowed-space change, or a
// network policy whose visibility flips. The stream answers with the compacted
// history rather than forwarding the commit.
func (v *streamVisibility) needsReset(u *state.WriteUpdate) bool {
	for i := range u.Mutations {
		m := &u.Mutations[i]
		e := m.Entity()
		id := m.EntityID()
		switch m.Type() {
		case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT_TEMPLATE, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE, apigen.CoreEntityType_CORE_ENTITY_SPACE:
			return true
		case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT:
			if e != nil && e.Value.AuthzGrant != nil {
				if e.Value.AuthzGrant.UserID == v.userID() {
					return true
				}
				continue
			}
			if user, ok := v.grants[id]; !ok || user == v.userID() {
				return true
			}
		case apigen.CoreEntityType_CORE_ENTITY_NODE:
			if e != nil && e.Value.Node != nil {
				if old, ok := v.nodes[id]; ok && !slices.Equal(old, e.Value.Node.Operator.AllowedSpaces) {
					return true
				}
			}
		case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:
			if e != nil && e.Value.Deployment != nil {
				if old, ok := v.deployments[id]; ok && old != e.Value.Deployment.SpaceID {
					return true
				}
			}
		case apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:
			if e != nil && e.Value.NetworkPolicy != nil {
				if old, ok := v.policyVisibility[id]; ok && old != v.h.networkPolicyVisible(v.ctx, e.Value.NetworkPolicy) {
					return true
				}
			}
		case apigen.CoreEntityType_CORE_ENTITY_SECRET:
			if e != nil && e.Value.Secret != nil {
				if old, ok := v.secrets[id]; ok && old != e.Value.Secret.SpaceID {
					return true
				}
			}
		case apigen.CoreEntityType_CORE_ENTITY_CONFIG:
			if e != nil && e.Value.Config != nil {
				if old, ok := v.configs[id]; ok && old != e.Value.Config.SpaceID {
					return true
				}
			}
		case apigen.CoreEntityType_CORE_ENTITY_ASSET:
			if e != nil && e.Value.Asset != nil {
				if old, ok := v.assets[id]; ok && old != e.Value.Asset.SpaceID {
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

// observe records the identities a payload carries whether or not the
// viewer may see it, so later statuses and deletes resolve without a lookup.
func (v *streamVisibility) observe(t apigen.CoreEntityType, id uint64, e *apigen.CoreEntity) {
	if e == nil {
		return
	}
	switch t {
	case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:
		if e.Value.Deployment != nil {
			v.deployments[id] = e.Value.Deployment.SpaceID
		}
	case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:
		if e.Value.ScheduledInstance != nil {
			v.instances[id] = e.Value.ScheduledInstance.Deployment.DeploymentID
		}
	case apigen.CoreEntityType_CORE_ENTITY_NODE:
		if e.Value.Node != nil {
			v.nodes[id] = e.Value.Node.Operator.AllowedSpaces
		}
	case apigen.CoreEntityType_CORE_ENTITY_SECRET:
		if e.Value.Secret != nil {
			v.secrets[id] = e.Value.Secret.SpaceID
		}
	case apigen.CoreEntityType_CORE_ENTITY_CONFIG:
		if e.Value.Config != nil {
			v.configs[id] = e.Value.Config.SpaceID
		}
	case apigen.CoreEntityType_CORE_ENTITY_ASSET:
		if e.Value.Asset != nil {
			v.assets[id] = e.Value.Asset.SpaceID
		}
	case apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:
		if e.Value.NetworkPolicy != nil {
			v.policies[id] = e.Value.NetworkPolicy
			v.policyVisibility[id] = v.h.networkPolicyVisible(v.ctx, e.Value.NetworkPolicy)
		}
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT:
		if e.Value.AuthzGrant != nil {
			v.grants[id] = e.Value.AuthzGrant.UserID
		}
	}
}

func (v *streamVisibility) forget(m *apigen.CoreMutation) {
	id := m.EntityID()
	switch m.Type() {
	case apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:
		delete(v.policies, id)
		delete(v.policyVisibility, id)
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT:
		delete(v.grants, id)
	}
}

// entityVisible decides whether the viewer may see an entity in the state
// the payload describes. For observed statuses the payload is not needed;
// the parent identity decides.
func (v *streamVisibility) entityVisible(t apigen.CoreEntityType, id uint64, e *apigen.CoreEntity) bool {
	h, ctx := v.h, v.ctx
	if e == nil {
		e = &apigen.CoreEntity{}
	}
	p := &e.Value
	switch t {
	case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:
		return p.Deployment != nil && h.canAccess(ctx, vView, eDeployment, p.Deployment.SpaceID, id)
	case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:
		return p.ScheduledInstance != nil && v.deploymentVisible(p.ScheduledInstance.Deployment.DeploymentID)
	case apigen.CoreEntityType_CORE_ENTITY_NODE:
		return p.Node != nil && h.nodeVisible(ctx, id, p.Node.Operator.AllowedSpaces)
	case apigen.CoreEntityType_CORE_ENTITY_SECRET:
		return p.Secret != nil && h.canAccess(ctx, vView, eSecret, p.Secret.SpaceID, id)
	case apigen.CoreEntityType_CORE_ENTITY_CONFIG:
		return p.Config != nil && h.canAccess(ctx, vView, eConfig, p.Config.SpaceID, id)
	case apigen.CoreEntityType_CORE_ENTITY_ASSET:
		return p.Asset != nil && h.canAccess(ctx, vView, eAsset, p.Asset.SpaceID, id)
	case apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:
		return p.NetworkPolicy != nil && h.networkPolicyVisible(ctx, p.NetworkPolicy)
	case apigen.CoreEntityType_CORE_ENTITY_SPACE:
		return h.spaceVisible(ctx, id)
	case apigen.CoreEntityType_CORE_ENTITY_USER:
		return id == v.userID() || h.canAccess(ctx, vView, eUser, 0, id)
	case apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY:
		return p.ValueDirectory != nil && h.canAccessAny(ctx, vView, eValues, p.ValueDirectory.SpaceID, 0)
	case apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY:
		return p.AssetDirectory != nil && h.canAccess(ctx, vView, eAsset, p.AssetDirectory.SpaceID, 0)
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT_TEMPLATE:
		return true
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT:
		return p.AuthzGrant != nil && (p.AuthzGrant.UserID == v.userID() || h.canAccess(ctx, vView, eAccess, 0, 0))
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE:
		return h.canAccess(ctx, vView, eAccess, 0, 0)
	case apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG, apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET:
		return h.canAccess(ctx, vView, eCluster, 0, 0)
	case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS:
		return v.instanceVisible(id)
	case apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS:
		return h.nodeVisible(ctx, id, v.nodes[id])
	// Sessions are owner-only: they reach the browser that holds them and no one else.
	case apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION:
		return p.AgentSession != nil && p.AgentSession.UserID == v.userID()
	case apigen.CoreEntityType_CORE_ENTITY_USER_SESSION:
		return p.UserSession != nil && p.UserSession.UserID == v.userID()
	}
	return false
}

// visible decides one live mutation and updates the forwarded set. The
// mutation's own payload decides a create or update. A delete is forwarded
// exactly when this connection forwarded the entity, in the snapshot or in a
// later commit.
func (v *streamVisibility) visible(m *apigen.CoreMutation) bool {
	key := entityKey{m.Type(), m.EntityID()}
	if m.Value.Delete != nil {
		defer v.forget(m)
		if v.sent[key] {
			delete(v.sent, key)
			return true
		}
		return false
	}
	ok := v.entityVisible(key.t, key.id, m.Entity())
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
	for i := range u.Mutations {
		m := &u.Mutations[i]
		v.observe(m.Type(), m.EntityID(), m.Entity())
	}
	out := &state.WriteUpdate{Seq: u.Seq, Time: u.Time, Actor: u.Actor}
	for i := range u.Mutations {
		m := &u.Mutations[i]
		if !v.visible(m) {
			continue
		}
		out.Mutations = append(out.Mutations, browserMutation(m))
	}
	if len(out.Mutations) == 0 {
		return nil
	}
	return out
}

// visibleSnapshot filters an opening. An entity's entries stand or fall
// together: its newest entry decides, so a value moved out of the viewer's
// space ships none of its versions and one moved in ships all of them. Every
// identity is observed first, so a child never resolves against a parent
// entry the snapshot has not reached yet. The retained versions of a deleted
// deployment ship when their newest version would, but are not counted as
// forwarded since no later delete can follow them.
func (v *streamVisibility) visibleSnapshot(entries []*apigen.MaterialisedEntity) []apigen.MaterialisedEntity {
	newest := map[entityKey]*apigen.CoreEntity{}
	for _, e := range entries {
		v.observe(e.EntityType, e.EntityID, &e.Entity)
		newest[entityKey{e.EntityType, e.EntityID}] = &e.Entity
	}
	out := make([]apigen.MaterialisedEntity, 0, len(entries))
	for _, e := range entries {
		key := entityKey{e.EntityType, e.EntityID}
		if !v.entityVisible(key.t, key.id, newest[key]) {
			continue
		}
		if !e.Meta.Deleted {
			v.sent[key] = true
		}
		copied := *e
		copied.Entity = browserEntity(e.Entity)
		out = append(out, copied)
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
func browserMutation(m *apigen.CoreMutation) apigen.CoreMutation {
	switch {
	case m.Value.Create != nil:
		c := *m.Value.Create
		c.Entity = browserEntity(c.Entity)
		return apigen.CoreMutation{Value: apigen.CoreMutationValueOneof{Create: &c}}
	case m.Value.Update != nil:
		u := *m.Value.Update
		u.Entity = browserEntity(u.Entity)
		return apigen.CoreMutation{Value: apigen.CoreMutationValueOneof{Update: &u}}
	}
	return *m
}

func browserEntity(e apigen.CoreEntity) apigen.CoreEntity {
	out := e
	p := &out.Value
	if p.Secret != nil {
		s := *p.Secret
		s.Sealed = apigen.Maybe[apigen.SealedSecret]{}
		p.Secret = &s
	}
	if p.User != nil {
		u := *p.User
		u.Authentication = apigen.UserAuthentication{}
		p.User = &u
	}
	if p.AgentSession != nil {
		s := *p.AgentSession
		if s.Token.Present {
			s.Token.Value.Hash = apigen.Maybe[[]byte]{}
		}
		p.AgentSession = &s
	}
	if p.UserSession != nil {
		s := *p.UserSession
		s.TokenHash = apigen.Maybe[[]byte]{}
		p.UserSession = &s
	}
	if p.SystemConfig != nil {
		c := *p.SystemConfig
		c.MasterPasswordHash = apigen.Maybe[string]{}
		p.SystemConfig = &c
	}
	return out
}

// written builds the receipt of a committed write: the update stamped with
// the meta the rows now hold, filtered to what the writer may see.
func (h *Handler) written(ctx apigen.Context, m pq.Mutation) *apigen.CoreWriteUpdate {
	u := pq.Written(m)
	if err := h.Queries.StampMeta(ctx, u); err != nil {
		panic(fmt.Sprintf("stamp receipt meta: %v", err))
	}
	if visible := h.visibleUpdate(ctx, u); visible != nil {
		return visible
	}
	return &apigen.CoreWriteUpdate{Seq: u.Seq, Time: u.Time, Actor: u.Actor}
}
