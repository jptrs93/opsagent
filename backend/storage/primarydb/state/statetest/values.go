package statetest

import (
	"context"
	"sort"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

type ValueVersion struct {
	Ref                          apigen.ValueRef
	ID, Version, SpaceID, Author int32
	GlobalSeq                    int64
	CreatedAt                    time.Time
	Value, Sha256                string
	SizeBytes                    int64
}

type valueEvent struct {
	id, version, valueVersion, author, spaceID int32
	eventID, seq, eventTime                    int64
	value, sha                                 string
	size                                       int64
}

func projectValue(value any) valueEvent {
	switch e := value.(type) {
	case *apigen.SecretEvent:
		return valueEvent{id: e.SecretID, version: e.Version, valueVersion: e.ValueVersion, author: e.Author, spaceID: e.Value.SpaceID, eventID: e.EventID, seq: e.Seq, eventTime: e.EventTime}
	case *apigen.ConfigEvent:
		return valueEvent{id: e.ConfigID, version: e.Version, valueVersion: e.ValueVersion, author: e.Author, spaceID: e.Value.SpaceID, eventID: e.EventID, seq: e.Seq, eventTime: e.EventTime, value: e.Value.Value}
	case *apigen.AssetEvent:
		return projectValue(*e)
	case apigen.AssetEvent:
		return valueEvent{id: e.AssetID, version: e.Version, valueVersion: e.ValueVersion, author: e.Author, spaceID: e.Value.SpaceID, eventID: e.EventID, seq: e.Seq, eventTime: e.EventTime, sha: e.Value.Sha256, size: e.Value.SizeBytes}
	default:
		panic("not a value event")
	}
}

// valueHistory lists every row of the value up to the version of the given
// event, oldest first, straight from the event log.
func valueHistory(s *state.Service, value any) []valueEvent {
	target := projectValue(value)
	if s == nil {
		return []valueEvent{target}
	}
	ctx := context.Background()
	q := s.Queries()
	var events []valueEvent
	add := func(row any) {
		e := projectValue(row)
		if e.id == target.id && e.version <= target.version {
			events = append(events, e)
		}
	}
	switch value.(type) {
	case *apigen.SecretEvent:
		for _, e := range erru.Must(q.ListAllSecretEvents(ctx)) {
			add(e)
		}
	case *apigen.ConfigEvent:
		for _, e := range erru.Must(q.ListAllConfigEvents(ctx)) {
			add(e)
		}
	default:
		for _, e := range erru.Must(q.ListAllAssetEvents(ctx)) {
			add(e)
		}
	}
	if len(events) == 0 {
		events = append(events, target)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].version < events[j].version })
	return events
}

// ValueVersions returns the value versions of a secret, config, or asset as
// of the given event, newest first. ID is the row id of the event that wrote
// the value version; Ref is the (entity id, value version) pair.
func ValueVersions(s *state.Service, value any) []*ValueVersion {
	seen := map[int32]bool{}
	var out []*ValueVersion
	for _, e := range valueHistory(s, value) {
		if seen[e.valueVersion] {
			continue
		}
		seen[e.valueVersion] = true
		out = append(out, &ValueVersion{Ref: apigen.ValueRef{ID: e.id, Version: e.valueVersion}, ID: int32(e.eventID), Version: e.valueVersion, SpaceID: e.spaceID, Author: e.author, GlobalSeq: e.seq, CreatedAt: time.UnixMilli(e.eventTime), Value: e.value, Sha256: e.sha, SizeBytes: e.size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out
}

func LatestValue(s *state.Service, value any) *ValueVersion {
	return ValueVersions(s, value)[0]
}
