package state

import (
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// assetMetaFromRow builds the wire meta: identity at the root, version facts
// only in VersionRefs (newest first). refs must be non-empty — an asset with
// no version is never surfaced as a meta.
func assetMetaFromRow(a Asset, refs []*apigen.AssetVersionMeta) *apigen.AssetMeta {
	return &apigen.AssetMeta{
		ID:               int32(a.ID),
		Key:              a.Key,
		SpaceID:          int32(a.SpaceID),
		AssetDirectoryID: int32(a.AssetDirectoryID),
		CreatedAt:        time.UnixMilli(a.CreatedAt),
		VersionRefs:      refs,
	}
}

// assetLocationString derives the display location: empty for inline content,
// otherwise the active storage side keyed by the content-store id.
func assetLocationString(s pq.AssetStoreRef) string {
	if s.RemoteStatus == 1 {
		return "s3://" + s.ID
	}
	if s.LocalStatus == 1 {
		return "local://" + s.ID
	}
	return ""
}

func assetVersionMetaFromJoined(r pq.AssetVersionJoined) *apigen.AssetVersionMeta {
	return &apigen.AssetVersionMeta{
		ID:        int32(r.Version.ID),
		Version:   int32(r.Version.Version),
		CreatedAt: time.UnixMilli(r.Version.CreatedAt),
		Author:    int32(r.Version.Author),
		SizeBytes: int32(r.Version.SizeBytes),
		Location:  assetLocationString(r.Store),
		Sha256:    r.Version.Sha256,
	}
}

func assetVersionFromJoined(a Asset, r pq.AssetVersionJoined) *apigen.AssetVersion {
	return &apigen.AssetVersion{
		ID:        int32(r.Version.ID),
		AssetID:   int32(a.ID),
		Key:       a.Key,
		SpaceID:   int32(a.SpaceID),
		CreatedAt: time.UnixMilli(r.Version.CreatedAt),
		Author:    int32(r.Version.Author),
		Version:   int32(r.Version.Version),
		Location:  assetLocationString(r.Store),
		SizeBytes: int32(r.Version.SizeBytes),
		Sha256:    r.Version.Sha256,
		Blob:      r.Store.InlineBlob,
	}
}
