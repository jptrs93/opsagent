package statetest

import (
	"context"
	"sort"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type SnapshotSource interface {
	BuildSnapshot(context.Context) *apigen.Snapshot
}

type ValueVersion struct {
	ID, Version, SpaceID, Author int32
	GlobalSeq                    int64
	CreatedAt                    time.Time
	Value, Sha256                string
	SizeBytes                    int64
}

type event struct {
	id, version, valueVersion, spaceVersion, author, spaceID int32
	eventID, seq, eventTime                                  int64
	value, sha                                               string
	size                                                     int64
}

func project(value any) event {
	switch e := value.(type) {
	case *apigen.SecretEvent:
		return event{id: e.SecretID, version: e.Version, valueVersion: e.ValueVersion, spaceVersion: e.SpaceVersion, author: e.Author, spaceID: e.Value.SpaceID, eventID: e.EventID, seq: e.Seq, eventTime: e.EventTime}
	case *apigen.ConfigEvent:
		return event{id: e.ConfigID, version: e.Version, valueVersion: e.ValueVersion, spaceVersion: e.SpaceVersion, author: e.Author, spaceID: e.Value.SpaceID, eventID: e.EventID, seq: e.Seq, eventTime: e.EventTime, value: e.Value.Value}
	case *apigen.AssetEvent:
		return event{id: e.AssetID, version: e.Version, valueVersion: e.ValueVersion, spaceVersion: e.SpaceVersion, author: e.Author, spaceID: e.Value.SpaceID, eventID: e.EventID, seq: e.Seq, eventTime: e.EventTime, sha: e.Value.Sha256, size: e.Value.SizeBytes}
	default:
		panic("not a value event")
	}
}

func history(source SnapshotSource, value any) []event {
	target := project(value)
	if source == nil {
		return []event{target}
	}
	snapshot := source.BuildSnapshot(context.Background())
	var events []event
	appendEvent := func(value any) {
		e := project(value)
		if e.id == target.id && e.version <= target.version {
			events = append(events, e)
		}
	}
	switch value.(type) {
	case *apigen.SecretEvent:
		for _, e := range snapshot.SecretEvents {
			appendEvent(e)
		}
	case *apigen.ConfigEvent:
		for _, e := range snapshot.ConfigEvents {
			appendEvent(e)
		}
	case *apigen.AssetEvent:
		for _, e := range snapshot.AssetEvents {
			appendEvent(e)
		}
	}
	if len(events) == 0 {
		events = append(events, target)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].version < events[j].version })
	return events
}

func versions(source SnapshotSource, value any, space bool) []*ValueVersion {
	seen := map[int32]bool{}
	var out []*ValueVersion
	for _, e := range history(source, value) {
		key := e.valueVersion
		if space {
			key = e.spaceVersion
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, &ValueVersion{ID: int32(e.eventID), Version: key, SpaceID: e.spaceID, Author: e.author, GlobalSeq: e.seq, CreatedAt: time.UnixMilli(e.eventTime), Value: e.value, Sha256: e.sha, SizeBytes: e.size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out
}

func ValueVersions(source SnapshotSource, value any) []*ValueVersion {
	return versions(source, value, false)
}

func SpaceVersions(source SnapshotSource, value any) []*ValueVersion {
	return versions(source, value, true)
}

func LatestValue(source SnapshotSource, value any) *ValueVersion {
	return ValueVersions(source, value)[0]
}
