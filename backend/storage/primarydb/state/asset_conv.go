package state

import (
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// assetFromParts builds the wire shape: identity at the root, the space and
// content logs newest first. events and versions must be oldest first (query
// order); versions must be non-empty — an asset with no version is never
// surfaced. Space entries are the space_changed events; the create event
// carries the initial assignment.
func assetFromParts(a Asset, events []pq.AssetEvent, versions []pq.AssetVersionJoined) *apigen.Asset {
	svs := []*apigen.AssetSpaceVersion{}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].SpaceChanged == 0 {
			continue
		}
		svs = append(svs, assetSpaceVersionFromEvent(events[i]))
	}
	cvs := make([]*apigen.AssetContentVersion, 0, len(versions))
	for i := len(versions) - 1; i >= 0; i-- {
		cvs = append(cvs, assetContentVersionFromJoined(versions[i]))
	}
	return &apigen.Asset{
		ID: int32(a.ID),
		Fs: &apigen.AssetFs{
			Key:         a.Key,
			DirectoryID: int32(a.AssetDirectoryID),
		},
		SpaceVersions:   svs,
		ContentVersions: cvs,
	}
}

func assetSpaceVersionFromEvent(e pq.AssetEvent) *apigen.AssetSpaceVersion {
	return &apigen.AssetSpaceVersion{
		ID:        int32(e.ID),
		CreatedAt: time.UnixMilli(e.EventTime),
		Author:    int32(e.Author),
		SpaceID:   int32(e.SpaceID),
		GlobalSeq: e.GlobalSeq,
	}
}

func assetContentVersionFromJoined(r pq.AssetVersionJoined) *apigen.AssetContentVersion {
	return &apigen.AssetContentVersion{
		ID:        int32(r.Version.ID),
		Version:   int32(r.Version.Version),
		CreatedAt: time.UnixMilli(r.Version.CreatedAt),
		Author:    int32(r.Version.Author),
		Sha256:    r.Version.Sha256,
		SizeBytes: r.Version.SizeBytes,
		GlobalSeq: r.Version.GlobalSeq,
	}
}
