package pq

import "github.com/jptrs93/opsagent/backend/apigen"

type NodeEvent struct {
	NodeID      uint64
	Seq         int64
	Author      int64
	CreatedTime int64
	EventTime   int64
	Value       apigen.Node
}

type SecretEvent struct {
	SecretID     uint64
	Seq          int64
	Author       int64
	CreatedTime  int64
	EventTime    int64
	ValueVersion uint32
	Value        apigen.Secret
}

type ConfigEvent struct {
	ConfigID     uint64
	Seq          int64
	Author       int64
	CreatedTime  int64
	EventTime    int64
	ValueVersion uint32
	Value        apigen.Config
}

type AssetEvent struct {
	AssetID      uint64
	Seq          int64
	Author       int64
	CreatedTime  int64
	EventTime    int64
	ValueVersion uint32
	Value        apigen.Asset
}

type NetworkPolicyEvent struct {
	NetworkPolicyID uint64
	Seq             int64
	Author          int64
	CreatedTime     int64
	EventTime       int64
	Value           apigen.NetworkPolicy
}

type ScheduledInstanceEvent struct {
	ScheduledInstanceID uint64
	Seq                 int64
	Author              int64
	CreatedTime         int64
	EventTime           int64
	Value               apigen.ScheduledInstance
}

func (v *SecretEvent) SpaceID() uint64 {
	if v == nil {
		return 0
	}
	return v.Value.SpaceID
}

func (v *ConfigEvent) SpaceID() uint64 {
	if v == nil {
		return 0
	}
	return v.Value.SpaceID
}

func (v *AssetEvent) SpaceID() uint64 {
	if v == nil {
		return 0
	}
	return v.Value.SpaceID
}

func viewMeta(seq, eventTime, author int64, verb apigen.AuthzVerb) EventMeta {
	return EventMeta{GlobalSeq: seq, EventTime: eventTime, Author: author, EventType: verb}
}

func (v *SecretEvent) Mutation(verb apigen.AuthzVerb) Mutation {
	return SecretMutation(viewMeta(v.Seq, v.EventTime, v.Author, verb), v.SecretID, v.Value)
}

func (v *ConfigEvent) Mutation(verb apigen.AuthzVerb) Mutation {
	return ConfigMutation(viewMeta(v.Seq, v.EventTime, v.Author, verb), v.ConfigID, v.Value)
}

func (v *AssetEvent) Mutation(verb apigen.AuthzVerb) Mutation {
	return AssetMutation(viewMeta(v.Seq, v.EventTime, v.Author, verb), v.AssetID, v.Value)
}

func (v *NetworkPolicyEvent) Mutation(verb apigen.AuthzVerb) Mutation {
	return NetworkPolicyMutation(viewMeta(v.Seq, v.EventTime, v.Author, verb), v.NetworkPolicyID, v.Value)
}

func Written(m Mutation) *apigen.CoreWriteUpdate {
	u := NewUpdate(m)
	u.Seq = m.GlobalSeq
	return u
}
