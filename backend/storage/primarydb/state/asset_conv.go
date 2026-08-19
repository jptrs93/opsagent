package state

import (
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// assetFromParts builds the wire shape: identity at the root, the space and
// content logs newest first. spaces and versions must be oldest first (query
// order); versions must be non-empty — an asset with no version is never
// surfaced.
func assetFromParts(a Asset, spaces []pq.AssetSpace, versions []pq.AssetVersionJoined) *apigen.Asset {
	svs := make([]*apigen.AssetSpaceVersion, 0, len(spaces))
	for i := len(spaces) - 1; i >= 0; i-- {
		svs = append(svs, assetSpaceVersionFromRow(spaces[i]))
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

func assetSpaceVersionFromRow(r pq.AssetSpace) *apigen.AssetSpaceVersion {
	return &apigen.AssetSpaceVersion{
		ID:        int32(r.ID),
		CreatedAt: time.UnixMilli(r.CreatedAt),
		Author:    int32(r.Author),
		SpaceID:   int32(r.SpaceID),
		GlobalSeq: r.GlobalSeq,
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
