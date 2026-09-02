package state

import (
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// configFromEvents builds the wire shape from one config's full event list,
// oldest first (query order) and non-empty: identity from the latest row, the
// space and value logs newest first. Space entries are the space_changed
// events (the create event carries the initial assignment); value entries are
// the value_changed events.
func configFromEvents(events []pq.ConfigEvent) *apigen.Config {
	latest := events[len(events)-1]
	svs := []*apigen.ConfigSpaceVersion{}
	vvs := []*apigen.ConfigValueVersion{}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.SpaceChanged != 0 {
			svs = append(svs, configSpaceVersionFromEvent(e))
		}
		if e.ValueChanged != 0 {
			vvs = append(vvs, configValueVersionFromEvent(e))
		}
	}
	return &apigen.Config{
		ID: int32(latest.ConfigID),
		Fs: &apigen.ConfigFs{
			Name:        latest.Name,
			DirectoryID: int32(latest.ValueDirectoryID),
		},
		SpaceVersions: svs,
		ValueVersions: vvs,
	}
}

func configSpaceVersionFromEvent(e pq.ConfigEvent) *apigen.ConfigSpaceVersion {
	return &apigen.ConfigSpaceVersion{
		ID:        int32(e.ID),
		CreatedAt: time.UnixMilli(e.EventTime),
		Author:    int32(e.Author),
		SpaceID:   int32(e.SpaceID),
		GlobalSeq: e.GlobalSeq,
	}
}

func configValueVersionFromEvent(e pq.ConfigEvent) *apigen.ConfigValueVersion {
	return &apigen.ConfigValueVersion{
		ID:        int32(e.ID),
		Version:   int32(e.ValueVersion),
		Value:     e.Value,
		CreatedAt: time.UnixMilli(e.EventTime),
		Author:    int32(e.Author),
		GlobalSeq: e.GlobalSeq,
	}
}
