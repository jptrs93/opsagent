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

// AssetRow is a live asset identity with its current space — the newest row
// of the append-only asset_spaces log, written first in the creation tx.
// Reads exclude soft-deleted rows (deleted_at != 0).
type AssetRow struct {
	ID               int64
	Key              string
	SpaceID          int64
	AssetDirectoryID int64
	CreatedAt        int64
}

const assetSpaceExpr = `(SELECT sp.space_id FROM asset_spaces sp WHERE sp.asset_id = a.id ORDER BY sp.id DESC LIMIT 1)`

const assetRowSelect = `SELECT a.id, a.key, ` + assetSpaceExpr + `, a.asset_directory_id, a.created_at
FROM assets a`

func scanAssetRow(scan func(dest ...any) error) (AssetRow, error) {
	var r AssetRow
	err := scan(&r.ID, &r.Key, &r.SpaceID, &r.AssetDirectoryID, &r.CreatedAt)
	return r, err
}

func (q *Queries) ListAssetRows(ctx context.Context) ([]AssetRow, error) {
	rows, err := q.db.QueryContext(ctx, assetRowSelect+` WHERE a.deleted_at = 0 ORDER BY a.key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssetRow{}
	for rows.Next() {
		r, err := scanAssetRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) GetAssetByID(ctx context.Context, id int64) (AssetRow, error) {
	return scanAssetRow(q.db.QueryRowContext(ctx, assetRowSelect+` WHERE a.id = ? AND a.deleted_at = 0`, id).Scan)
}

type GetAssetInDirectoryByKeyParams struct {
	SpaceID          int64
	AssetDirectoryID int64
	Key              string
}

func (q *Queries) GetAssetInDirectoryByKey(ctx context.Context, arg GetAssetInDirectoryByKeyParams) (AssetRow, error) {
	return scanAssetRow(q.db.QueryRowContext(ctx, assetRowSelect+
		` WHERE a.deleted_at = 0 AND a.asset_directory_id = ? AND a.key = ? AND `+assetSpaceExpr+` = ?`,
		arg.AssetDirectoryID, arg.Key, arg.SpaceID).Scan)
}

type CountAssetSiblingsWithKeyParams struct {
	SpaceID          int64
	AssetDirectoryID int64
	Key              string
	ID               int64
}

func (q *Queries) CountAssetSiblingsWithKey(ctx context.Context, arg CountAssetSiblingsWithKeyParams) (int64, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assets a
WHERE a.deleted_at = 0 AND a.asset_directory_id = ? AND a.key = ? AND a.id != ? AND `+assetSpaceExpr+` = ?`,
		arg.AssetDirectoryID, arg.Key, arg.ID, arg.SpaceID).Scan(&n)
	return n, err
}

// AssetVersionJoined is an asset_versions row joined with its content-store
// row and, where the query says so, the display fields of its owning asset
// (ID, Key, SpaceID).
type AssetVersionJoined struct {
	Version AssetVersion
	Asset   AssetRow
	Store   AssetStoreRef
}

const assetVersionJoinedColumns = `v.id, v.asset_id, v.version, v.created_at, v.author, v.size_bytes, v.sha256,
       s.id, s.local_status, s.remote_status, CAST(LENGTH(s.inline_blob) AS INTEGER)`

func scanAssetVersionJoined(scan func(dest ...any) error, r *AssetVersionJoined, extra ...any) error {
	dest := []any{
		&r.Version.ID, &r.Version.AssetID, &r.Version.Version, &r.Version.CreatedAt, &r.Version.Author,
		&r.Version.SizeBytes, &r.Version.Sha256,
		&r.Store.ID, &r.Store.LocalStatus, &r.Store.RemoteStatus, &r.Store.InlineSize,
	}
	return scan(append(dest, extra...)...)
}

// ListAssetVersionsJoined returns every version row (inline blobs not loaded)
// joined with its store row and owning asset, ordered by key then version.
func (q *Queries) ListAssetVersionsJoined(ctx context.Context) ([]AssetVersionJoined, error) {
	rows, err := q.db.QueryContext(ctx, `
SELECT `+assetVersionJoinedColumns+`, a.key, `+assetSpaceExpr+`
FROM asset_versions v
JOIN asset_store s ON s.sha256 = v.sha256
JOIN assets a ON a.id = v.asset_id
WHERE a.deleted_at = 0
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
SELECT `+assetVersionJoinedColumns+`, s.inline_blob, a.key, `+assetSpaceExpr+`
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
