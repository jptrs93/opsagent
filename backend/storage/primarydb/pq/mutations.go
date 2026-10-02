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
	ID     int64
	Entity apigen.CoreEntity
}

func (m Mutation) Wire() *apigen.CoreMutation {
	switch m.EventType {
	case apigen.AuthzVerb_AUTHZ_VERB_DELETE:
		return &apigen.CoreMutation{Delete: &apigen.DeleteMutation{EntityType: m.Type, EntityID: m.ID}}
	case apigen.AuthzVerb_AUTHZ_VERB_CREATE:
		entity := m.Entity
		return &apigen.CoreMutation{Create: &apigen.CreateMutation{EntityType: m.Type, EntityID: m.ID, Entity: &entity}}
	default:
		entity := m.Entity
		return &apigen.CoreMutation{Update: &apigen.UpdateMutation{EntityType: m.Type, EntityID: m.ID, Entity: &entity}}
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
			u.Time, u.Actor = m.EventTime, int32(m.Author)
		}
		u.Mutations = append(u.Mutations, m.Wire())
	}
}

func eventMetaOf(seq, eventTime int64, author int64, eventType apigen.EventType) EventMeta {
	return EventMeta{GlobalSeq: seq, EventTime: eventTime, Author: author, EventType: apigen.AuthzVerb(eventType)}
}

// DeploymentMutation carries the deployment document; a delete event
// carries no payload.
func DeploymentMutation(e *apigen.DeploymentEvent) Mutation {
	m := Mutation{
		EventMeta: eventMetaOf(e.Seq, e.EventTime.UnixMilli(), int64(e.Author), e.EventType),
		RowID:     int64(e.Version), Type: apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, ID: int64(e.DeploymentID),
	}
	if e.EventType != apigen.EventType_EVENT_TYPE_DELETE {
		value := e.Value
		value.ID = e.DeploymentID
		m.Entity = apigen.CoreEntity{Deployment: &value}
	}
	return m
}

// DeploymentRecord is a deployment version for a REST response: the
// document with the meta the stream would carry for it, a delete rendered
// as the last version with deleted set under the delete's envelope.
func DeploymentRecord(e *apigen.DeploymentEvent) *apigen.DeploymentRecord {
	value := e.Value
	value.ID = e.DeploymentID
	return &apigen.DeploymentRecord{Deployment: &value, Meta: &apigen.EntityMeta{
		CreatedTime: e.CreatedTime.UnixMilli(), UpdatedTime: e.EventTime.UnixMilli(), UpdatedSeq: e.Seq, UpdatedActor: e.Author,
		Version: e.Version, SpecVersion: e.SpecVersion, Deleted: e.EventType == apigen.EventType_EVENT_TYPE_DELETE,
	}}
}

func ScheduledInstanceMutation(verb apigen.AuthzVerb, e *ScheduledInstanceEvent) Mutation {
	value := e.Value
	return Mutation{
		EventMeta: EventMeta{GlobalSeq: e.Seq, EventTime: e.EventTime, EventType: verb},
		RowID:     int64(e.ScheduledInstanceID), Type: apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE, ID: int64(e.ScheduledInstanceID),
		Entity: apigen.CoreEntity{ScheduledInstance: &value},
	}
}

func NodeMutation(verb apigen.AuthzVerb, e *NodeEvent) Mutation {
	value := e.Value
	value.ID = e.NodeID
	return Mutation{
		EventMeta: EventMeta{GlobalSeq: e.Seq, EventTime: e.EventTime, Author: int64(e.Author), EventType: verb},
		RowID:     int64(e.NodeID), Type: apigen.CoreEntityType_CORE_ENTITY_NODE, ID: int64(e.NodeID),
		Entity: apigen.CoreEntity{Node: &value},
	}
}

func SecretMutation(meta EventMeta, id int64, s apigen.Secret) Mutation {
	s.ID = int32(id)
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_SECRET, ID: id, Entity: apigen.CoreEntity{Secret: &s}}
}

func ConfigMutation(meta EventMeta, id int64, c apigen.Config) Mutation {
	c.ID = int32(id)
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_CONFIG, ID: id, Entity: apigen.CoreEntity{Config: &c}}
}

func AssetMutation(meta EventMeta, id int64, a apigen.Asset) Mutation {
	a.ID = int32(id)
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_ASSET, ID: id, Entity: apigen.CoreEntity{Asset: &a}}
}

func NetworkPolicyMutation(meta EventMeta, id int64, p apigen.NetworkPolicy) Mutation {
	p.ID = int32(id)
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY, ID: id, Entity: apigen.CoreEntity{NetworkPolicy: &p}}
}

func SpaceMutation(meta EventMeta, space apigen.Space) Mutation {
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_SPACE, ID: int64(space.ID), Entity: apigen.CoreEntity{Space: &space}}
}

// UserMutation carries the public user with the encoded InternalUser as
// Credentials.
func UserMutation(meta EventMeta, u apigen.User) Mutation {
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_USER, ID: int64(u.ID), Entity: apigen.CoreEntity{User: &u}}
}

func ValueDirectoryMutation(meta EventMeta, d *apigen.ValueDirectory) Mutation {
	value := *d
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY, ID: int64(d.ID), Entity: apigen.CoreEntity{ValueDirectory: &value}}
}

func AssetDirectoryMutation(meta EventMeta, d apigen.AssetDirectory) Mutation {
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY, ID: int64(d.ID), Entity: apigen.CoreEntity{AssetDirectory: &d}}
}

func AuthzRuleTemplateMutation(meta EventMeta, t apigen.AuthzRuleTemplate) Mutation {
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE, ID: t.ID, Entity: apigen.CoreEntity{AuthzRuleTemplate: &t}}
}

func AuthzGrantMutation(meta EventMeta, id int64, g apigen.AuthzGrant) Mutation {
	g.ID = id
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT, ID: id, Entity: apigen.CoreEntity{AuthzGrant: &g}}
}

func AuthzGlobalRuleMutation(meta EventMeta, r apigen.AuthzGlobalRule) Mutation {
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE, ID: r.ID, Entity: apigen.CoreEntity{AuthzGlobalRule: &r}}
}

const SystemConfigEntityID int64 = 1

func SystemConfigMutation(meta EventMeta, cfg *apigen.SystemConfig) Mutation {
	value := *cfg
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG, ID: SystemConfigEntityID, Entity: apigen.CoreEntity{SystemConfig: &value}}
}

func ScheduledInstanceStatusMutation(seq, eventTime int64, st *apigen.ScheduledInstanceStatus) Mutation {
	value := *st
	return Mutation{
		EventMeta: EventMeta{GlobalSeq: seq, EventTime: eventTime, EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE},
		Type:      apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS, ID: int64(st.ScheduledInstanceID),
		Entity: apigen.CoreEntity{ScheduledInstanceStatus: &value},
	}
}

func NodeStatusMutation(seq, eventTime int64, st *apigen.NodeStatus) Mutation {
	value := *st
	return Mutation{
		EventMeta: EventMeta{GlobalSeq: seq, EventTime: eventTime, EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE},
		Type:      apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS, ID: int64(st.NodeID),
		Entity: apigen.CoreEntity{NodeStatus: &value},
	}
}

// AgentSessionMutation and UserSessionMutation carry the session document
// with its token hash; id is the stream entity id, not the token's.
func AgentSessionMutation(meta EventMeta, id int64, s *apigen.AgentSession) Mutation {
	value := *s
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION, ID: id, Entity: apigen.CoreEntity{AgentSession: &value}}
}

func UserSessionMutation(meta EventMeta, id int64, s *apigen.UserSession) Mutation {
	value := *s
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_USER_SESSION, ID: id, Entity: apigen.CoreEntity{UserSession: &value}}
}

func NixStoreResetMutation(meta EventMeta, id int64, r *apigen.NixStoreReset) Mutation {
	value := *r
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET, ID: id, Entity: apigen.CoreEntity{NixStoreReset: &value}}
}

// SecretKeyslotEntityID keys a keyslot by the node it belongs to and its kind.
func SecretKeyslotEntityID(k SecretKeyslot) int64 {
	return k.NodeID*256 + int64(k.Kind)
}

func SecretKeyslotMutation(meta EventMeta, k SecretKeyslot) Mutation {
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT, ID: SecretKeyslotEntityID(k), Entity: apigen.CoreEntity{SecretKeyslot: k.Entity()}}
}

// DeleteMutation is the delete of one entity; the payload stays empty.
func DeleteMutation(meta EventMeta, t apigen.CoreEntityType, id int64) Mutation {
	meta.EventType = apigen.AuthzVerb_AUTHZ_VERB_DELETE
	return Mutation{EventMeta: meta, Type: t, ID: id}
}

func StampEntityID(e *apigen.CoreEntity, id int64) {
	if e == nil {
		return
	}
	switch {
	case e.Deployment != nil:
		e.Deployment.ID = int32(id)
	case e.Node != nil:
		e.Node.ID = int32(id)
	case e.Secret != nil:
		e.Secret.ID = int32(id)
	case e.Config != nil:
		e.Config.ID = int32(id)
	case e.Asset != nil:
		e.Asset.ID = int32(id)
	case e.NetworkPolicy != nil:
		e.NetworkPolicy.ID = int32(id)
	case e.AuthzGrant != nil:
		e.AuthzGrant.ID = id
	}
}
