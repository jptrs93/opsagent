package pq

import "github.com/jptrs93/opsagent/backend/apigen"

type NodeEvent struct {
	NodeID      int32
	Seq         int64
	Author      int32
	CreatedTime int64
	EventTime   int64
	Value       apigen.Node
}

type SecretEvent struct {
	SecretID     int32
	Seq          int64
	Author       int32
	CreatedTime  int64
	EventTime    int64
	ValueVersion int32
	Value        apigen.Secret
}

type ConfigEvent struct {
	ConfigID     int32
	Seq          int64
	Author       int32
	CreatedTime  int64
	EventTime    int64
	ValueVersion int32
	Value        apigen.Config
}

type AssetEvent struct {
	AssetID      int32
	Seq          int64
	Author       int32
	CreatedTime  int64
	EventTime    int64
	ValueVersion int32
	Value        apigen.Asset
}

type NetworkPolicyEvent struct {
	NetworkPolicyID int32
	Seq             int64
	Author          int32
	CreatedTime     int64
	EventTime       int64
	Value           apigen.NetworkPolicy
}

type ScheduledInstanceEvent struct {
	ScheduledInstanceID int32
	Seq                 int64
	Author              int32
	CreatedTime         int64
	EventTime           int64
	Value               apigen.ScheduledInstance
}

func (v *SecretEvent) SpaceID() int32 {
	if v == nil {
		return 0
	}
	return v.Value.SpaceID
}

func (v *ConfigEvent) SpaceID() int32 {
	if v == nil {
		return 0
	}
	return v.Value.SpaceID
}

func (v *AssetEvent) SpaceID() int32 {
	if v == nil {
		return 0
	}
	return v.Value.SpaceID
}

func viewMeta(seq, eventTime int64, author int32, verb apigen.AuthzVerb) EventMeta {
	return EventMeta{GlobalSeq: seq, EventTime: eventTime, Author: int64(author), EventType: verb}
}

func (v *SecretEvent) Mutation(verb apigen.AuthzVerb) Mutation {
	return SecretMutation(viewMeta(v.Seq, v.EventTime, v.Author, verb), int64(v.SecretID), v.Value)
}

func (v *ConfigEvent) Mutation(verb apigen.AuthzVerb) Mutation {
	return ConfigMutation(viewMeta(v.Seq, v.EventTime, v.Author, verb), int64(v.ConfigID), v.Value)
}

func (v *AssetEvent) Mutation(verb apigen.AuthzVerb) Mutation {
	return AssetMutation(viewMeta(v.Seq, v.EventTime, v.Author, verb), int64(v.AssetID), v.Value)
}

func (v *NetworkPolicyEvent) Mutation(verb apigen.AuthzVerb) Mutation {
	return NetworkPolicyMutation(viewMeta(v.Seq, v.EventTime, v.Author, verb), int64(v.NetworkPolicyID), v.Value)
}

func Written(m Mutation) *apigen.CoreWriteUpdate {
	u := NewUpdate(m)
	u.Seq = m.GlobalSeq
	return u
}
