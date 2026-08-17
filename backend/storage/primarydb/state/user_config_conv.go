package state

import (
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// configMetaFromRow builds the wire meta: identity at the root, version facts
// only in VersionRefs (newest first). refs must be non-empty — every config
// has at least one version by construction.
func configMetaFromRow(c Config, refs []*apigen.ConfigVersionMeta) *apigen.ConfigMeta {
	return &apigen.ConfigMeta{
		ID:               int32(c.ID),
		Name:             c.Name,
		SpaceID:          int32(c.SpaceID),
		ValueDirectoryID: int32(c.ValueDirectoryID),
		CreatedAt:        time.UnixMilli(c.CreatedAt),
		Author:           int32(c.Author),
		VersionRefs:      refs,
	}
}

func configVersionMetaFromRow(v pq.ConfigVersion) *apigen.ConfigVersionMeta {
	return &apigen.ConfigVersionMeta{
		ID:        int32(v.ID),
		Version:   int32(v.Version),
		Value:     v.Value,
		CreatedAt: time.UnixMilli(v.CreatedAt),
		Author:    int32(v.Author),
	}
}
