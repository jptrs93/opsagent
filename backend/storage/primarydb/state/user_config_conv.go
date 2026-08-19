package state

import (
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// configFromParts builds the wire shape: identity at the root, the space and
// value logs newest first. spaces and versions must be oldest first (query
// order); versions must be non-empty — a config with no version is never
// surfaced.
func configFromParts(c Config, spaces []pq.ConfigSpace, versions []pq.ConfigVersion) *apigen.Config {
	svs := make([]*apigen.ConfigSpaceVersion, 0, len(spaces))
	for i := len(spaces) - 1; i >= 0; i-- {
		svs = append(svs, configSpaceVersionFromRow(spaces[i]))
	}
	vvs := make([]*apigen.ConfigValueVersion, 0, len(versions))
	for i := len(versions) - 1; i >= 0; i-- {
		vvs = append(vvs, configValueVersionFromRow(versions[i]))
	}
	return &apigen.Config{
		ID: int32(c.ID),
		Fs: &apigen.ConfigFs{
			Name:        c.Name,
			DirectoryID: int32(c.ValueDirectoryID),
		},
		SpaceVersions: svs,
		ValueVersions: vvs,
	}
}

func configSpaceVersionFromRow(r pq.ConfigSpace) *apigen.ConfigSpaceVersion {
	return &apigen.ConfigSpaceVersion{
		ID:        int32(r.ID),
		CreatedAt: time.UnixMilli(r.CreatedAt),
		Author:    int32(r.Author),
		SpaceID:   int32(r.SpaceID),
		GlobalSeq: r.GlobalSeq,
	}
}

func configValueVersionFromRow(v pq.ConfigVersion) *apigen.ConfigValueVersion {
	return &apigen.ConfigValueVersion{
		ID:        int32(v.ID),
		Version:   int32(v.Version),
		Value:     v.Value,
		CreatedAt: time.UnixMilli(v.CreatedAt),
		Author:    int32(v.Author),
		GlobalSeq: v.GlobalSeq,
	}
}
