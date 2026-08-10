package pq

import (
	"context"
)

// AssetVersionJoined is an asset_versions row joined with the display fields of
// its owning asset. Asset carries only ID, Key, and SpaceID.
type AssetVersionJoined struct {
	Version AssetVersion
	Asset   Asset
}

// ListAssetVersionsJoined returns every version row (blobs not loaded) joined
// with its owning asset, ordered by key then version. Pending uploads are
// excluded unless includePending is set.
func (q *Queries) ListAssetVersionsJoined(ctx context.Context, includePending bool) ([]AssetVersionJoined, error) {
	where := "WHERE v.location NOT LIKE 'pending://%'"
	if includePending {
		where = ""
	}
	rows, err := q.db.QueryContext(ctx, `
SELECT v.id, v.asset_id, v.version, v.created_at, v.created_by, v.location, v.size_bytes,
       a.key, a.space_id
FROM asset_versions v
JOIN assets a ON a.id = v.asset_id
`+where+`
ORDER BY a.key, v.version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssetVersionJoined{}
	for rows.Next() {
		var r AssetVersionJoined
		if err := rows.Scan(&r.Version.ID, &r.Version.AssetID, &r.Version.Version, &r.Version.CreatedAt, &r.Version.CreatedBy, &r.Version.Location, &r.Version.SizeBytes, &r.Asset.Key, &r.Asset.SpaceID); err != nil {
			return nil, err
		}
		r.Asset.ID = r.Version.AssetID
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetAssetVersionJoinedByID resolves a version row id (blob included) joined
// with its owning asset. Pending uploads are excluded unless includePending is
// set.
func (q *Queries) GetAssetVersionJoinedByID(ctx context.Context, assetVersionID int64, includePending bool) (AssetVersionJoined, error) {
	pendingClause := "AND v.location NOT LIKE 'pending://%'"
	if includePending {
		pendingClause = ""
	}
	var r AssetVersionJoined
	err := q.db.QueryRowContext(ctx, `
SELECT v.id, v.asset_id, v.version, v.created_at, v.created_by, v.location, v.size_bytes, v.blob,
       a.key, a.space_id
FROM asset_versions v
JOIN assets a ON a.id = v.asset_id
WHERE v.id = ? `+pendingClause, assetVersionID).
		Scan(&r.Version.ID, &r.Version.AssetID, &r.Version.Version, &r.Version.CreatedAt, &r.Version.CreatedBy, &r.Version.Location, &r.Version.SizeBytes, &r.Version.Blob, &r.Asset.Key, &r.Asset.SpaceID)
	if err != nil {
		return r, err
	}
	r.Asset.ID = r.Version.AssetID
	return r, nil
}
