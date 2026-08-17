package pq

import (
	"context"
)

// AssetStoreRef carries the content-store fields a version row resolves to
// through its sha256 link. InlineBlob is loaded only by the queries that say
// so.
type AssetStoreRef struct {
	ID           string
	LocalStatus  int64
	RemoteStatus int64
	InlineSize   int64
	InlineBlob   []byte
}

// AssetVersionJoined is an asset_versions row joined with its content-store
// row and, where the query says so, the display fields of its owning asset
// (ID, Key, SpaceID).
type AssetVersionJoined struct {
	Version AssetVersion
	Asset   Asset
	Store   AssetStoreRef
}

const assetVersionJoinedColumns = `v.id, v.asset_id, v.version, v.created_at, v.created_by, v.size_bytes, v.sha256,
       s.id, s.local_status, s.remote_status, CAST(LENGTH(s.inline_blob) AS INTEGER)`

func scanAssetVersionJoined(scan func(dest ...any) error, r *AssetVersionJoined, extra ...any) error {
	dest := []any{
		&r.Version.ID, &r.Version.AssetID, &r.Version.Version, &r.Version.CreatedAt, &r.Version.CreatedBy,
		&r.Version.SizeBytes, &r.Version.Sha256,
		&r.Store.ID, &r.Store.LocalStatus, &r.Store.RemoteStatus, &r.Store.InlineSize,
	}
	return scan(append(dest, extra...)...)
}

// ListAssetVersionsJoined returns every version row (inline blobs not loaded)
// joined with its store row and owning asset, ordered by key then version.
func (q *Queries) ListAssetVersionsJoined(ctx context.Context) ([]AssetVersionJoined, error) {
	rows, err := q.db.QueryContext(ctx, `
SELECT `+assetVersionJoinedColumns+`, a.key, a.space_id
FROM asset_versions v
JOIN asset_store s ON s.sha256 = v.sha256
JOIN assets a ON a.id = v.asset_id
ORDER BY a.key, v.version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssetVersionJoined{}
	for rows.Next() {
		var r AssetVersionJoined
		if err := scanAssetVersionJoined(rows.Scan, &r, &r.Asset.Key, &r.Asset.SpaceID); err != nil {
			return nil, err
		}
		r.Asset.ID = r.Version.AssetID
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetAssetVersionJoinedByID resolves a version row id (inline blob included)
// joined with its store row and owning asset.
func (q *Queries) GetAssetVersionJoinedByID(ctx context.Context, assetVersionID int64) (AssetVersionJoined, error) {
	var r AssetVersionJoined
	err := scanAssetVersionJoined(q.db.QueryRowContext(ctx, `
SELECT `+assetVersionJoinedColumns+`, s.inline_blob, a.key, a.space_id
FROM asset_versions v
JOIN asset_store s ON s.sha256 = v.sha256
JOIN assets a ON a.id = v.asset_id
WHERE v.id = ?`, assetVersionID).Scan, &r, &r.Store.InlineBlob, &r.Asset.Key, &r.Asset.SpaceID)
	if err != nil {
		return r, err
	}
	r.Asset.ID = r.Version.AssetID
	return r, nil
}

// ListAssetVersionsOfAsset returns every version of one asset (inline blobs
// included), oldest first.
func (q *Queries) ListAssetVersionsOfAsset(ctx context.Context, assetID int64) ([]AssetVersionJoined, error) {
	rows, err := q.db.QueryContext(ctx, `
SELECT `+assetVersionJoinedColumns+`, s.inline_blob
FROM asset_versions v
JOIN asset_store s ON s.sha256 = v.sha256
WHERE v.asset_id = ?
ORDER BY v.version ASC`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssetVersionJoined{}
	for rows.Next() {
		var r AssetVersionJoined
		if err := scanAssetVersionJoined(rows.Scan, &r, &r.Store.InlineBlob); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetAssetVersionJoinedByNumber resolves one version of an asset by number
// (inline blob included), or the latest when version is 0.
func (q *Queries) GetAssetVersionJoinedByNumber(ctx context.Context, assetID, version int64) (AssetVersionJoined, error) {
	query := `
SELECT ` + assetVersionJoinedColumns + `, s.inline_blob
FROM asset_versions v
JOIN asset_store s ON s.sha256 = v.sha256
WHERE v.asset_id = ? AND v.version = ?`
	args := []any{assetID, version}
	if version == 0 {
		query = `
SELECT ` + assetVersionJoinedColumns + `, s.inline_blob
FROM asset_versions v
JOIN asset_store s ON s.sha256 = v.sha256
WHERE v.asset_id = ?
ORDER BY v.version DESC
LIMIT 1`
		args = []any{assetID}
	}
	var r AssetVersionJoined
	err := scanAssetVersionJoined(q.db.QueryRowContext(ctx, query, args...).Scan, &r, &r.Store.InlineBlob)
	return r, err
}
