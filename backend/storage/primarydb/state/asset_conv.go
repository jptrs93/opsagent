package state

import (
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// assetMetaFromRow builds the wire meta: identity at the root, version facts
// only in VersionRefs (newest first). refs must be non-empty — an asset with
// no published version is never surfaced as a meta.
func assetMetaFromRow(a Asset, refs []*apigen.AssetVersionMeta) *apigen.AssetMeta {
	return &apigen.AssetMeta{
		ID:               int32(a.ID),
		Key:              a.Key,
		SpaceID:          int32(a.SpaceID),
		AssetDirectoryID: int32(a.AssetDirectoryID),
		CreatedAt:        time.UnixMilli(a.CreatedAt),
		CreatedBy:        int32(a.CreatedBy),
		VersionRefs:      refs,
	}
}

func assetVersionMetaFromRow(v pq.AssetVersion) *apigen.AssetVersionMeta {
	return &apigen.AssetVersionMeta{
		ID:        int32(v.ID),
		Version:   int32(v.Version),
		CreatedAt: time.UnixMilli(v.CreatedAt),
		CreatedBy: int32(v.CreatedBy),
		SizeBytes: int32(v.SizeBytes),
		Location:  v.Location,
	}
}

func assetVersionFromRows(a Asset, v pq.AssetVersion) *apigen.AssetVersion {
	return &apigen.AssetVersion{
		ID:        int32(v.ID),
		AssetID:   int32(a.ID),
		Key:       a.Key,
		SpaceID:   int32(a.SpaceID),
		CreatedAt: time.UnixMilli(v.CreatedAt),
		CreatedBy: int32(v.CreatedBy),
		Version:   int32(v.Version),
		Location:  v.Location,
		SizeBytes: int32(v.SizeBytes),
		Blob:      v.Blob,
	}
}
