package pq

import (
	"github.com/jptrs93/opsagent/backend/apigen"
)

// Mutation is one row of an append-only entity table expressed as the event
// stream carries it: the row's envelope, the entity it belongs to, and the
// entity payload. The same converters build it for a row a producer just
// wrote and for a row read back for replay, so the live and replayed
// payloads always agree.
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

func DeploymentMutation(e *apigen.DeploymentEvent) Mutation {
	value := e.Value
	value.Version, value.SpecVersion, value.CreatedTime = e.Version, e.SpecVersion, e.CreatedTime
	return Mutation{
		EventMeta: eventMetaOf(e.Seq, e.EventTime.UnixMilli(), int64(e.Author), e.EventType),
		RowID:     e.EventID, Type: apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, ID: int64(e.DeploymentID),
		Entity: apigen.CoreEntity{Deployment: &value},
	}
}

func ScheduledInstanceMutation(e *apigen.ScheduledInstanceEvent) Mutation {
	value := e.Value
	return Mutation{
		EventMeta: eventMetaOf(e.Seq, e.EventTime, 0, e.EventType),
		RowID:     e.EventID, Type: apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE, ID: int64(e.ScheduledInstanceID),
		Entity: apigen.CoreEntity{ScheduledInstance: &value},
	}
}

func NodeMutation(e *apigen.NodeEvent) Mutation {
	value := e.Value
	value.CreatedTime = e.CreatedTime
	return Mutation{
		EventMeta: eventMetaOf(e.Seq, e.EventTime, int64(e.Author), e.EventType),
		RowID:     e.EventID, Type: apigen.CoreEntityType_CORE_ENTITY_NODE, ID: int64(e.NodeID),
		Entity: apigen.CoreEntity{Node: &value},
	}
}

// SealedValue is the sealed payload of one secret row. It rides the replica
// class of the event stream and never the browser class.
type SealedValue struct {
	SmkVersion int64
	Ciphertext []byte
	Nonce      []byte
}

func SecretMutation(e *apigen.SecretEvent, sealed SealedValue) Mutation {
	fs := *e.Value.Fs
	value := apigen.Secret{Fs: &fs, SpaceID: e.Value.SpaceID, ValueVersion: e.ValueVersion, CreatedTime: e.CreatedTime,
		SmkVersion: sealed.SmkVersion, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce}
	return Mutation{
		EventMeta: eventMetaOf(e.Seq, e.EventTime, int64(e.Author), e.EventType),
		RowID:     e.EventID, Type: apigen.CoreEntityType_CORE_ENTITY_SECRET, ID: int64(e.SecretID),
		Entity: apigen.CoreEntity{Secret: &value},
	}
}

func ConfigMutation(e *apigen.ConfigEvent) Mutation {
	fs := *e.Value.Fs
	value := e.Value
	value.Fs = &fs
	value.ValueVersion, value.CreatedTime = e.ValueVersion, e.CreatedTime
	return Mutation{
		EventMeta: eventMetaOf(e.Seq, e.EventTime, int64(e.Author), e.EventType),
		RowID:     e.EventID, Type: apigen.CoreEntityType_CORE_ENTITY_CONFIG, ID: int64(e.ConfigID),
		Entity: apigen.CoreEntity{Config: &value},
	}
}

func AssetMutation(e *apigen.AssetEvent) Mutation {
	fs := *e.Value.Fs
	value := e.Value
	value.Fs = &fs
	value.ValueVersion, value.CreatedTime = e.ValueVersion, e.CreatedTime
	return Mutation{
		EventMeta: eventMetaOf(e.Seq, e.EventTime, int64(e.Author), e.EventType),
		RowID:     e.EventID, Type: apigen.CoreEntityType_CORE_ENTITY_ASSET, ID: int64(e.AssetID),
		Entity: apigen.CoreEntity{Asset: &value},
	}
}

func NetworkPolicyMutation(e *apigen.NetworkPolicyEvent) Mutation {
	value := e.Value
	value.CreatedTime = e.CreatedTime
	return Mutation{
		EventMeta: eventMetaOf(e.Seq, e.EventTime, int64(e.Author), e.EventType),
		RowID:     e.EventID, Type: apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY, ID: int64(e.NetworkPolicyID),
		Entity: apigen.CoreEntity{NetworkPolicy: &value},
	}
}

func SpaceMutation(meta EventMeta, space apigen.Space) Mutation {
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_SPACE, ID: int64(space.ID), Entity: apigen.CoreEntity{Space: &space}}
}

func UserMutation(r UserRow) Mutation {
	value := r.Public()
	value.Credentials = r.DataBlob
	return Mutation{EventMeta: r.EventMeta, RowID: r.ID, Type: apigen.CoreEntityType_CORE_ENTITY_USER, ID: r.UserID, Entity: apigen.CoreEntity{User: &value}}
}

func ValueDirectoryMutation(meta EventMeta, d *apigen.ValueDirectory) Mutation {
	value := *d
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY, ID: int64(d.ID), Entity: apigen.CoreEntity{ValueDirectory: &value}}
}

func AssetDirectoryMutation(meta EventMeta, d apigen.AssetDirectory) Mutation {
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY, ID: int64(d.ID), Entity: apigen.CoreEntity{AssetDirectory: &d}}
}

func AuthzRuleTemplateMutation(r AuthzRuleTemplateEvent) (Mutation, error) {
	template, err := apigen.DecodeAuthzRuleTemplate(r.DataBlob)
	if err != nil {
		return Mutation{}, err
	}
	value := &apigen.AuthzRuleTemplateRecord{ID: r.TemplateID, Name: r.Name, Builtin: r.Builtin != 0, Author: r.Author, CreatedAt: r.CreatedTime, Template: template}
	return Mutation{
		EventMeta: EventMeta{GlobalSeq: r.GlobalSeq, EventTime: r.EventTime, Author: r.Author, EventType: apigen.AuthzVerb(r.EventType)},
		RowID:     r.ID, Type: apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE, ID: r.TemplateID,
		Entity: apigen.CoreEntity{AuthzRuleTemplate: value},
	}, nil
}

func AuthzGrantMutation(e *apigen.AuthzGrantEvent) Mutation {
	value := e.Value
	value.Author, value.CreatedTime = e.Author, e.CreatedTime
	return Mutation{
		EventMeta: eventMetaOf(e.Seq, e.EventTime, e.Author, e.EventType),
		RowID:     e.EventID, Type: apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT, ID: e.AuthzGrantID,
		Entity: apigen.CoreEntity{AuthzGrant: &value},
	}
}

func GlobalAccessRuleMutation(r GlobalAccessRuleEvent) (Mutation, error) {
	rule, err := apigen.DecodeAuthzGlobalRule(r.DataBlob)
	if err != nil {
		return Mutation{}, err
	}
	value := &apigen.AuthzGlobalRuleRecord{ID: r.RuleID, Name: r.Name, Author: r.Author, CreatedAt: r.CreatedTime, Rule: rule}
	return Mutation{
		EventMeta: EventMeta{GlobalSeq: r.GlobalSeq, EventTime: r.EventTime, Author: r.Author, EventType: apigen.AuthzVerb(r.EventType)},
		RowID:     r.ID, Type: apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE, ID: r.RuleID,
		Entity: apigen.CoreEntity{AuthzGlobalRule: value},
	}, nil
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

func AgentSessionMutation(r AgentSession) Mutation {
	value := r.Proto()
	value.TokenHash = r.TokenHash
	return Mutation{EventMeta: r.EventMeta, RowID: r.ID, Type: apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION, ID: r.EntityID, Entity: apigen.CoreEntity{AgentSession: value}}
}

func UserSessionMutation(r UserSession) Mutation {
	value := r.Proto()
	value.TokenHash = r.TokenHash
	return Mutation{EventMeta: r.EventMeta, RowID: r.ID, Type: apigen.CoreEntityType_CORE_ENTITY_USER_SESSION, ID: r.EntityID, Entity: apigen.CoreEntity{UserSession: value}}
}

func NixStoreResetMutation(r NixStoreResetRow) Mutation {
	value := &apigen.NixStoreReset{Repo: r.Repo, RequestedAt: r.RequestedAt}
	return Mutation{EventMeta: r.EventMeta, RowID: r.ID, Type: apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET, ID: r.EntityID, Entity: apigen.CoreEntity{NixStoreReset: value}}
}

// SecretKeyslotEntityID keys a keyslot by the node it belongs to and its kind.
func SecretKeyslotEntityID(k SecretKeyslot) int64 {
	return k.NodeID*256 + int64(k.Kind)
}

func SecretKeyslotMutation(meta EventMeta, k SecretKeyslot) Mutation {
	value := &apigen.SecretKeyslot{Kind: k.Kind, NodeID: int32(k.NodeID), SmkVersion: k.SmkVersion, WrappedSmk: k.WrappedSmk, Nonce: k.Nonce, KdfSalt: k.KdfSalt, UpdatedAt: k.UpdatedAt}
	return Mutation{EventMeta: meta, Type: apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT, ID: SecretKeyslotEntityID(k), Entity: apigen.CoreEntity{SecretKeyslot: value}}
}

// Events groups mutations already ordered by seq into one CoreWriteUpdate per
// commit. Time and actor come from the first mutation of each commit.
func Events(ms []Mutation) []*apigen.CoreWriteUpdate {
	var out []*apigen.CoreWriteUpdate
	for _, m := range ms {
		if n := len(out); n == 0 || out[n-1].Seq != m.GlobalSeq {
			out = append(out, &apigen.CoreWriteUpdate{Seq: m.GlobalSeq})
		}
		AppendMutations(out[len(out)-1], m)
	}
	return out
}
