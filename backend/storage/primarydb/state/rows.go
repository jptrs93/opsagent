package state

import "github.com/jptrs93/opsagent/backend/storage/primarydb/pq"

// Row types from the SQL layer that appear in Service's exported API. Aliased
// so callers keep naming them primarydb.X rather than reaching into pq.
type (
	Asset                = pq.Asset
	AssetDirectory       = pq.AssetDirectory
	AssetStore           = pq.AssetStore
	AssetVersionJoined   = pq.AssetVersionJoined
	ValueDirectory       = pq.ValueDirectory
	Secret               = pq.Secret
	Config               = pq.Config
	AssetMigration       = pq.AssetMigration
	SystemConfigRevision = pq.SystemConfigRevision
)
