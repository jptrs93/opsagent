package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type AssetVersion struct {
	ID        int64
	AssetID   int64
	Version   int64
	CreatedAt int64
	Author    int64
	SizeBytes int64
	Sha256    string
	GlobalSeq int64
}

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

// AssetRow is a live asset identity with its current facets — the latest
// event row of a not-deleted asset.
type AssetRow struct {
	ID               int64
	Key              string
	SpaceID          int64
	AssetDirectoryID int64
	CreatedAt        int64
}

const assetLatestJoin = `JOIN (SELECT asset_id, MAX(version) AS version
	      FROM asset_event_log GROUP BY asset_id) latest
	  ON latest.asset_id = e.asset_id AND latest.version = e.version`

const assetRowSelect = `SELECT e.asset_id, e.key, e.space_id, e.asset_directory_id, e.created_time
	FROM asset_event_log e
	` + assetLatestJoin + `
	WHERE e.event_type != 3`

func scanAssetRow(scan func(dest ...any) error) (AssetRow, error) {
	var r AssetRow
	err := scan(&r.ID, &r.Key, &r.SpaceID, &r.AssetDirectoryID, &r.CreatedAt)
	return r, err
}

func (q *Queries) InsertAssetEvent(ctx context.Context, e *apigen.AssetEvent) error {
	row := q.db.QueryRowContext(ctx, `WITH previous AS (
  SELECT value_version, space_version FROM asset_event_log
  WHERE asset_id = ? ORDER BY version DESC LIMIT 1
)
 INSERT INTO asset_event_log (
  id, global_seq, event_time, created_time, author,
  asset_id,version,value_version, space_version,
  key,asset_directory_id,space_id,size_bytes,sha256,event_type, value_changed, space_changed
) VALUES (NULLIF(?, 0),?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
 ? > COALESCE((SELECT value_version FROM previous),0), ? > COALESCE((SELECT space_version FROM previous),0))
RETURNING id, global_seq, event_time, created_time, author,
  asset_id,version,value_version, space_version,
  key,asset_directory_id,space_id,size_bytes,sha256,event_type`,
		e.AssetID, e.EventID, e.Seq, e.EventTime, e.CreatedTime, e.Author, e.AssetID, e.Version, e.ValueVersion, e.SpaceVersion, e.Value.Fs.Key, e.Value.Fs.DirectoryID, e.Value.SpaceID, e.Value.SizeBytes, e.Value.Sha256, e.EventType, e.ValueVersion, e.SpaceVersion)
	written, err := scanAssetEvent(row)
	if err != nil {
		return err
	}
	*e = written
	return nil
}

type GetAssetInDirectoryByKeyParams struct {
	AssetDirectoryID int64
	Key              string
	SpaceID          int64
}

func (q *Queries) GetAssetInDirectoryByKey(ctx context.Context, arg GetAssetInDirectoryByKeyParams) (AssetRow, error) {
	return scanAssetRow(q.db.QueryRowContext(ctx, assetRowSelect+
		` AND e.asset_directory_id = ? AND e.key = ? AND e.space_id = ?`,
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
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+assetRowSelect+
		` AND e.asset_directory_id = ? AND e.key = ? AND e.asset_id != ? AND e.space_id = ?)`,
		arg.AssetDirectoryID, arg.Key, arg.ID, arg.SpaceID).Scan(&n)
	return n, err
}

func (q *Queries) CountAssetsInDirectory(ctx context.Context, directoryID int64) (int64, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+assetRowSelect+
		` AND e.asset_directory_id = ?)`, directoryID).Scan(&n)
	return n, err
}

func (q *Queries) CountAssetVersionsBySha(ctx context.Context, sha256 string) (int64, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_event_log WHERE sha256 = ? AND value_changed != 0`, sha256).Scan(&n)
	return n, err
}

func (q *Queries) ListAssetIDsBySha(ctx context.Context, sha256 string) ([]int64, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT DISTINCT asset_id FROM asset_event_log WHERE sha256 = ? AND value_changed != 0`, sha256)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AssetVersionJoined is a pinnable content version row joined with its
// content-store row and, where the query says so, the display fields of its
// owning asset (ID, Key, SpaceID).
type AssetVersionJoined struct {
	Version AssetVersion
	Asset   AssetRow
	Store   AssetStoreRef
}

const assetVersionJoinedColumns = `v.id, v.asset_id, v.value_version, v.event_time, v.author, v.size_bytes, v.sha256, v.global_seq,
       s.id, s.local_status, s.remote_status, CAST(LENGTH(s.inline_blob) AS INTEGER)`

const assetVersionRowsFrom = `FROM asset_event_log v
JOIN asset_store s ON s.sha256 = v.sha256`

func scanAssetVersionJoined(scan func(dest ...any) error, r *AssetVersionJoined, extra ...any) error {
	dest := []any{
		&r.Version.ID, &r.Version.AssetID, &r.Version.Version, &r.Version.CreatedAt, &r.Version.Author,
		&r.Version.SizeBytes, &r.Version.Sha256, &r.Version.GlobalSeq,
		&r.Store.ID, &r.Store.LocalStatus, &r.Store.RemoteStatus, &r.Store.InlineSize,
	}
	return scan(append(dest, extra...)...)
}

const assetCurrentIdentityJoin = `JOIN asset_event_log a
  ON a.asset_id = v.asset_id
 AND a.version = (SELECT MAX(version) FROM asset_event_log WHERE asset_id = v.asset_id)`

// GetAssetVersionJoinedByID resolves a pinned content version row id (inline
// blob included) joined with its store row and owning asset.
func (q *Queries) GetAssetVersionJoinedByID(ctx context.Context, assetVersionID int64) (AssetVersionJoined, error) {
	var r AssetVersionJoined
	err := scanAssetVersionJoined(q.db.QueryRowContext(ctx, `
SELECT `+assetVersionJoinedColumns+`, s.inline_blob, a.key, a.space_id
`+assetVersionRowsFrom+`
`+assetCurrentIdentityJoin+`
WHERE v.id = ? AND v.value_changed != 0`, assetVersionID).Scan, &r, &r.Store.InlineBlob, &r.Asset.Key, &r.Asset.SpaceID)
	if err != nil {
		return r, err
	}
	r.Asset.ID = r.Version.AssetID
	return r, nil
}

const listAssetVersionIDsByAssetID = `SELECT id FROM asset_event_log WHERE asset_id = ? AND value_changed != 0 ORDER BY value_version
`

func (q *Queries) ListAssetVersionIDsByAssetID(ctx context.Context, assetID int64) ([]int64, error) {
	rows, err := q.db.QueryContext(ctx, listAssetVersionIDsByAssetID, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		items = append(items, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
