package pq

import (
	"github.com/jptrs93/opsagent/backend/apigen"
)

// Mutation is one entity write expressed as the event stream carries it: the
// envelope, the entity it belongs to, and the entity payload. Producers build
// it for the write they made; openings build it from the materialised rows.
// RowID only orders mutations that share a seq. The meta the stream carries
// is stamped by the reducer inside Commit, or by StampMeta on a receipt.
type Mutation struct {
	EventMeta
	RowID  int64
	Type   apigen.CoreEntityType
	ID     uint64
	Entity apigen.CoreEntity
}

func (m Mutation) Wire() apigen.CoreMutation {
	switch m.EventType {
	case apigen.AuthzVerb_AUTHZ_VERB_DELETE:
		return apigen.DeleteMutationOf(m.Type, m.ID)
	case apigen.AuthzVerb_AUTHZ_VERB_CREATE:
		return apigen.CreateMutationOf(m.Type, m.ID, m.Entity)
	default:
		return apigen.UpdateMutationOf(m.Type, m.ID, m.Entity)
	}
}

// NewUpdate builds the event a mutate function returns from the rows it
// wrote. The event's clock and actor are the first mutation's.
func NewUpdate(ms ...Mutation) *apigen.CoreWriteUpdate {
	u := &apigen.CoreWriteUpdate{}
	AppendMutations(u, ms...)
	return u
}

func AppendMutations(u *apigen.CoreWriteUpdate, ms ...Mutation) {
	for _, m := range ms {
		if len(u.Mutations) == 0 {
			u.Time, u.Actor = m.EventTime, m.Author
		}
		u.Mutations = append(u.Mutations, m.Wire())
	}
}

func entityOf(v apigen.CoreEntityValueOneof) apigen.CoreEntity {
	return apigen.CoreEntity{Value: v}
}

// DeploymentMutation carries the deployment document under the verb its meta
// implies: a delete when the record is deleted, a create at version 1, an
// update otherwise. A delete carries no payload.
func DeploymentMutation(r *apigen.DeploymentRecord) Mutation {
	verb := apigen.AuthzVerb_AUTHZ_VERB_UPDATE
	switch {
	case r.Meta.Deleted:
		verb = apigen.AuthzVerb_AUTHZ_VERB_DELETE
	case r.Meta.Version == 1:
		verb = apigen.AuthzVerb_AUTHZ_VERB_CREATE
	}
	return DeploymentMutationOf(verb, r)
}

func DeploymentMutationOf(verb apigen.AuthzVerb, r *apigen.DeploymentRecord) Mutation {
	m := Mutation{
		EventMeta: EventMeta{GlobalSeq: r.Meta.UpdatedSeq, EventTime: r.Meta.UpdatedTime, Author: r.Meta.UpdatedActor, EventType: verb},
		RowID:     int64(r.Meta.Version), Type: apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, ID: r.Deployment.ID,
	}
	if verb != apigen.AuthzVerb_AUTHZ_VERB_DELETE {
		value := r.Deployment
		m.Entity = entityOf(apigen.CoreEntityValueOneof{Deployment: &value})
	}
	return m
}

func ScheduledInstanceMutation(verb apigen.AuthzVerb, e *ScheduledInstanceEvent) Mutation {
	value := e.Value
	value.ID = e.ScheduledInstanceID
	return Mutation{
		EventMeta: EventMeta{GlobalSeq: e.Seq, EventTime: e.EventTime, EventType: verb},
		RowID:     int64(e.ScheduledInstanceID), Type: apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE, ID: e.ScheduledInstanceID,
		Entity: entityOf(apigen.CoreEntityValueOneof{ScheduledInstance: &value}),
	}
}

func NodeMutation(verb apigen.AuthzVerb, e *NodeEvent) Mutation {
	value := e.Value
	value.ID = e.NodeID
	return Mutation{
		EventMeta: EventMeta{GlobalSeq: e.Seq, EventTime: e.EventTime, Author: e.Author, EventType: verb},
		RowID:     int64(e.NodeID), Type: apigen.CoreEntityType_CORE_ENTITY_NODE, ID: e.NodeID,
		Entity: entityOf(apigen.CoreEntityValueOneof{Node: &value}),
	}
}

func SecretMutation(meta EventMeta, id uint64, s apigen.Secret) Mutation {
	s.ID = id
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_SECRET, ID: id, Entity: entityOf(apigen.CoreEntityValueOneof{Secret: &s})}
}

func ConfigMutation(meta EventMeta, id uint64, c apigen.Config) Mutation {
	c.ID = id
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_CONFIG, ID: id, Entity: entityOf(apigen.CoreEntityValueOneof{Config: &c})}
}

func AssetMutation(meta EventMeta, id uint64, a apigen.Asset) Mutation {
	a.ID = id
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_ASSET, ID: id, Entity: entityOf(apigen.CoreEntityValueOneof{Asset: &a})}
}

func NetworkPolicyMutation(meta EventMeta, id uint64, p apigen.NetworkPolicy) Mutation {
	p.ID = id
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY, ID: id, Entity: entityOf(apigen.CoreEntityValueOneof{NetworkPolicy: &p})}
}

func SpaceMutation(meta EventMeta, space apigen.Space) Mutation {
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_SPACE, ID: space.ID, Entity: entityOf(apigen.CoreEntityValueOneof{Space: &space})}
}

// UserMutation carries the account with its authentication material.
func UserMutation(meta EventMeta, u apigen.User) Mutation {
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_USER, ID: u.ID, Entity: entityOf(apigen.CoreEntityValueOneof{User: &u})}
}

func ValueDirectoryMutation(meta EventMeta, d *apigen.ValueDirectory) Mutation {
	value := *d
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY, ID: d.ID, Entity: entityOf(apigen.CoreEntityValueOneof{ValueDirectory: &value})}
}

func AssetDirectoryMutation(meta EventMeta, d apigen.AssetDirectory) Mutation {
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY, ID: d.ID, Entity: entityOf(apigen.CoreEntityValueOneof{AssetDirectory: &d})}
}

func AuthzGrantTemplateMutation(meta EventMeta, t apigen.AuthzGrantTemplate) Mutation {
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT_TEMPLATE, ID: t.ID, Entity: entityOf(apigen.CoreEntityValueOneof{AuthzGrantTemplate: &t})}
}

func AuthzGrantMutation(meta EventMeta, id uint64, g apigen.AuthzGrant) Mutation {
	g.ID = id
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT, ID: id, Entity: entityOf(apigen.CoreEntityValueOneof{AuthzGrant: &g})}
}

func AuthzGlobalRuleMutation(meta EventMeta, r apigen.AuthzGlobalRule) Mutation {
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE, ID: r.ID, Entity: entityOf(apigen.CoreEntityValueOneof{AuthzGlobalRule: &r})}
}

const SystemConfigEntityID uint64 = 1

func SystemConfigMutation(meta EventMeta, cfg *apigen.SystemConfig) Mutation {
	value := *cfg
	value.ID = uint32(SystemConfigEntityID)
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG, ID: SystemConfigEntityID, Entity: entityOf(apigen.CoreEntityValueOneof{SystemConfig: &value})}
}

func ScheduledInstanceStatusMutation(seq, eventTime int64, st *apigen.ScheduledInstanceStatus) Mutation {
	value := *st
	return Mutation{
		EventMeta: EventMeta{GlobalSeq: seq, EventTime: eventTime, EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE},
		Type:      apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS, ID: st.ScheduledInstanceID,
		Entity: entityOf(apigen.CoreEntityValueOneof{ScheduledInstanceStatus: &value}),
	}
}

func NodeStatusMutation(seq, eventTime int64, st *apigen.NodeStatus) Mutation {
	value := *st
	return Mutation{
		EventMeta: EventMeta{GlobalSeq: seq, EventTime: eventTime, EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE},
		Type:      apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS, ID: st.NodeID,
		Entity: entityOf(apigen.CoreEntityValueOneof{NodeStatus: &value}),
	}
}

// AgentSessionMutation and UserSessionMutation carry the session document
// with its token hash; id is the stream entity id, not the token's.
func AgentSessionMutation(meta EventMeta, id uint64, s *apigen.AgentSession) Mutation {
	value := *s
	value.ID = id
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION, ID: id, Entity: entityOf(apigen.CoreEntityValueOneof{AgentSession: &value})}
}

func UserSessionMutation(meta EventMeta, id uint64, s *apigen.UserSession) Mutation {
	value := *s
	value.ID = id
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_USER_SESSION, ID: id, Entity: entityOf(apigen.CoreEntityValueOneof{UserSession: &value})}
}

func NixStoreResetMutation(meta EventMeta, id uint64, r *apigen.NixStoreReset) Mutation {
	value := *r
	value.ID = id
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET, ID: id, Entity: entityOf(apigen.CoreEntityValueOneof{NixStoreReset: &value})}
}

func SecretKeyslotMutation(meta EventMeta, k apigen.SecretKeyslot) Mutation {
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT, ID: k.ID, Entity: entityOf(apigen.CoreEntityValueOneof{SecretKeyslot: &k})}
}

// DeleteMutation is the delete of one entity; the payload stays empty.
func DeleteMutation(meta EventMeta, t apigen.CoreEntityType, id uint64) Mutation {
	meta.EventType = apigen.AuthzVerb_AUTHZ_VERB_DELETE
	return Mutation{EventMeta: meta, Type: t, ID: id}
}

func StampEntityID(e *apigen.CoreEntity, id uint64) {
	if e == nil {
		return
	}
	v := &e.Value
	switch {
	case v.Deployment != nil:
		v.Deployment.ID = id
	case v.ScheduledInstance != nil:
		v.ScheduledInstance.ID = id
	case v.Node != nil:
		v.Node.ID = id
	case v.Secret != nil:
		v.Secret.ID = id
	case v.Config != nil:
		v.Config.ID = id
	case v.Asset != nil:
		v.Asset.ID = id
	case v.NetworkPolicy != nil:
		v.NetworkPolicy.ID = id
	case v.Space != nil:
		v.Space.ID = id
	case v.User != nil:
		v.User.ID = id
	case v.ValueDirectory != nil:
		v.ValueDirectory.ID = id
	case v.AssetDirectory != nil:
		v.AssetDirectory.ID = id
	case v.AuthzGrantTemplate != nil:
		v.AuthzGrantTemplate.ID = id
	case v.AuthzGrant != nil:
		v.AuthzGrant.ID = id
	case v.AuthzGlobalRule != nil:
		v.AuthzGlobalRule.ID = id
	case v.SystemConfig != nil:
		v.SystemConfig.ID = uint32(id)
	case v.ScheduledInstanceStatus != nil:
		v.ScheduledInstanceStatus.ScheduledInstanceID = id
	case v.NodeStatus != nil:
		v.NodeStatus.NodeID = id
	case v.AgentSession != nil:
		v.AgentSession.ID = id
	case v.UserSession != nil:
		v.UserSession.ID = id
	case v.NixStoreReset != nil:
		v.NixStoreReset.ID = id
	case v.SecretKeyslot != nil:
		v.SecretKeyslot.ID = id
	}
}
