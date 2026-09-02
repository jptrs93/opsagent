package pq

import (
	"context"
	"database/sql"
)

type AssetEvent struct {
	ID               int64
	GlobalSeq        int64
	EventTime        int64
	CreatedTime      int64
	Author           int64
	AssetID          int64
	Version          int64
	ValueVersion     int64
	SpaceVersion     int64
	Key              string
	AssetDirectoryID int64
	SpaceID          int64
	SizeBytes        sql.NullInt64
	Sha256           sql.NullString
	EventType        int64
}

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

const assetEventColumns = `e.id, e.global_seq, e.event_time, e.created_time, e.author,
	e.asset_id, e.version, e.value_version, e.space_version,
	e.key, e.asset_directory_id, e.space_id, e.size_bytes, e.sha256, e.event_type`

func scanAssetEvent(scan func(dest ...any) error) (AssetEvent, error) {
	var e AssetEvent
	err := scan(&e.ID, &e.GlobalSeq, &e.EventTime, &e.CreatedTime, &e.Author,
		&e.AssetID, &e.Version, &e.ValueVersion, &e.SpaceVersion,
		&e.Key, &e.AssetDirectoryID, &e.SpaceID, &e.SizeBytes, &e.Sha256, &e.EventType)
	return e, err
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

func (q *Queries) NextAssetID(ctx context.Context) (int64, error) {
	var id int64
	err := q.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(asset_id), 0) + 1 FROM asset_event_log`).Scan(&id)
	return id, err
}

func (q *Queries) GetLatestAssetEvent(ctx context.Context, assetID int64) (AssetEvent, error) {
	return scanAssetEvent(q.db.QueryRowContext(ctx, `
		SELECT `+assetEventColumns+`
		FROM asset_event_log e
		WHERE e.asset_id = ?
		ORDER BY e.version DESC LIMIT 1`, assetID).Scan)
}

func (q *Queries) InsertAssetEvent(ctx context.Context, e AssetEvent) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO asset_event_log (
			global_seq, event_time, created_time, author, asset_id, version,
			value_version, space_version, key, asset_directory_id, space_id,
			size_bytes, sha256, event_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.GlobalSeq, e.EventTime, e.CreatedTime, e.Author, e.AssetID, e.Version,
		e.ValueVersion, e.SpaceVersion, e.Key, e.AssetDirectoryID, e.SpaceID,
		e.SizeBytes, e.Sha256, e.EventType)
	return err
}

func (q *Queries) listAssetEvents(ctx context.Context, query string, args ...any) ([]AssetEvent, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssetEvent{}
	for rows.Next() {
		e, err := scanAssetEvent(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) ListAssetEvents(ctx context.Context, assetID int64) ([]AssetEvent, error) {
	return q.listAssetEvents(ctx, `
		SELECT `+assetEventColumns+`
		FROM asset_event_log e
		WHERE e.asset_id = ?
		ORDER BY e.version`, assetID)
}

func (q *Queries) ListAllAssetEvents(ctx context.Context) ([]AssetEvent, error) {
	return q.listAssetEvents(ctx, `
		SELECT `+assetEventColumns+`
		FROM asset_event_log e
		ORDER BY e.asset_id, e.version`)
}

func (q *Queries) ListAssetRows(ctx context.Context) ([]AssetRow, error) {
	rows, err := q.db.QueryContext(ctx, assetRowSelect+` ORDER BY e.key`)
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
	return scanAssetRow(q.db.QueryRowContext(ctx, assetRowSelect+` AND e.asset_id = ?`, id).Scan)
}

type GetAssetInDirectoryByKeyParams struct {
	SpaceID          int64
	AssetDirectoryID int64
	Key              string
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
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_event_log WHERE sha256 = ?`, sha256).Scan(&n)
	return n, err
}

func (q *Queries) ListAssetIDsBySha(ctx context.Context, sha256 string) ([]int64, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT DISTINCT asset_id FROM asset_event_log WHERE sha256 = ?`, sha256)
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

// ListAssetVersionsJoined returns every content version row (inline blobs not
// loaded) of live assets joined with its store row and owning asset's current
// identity, ordered by key then version.
func (q *Queries) ListAssetVersionsJoined(ctx context.Context) ([]AssetVersionJoined, error) {
	rows, err := q.db.QueryContext(ctx, `
SELECT `+assetVersionJoinedColumns+`, a.key, a.space_id
`+assetVersionRowsFrom+`
`+assetCurrentIdentityJoin+`
WHERE a.event_type != 3
ORDER BY a.key, v.value_version`)
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
WHERE v.id = ? AND v.sha256 IS NOT NULL`, assetVersionID).Scan, &r, &r.Store.InlineBlob, &r.Asset.Key, &r.Asset.SpaceID)
	if err != nil {
		return r, err
	}
	r.Asset.ID = r.Version.AssetID
	return r, nil
}

// ListAssetVersionsOfAsset returns every content version of one asset (inline
// blobs included), oldest first.
func (q *Queries) ListAssetVersionsOfAsset(ctx context.Context, assetID int64) ([]AssetVersionJoined, error) {
	rows, err := q.db.QueryContext(ctx, `
SELECT `+assetVersionJoinedColumns+`, s.inline_blob
`+assetVersionRowsFrom+`
WHERE v.asset_id = ? AND v.sha256 IS NOT NULL
ORDER BY v.value_version ASC`, assetID)
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

// GetAssetVersionJoinedByNumber resolves one content version of an asset by
// number (inline blob included), or the latest when version is 0.
func (q *Queries) GetAssetVersionJoinedByNumber(ctx context.Context, assetID, version int64) (AssetVersionJoined, error) {
	query := `
SELECT ` + assetVersionJoinedColumns + `, s.inline_blob
` + assetVersionRowsFrom + `
WHERE v.asset_id = ? AND v.sha256 IS NOT NULL AND v.value_version = ?`
	args := []any{assetID, version}
	if version == 0 {
		query = `
SELECT ` + assetVersionJoinedColumns + `, s.inline_blob
` + assetVersionRowsFrom + `
WHERE v.asset_id = ? AND v.sha256 IS NOT NULL
ORDER BY v.value_version DESC
LIMIT 1`
		args = []any{assetID}
	}
	var r AssetVersionJoined
	err := scanAssetVersionJoined(q.db.QueryRowContext(ctx, query, args...).Scan, &r, &r.Store.InlineBlob)
	return r, err
}
