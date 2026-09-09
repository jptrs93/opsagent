package pq

import (
	"context"
)

type AssetStore struct {
	ID           string
	Sha256       string
	SizeBytes    int64
	InlineBlob   []byte
	LocalStatus  int64
	RemoteStatus int64
	CreatedAt    int64
}

const completeAssetStoreRow = `UPDATE asset_store SET sha256 = ?, local_status = ?, remote_status = ? WHERE id = ?
`

type CompleteAssetStoreRowParams struct {
	Sha256       string
	LocalStatus  int64
	RemoteStatus int64
	ID           string
}

func (q *Queries) CompleteAssetStoreRow(ctx context.Context, arg CompleteAssetStoreRowParams) error {
	_, err := q.db.ExecContext(ctx, completeAssetStoreRow,
		arg.Sha256,
		arg.LocalStatus,
		arg.RemoteStatus,
		arg.ID,
	)
	return err
}

const deleteAssetStoreRow = `DELETE FROM asset_store WHERE id = ?
`

func (q *Queries) DeleteAssetStoreRow(ctx context.Context, id string) error {
	_, err := q.db.ExecContext(ctx, deleteAssetStoreRow, id)
	return err
}

const getAssetStoreRowByID = `SELECT id, sha256, size_bytes, inline_blob, local_status, remote_status, created_at
FROM asset_store
WHERE id = ?
`

func (q *Queries) GetAssetStoreRowByID(ctx context.Context, id string) (AssetStore, error) {
	row := q.db.QueryRowContext(ctx, getAssetStoreRowByID, id)
	var i AssetStore
	err := row.Scan(
		&i.ID,
		&i.Sha256,
		&i.SizeBytes,
		&i.InlineBlob,
		&i.LocalStatus,
		&i.RemoteStatus,
		&i.CreatedAt,
	)
	return i, err
}

const getAssetStoreRowBySha = `SELECT id, sha256, size_bytes, inline_blob, local_status, remote_status, created_at
FROM asset_store
WHERE sha256 = ? AND sha256 != ''
`

func (q *Queries) GetAssetStoreRowBySha(ctx context.Context, sha256 string) (AssetStore, error) {
	row := q.db.QueryRowContext(ctx, getAssetStoreRowBySha, sha256)
	var i AssetStore
	err := row.Scan(
		&i.ID,
		&i.Sha256,
		&i.SizeBytes,
		&i.InlineBlob,
		&i.LocalStatus,
		&i.RemoteStatus,
		&i.CreatedAt,
	)
	return i, err
}

const insertAssetStoreRow = `INSERT INTO asset_store (id, sha256, size_bytes, inline_blob, local_status, remote_status, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id, sha256, size_bytes, inline_blob, local_status, remote_status, created_at
`

type InsertAssetStoreRowParams struct {
	ID           string
	Sha256       string
	SizeBytes    int64
	InlineBlob   []byte
	LocalStatus  int64
	RemoteStatus int64
	CreatedAt    int64
}

func (q *Queries) InsertAssetStoreRow(ctx context.Context, arg InsertAssetStoreRowParams) (AssetStore, error) {
	row := q.db.QueryRowContext(ctx, insertAssetStoreRow,
		arg.ID,
		arg.Sha256,
		arg.SizeBytes,
		arg.InlineBlob,
		arg.LocalStatus,
		arg.RemoteStatus,
		arg.CreatedAt,
	)
	var i AssetStore
	err := row.Scan(
		&i.ID,
		&i.Sha256,
		&i.SizeBytes,
		&i.InlineBlob,
		&i.LocalStatus,
		&i.RemoteStatus,
		&i.CreatedAt,
	)
	return i, err
}

const listAssetStoreRowMetas = `SELECT id, sha256, size_bytes, CAST(LENGTH(inline_blob) AS INTEGER) AS inline_size, local_status, remote_status, created_at
FROM asset_store
`

type ListAssetStoreRowMetasRow struct {
	ID           string
	Sha256       string
	SizeBytes    int64
	InlineSize   int64
	LocalStatus  int64
	RemoteStatus int64
	CreatedAt    int64
}

func (q *Queries) ListAssetStoreRowMetas(ctx context.Context) ([]ListAssetStoreRowMetasRow, error) {
	rows, err := q.db.QueryContext(ctx, listAssetStoreRowMetas)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ListAssetStoreRowMetasRow
	for rows.Next() {
		var i ListAssetStoreRowMetasRow
		if err := rows.Scan(
			&i.ID,
			&i.Sha256,
			&i.SizeBytes,
			&i.InlineSize,
			&i.LocalStatus,
			&i.RemoteStatus,
			&i.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const listUnreferencedAssetStoreRows = `SELECT s.id, s.sha256, s.size_bytes, CAST(LENGTH(s.inline_blob) AS INTEGER) AS inline_size, s.local_status, s.remote_status, s.created_at
FROM asset_store s
WHERE s.created_at < ? AND NOT EXISTS (SELECT 1 FROM asset_event_log v WHERE v.sha256 = s.sha256 AND v.value_changed != 0)
`

type ListUnreferencedAssetStoreRowsRow struct {
	ID           string
	Sha256       string
	SizeBytes    int64
	InlineSize   int64
	LocalStatus  int64
	RemoteStatus int64
	CreatedAt    int64
}

func (q *Queries) ListUnreferencedAssetStoreRows(ctx context.Context, createdAt int64) ([]ListUnreferencedAssetStoreRowsRow, error) {
	rows, err := q.db.QueryContext(ctx, listUnreferencedAssetStoreRows, createdAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ListUnreferencedAssetStoreRowsRow
	for rows.Next() {
		var i ListUnreferencedAssetStoreRowsRow
		if err := rows.Scan(
			&i.ID,
			&i.Sha256,
			&i.SizeBytes,
			&i.InlineSize,
			&i.LocalStatus,
			&i.RemoteStatus,
			&i.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const setAssetStoreLocalStatus = `UPDATE asset_store SET local_status = ? WHERE id = ?
`

type SetAssetStoreLocalStatusParams struct {
	LocalStatus int64
	ID          string
}

func (q *Queries) SetAssetStoreLocalStatus(ctx context.Context, arg SetAssetStoreLocalStatusParams) error {
	_, err := q.db.ExecContext(ctx, setAssetStoreLocalStatus, arg.LocalStatus, arg.ID)
	return err
}

const setAssetStoreRemoteStatus = `UPDATE asset_store SET remote_status = ? WHERE id = ?
`

type SetAssetStoreRemoteStatusParams struct {
	RemoteStatus int64
	ID           string
}

func (q *Queries) SetAssetStoreRemoteStatus(ctx context.Context, arg SetAssetStoreRemoteStatusParams) error {
	_, err := q.db.ExecContext(ctx, setAssetStoreRemoteStatus, arg.RemoteStatus, arg.ID)
	return err
}
