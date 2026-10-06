package statetest

import (
	"context"
	"encoding/hex"
	"sort"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

type ValueVersion struct {
	Ref           apigen.ValueRef
	Version       uint32
	SpaceID       uint64
	Author        int64
	GlobalSeq     int64
	CreatedAt     time.Time
	Value, Sha256 string
	SizeBytes     int64
}

type valueEvent struct {
	id             uint64
	valueVersion   uint32
	author         int64
	spaceID        uint64
	seq, eventTime int64
	value, sha     string
	size           int64
}

func projectValue(value any) valueEvent {
	switch e := value.(type) {
	case *pq.SecretEvent:
		return valueEvent{id: e.SecretID, valueVersion: e.ValueVersion, author: e.Author, spaceID: e.Value.SpaceID, seq: e.Seq, eventTime: e.EventTime}
	case *pq.ConfigEvent:
		return valueEvent{id: e.ConfigID, valueVersion: e.ValueVersion, author: e.Author, spaceID: e.Value.SpaceID, seq: e.Seq, eventTime: e.EventTime, value: e.Value.Value}
	case *pq.AssetEvent:
		return projectValue(*e)
	case pq.AssetEvent:
		return valueEvent{id: e.AssetID, valueVersion: e.ValueVersion, author: e.Author, spaceID: e.Value.SpaceID, seq: e.Seq, eventTime: e.EventTime, sha: hex.EncodeToString(e.Value.Sha256), size: int64(e.Value.SizeBytes)}
	default:
		panic("not a value event")
	}
}

// valueHistory lists every version row of the value up to the value version
// of the given event, oldest first, straight from the version tables.
func valueHistory(s *state.Service, value any) []valueEvent {
	target := projectValue(value)
	if s == nil {
		return []valueEvent{target}
	}
	ctx := context.Background()
	q := s.Queries()
	var events []valueEvent
	add := func(e valueEvent) {
		if e.id == target.id && e.valueVersion <= target.valueVersion {
			events = append(events, e)
		}
	}
	switch value.(type) {
	case *pq.SecretEvent:
		for _, v := range erru.Must(q.ListSecretVersions(ctx)) {
			add(valueEvent{id: v.SecretID, valueVersion: v.ValueVersion, author: v.Author, spaceID: target.spaceID, seq: v.Seq, eventTime: v.EventTime})
		}
	case *pq.ConfigEvent:
		for _, v := range erru.Must(q.ListConfigVersions(ctx)) {
			add(valueEvent{id: v.ConfigID, valueVersion: v.ValueVersion, author: v.Author, spaceID: target.spaceID, seq: v.Seq, eventTime: v.EventTime, value: v.Value})
		}
	default:
		for _, v := range erru.Must(q.ListAssetVersions(ctx)) {
			add(valueEvent{id: v.AssetID, valueVersion: v.ValueVersion, author: v.Author, spaceID: target.spaceID, seq: v.Seq, eventTime: v.EventTime, sha: v.Sha256, size: v.SizeBytes})
		}
	}
	if len(events) == 0 {
		events = append(events, target)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].valueVersion < events[j].valueVersion })
	return events
}

// ValueVersions returns the value versions of a secret, config, or asset as
// of the given event, newest first. Ref is the (entity id, value version)
// pair; SpaceID is the entity's current space.
func ValueVersions(s *state.Service, value any) []*ValueVersion {
	var out []*ValueVersion
	for _, e := range valueHistory(s, value) {
		out = append(out, &ValueVersion{Ref: apigen.ValueRef{ID: e.id, Version: e.valueVersion}, Version: e.valueVersion, SpaceID: e.spaceID, Author: e.author, GlobalSeq: e.seq, CreatedAt: time.UnixMilli(e.eventTime), Value: e.value, Sha256: e.sha, SizeBytes: e.size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out
}

func LatestValue(s *state.Service, value any) *ValueVersion {
	return ValueVersions(s, value)[0]
}
