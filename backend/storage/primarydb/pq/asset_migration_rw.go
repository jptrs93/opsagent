package pq

import (
	"context"
)

type AssetMigration struct {
	ID                 int64
	OldConfigVersionID int64
	NewConfigVersionID int64
	Status             string
	LastError          string
	CreatedAt          int64
	StartedAt          int64
	LastAttemptAt      int64
	FinishedAt         int64
}

const finishAssetMigration = `UPDATE asset_migrations
SET status = 'finished', last_error = '', finished_at = ?
WHERE id = ?
RETURNING id, old_config_version_id, new_config_version_id, status, last_error,
          created_at, started_at, last_attempt_at, finished_at
`

type FinishAssetMigrationParams struct {
	FinishedAt int64
	ID         int64
}

func (q *Queries) FinishAssetMigration(ctx context.Context, arg FinishAssetMigrationParams) (AssetMigration, error) {
	row := q.db.QueryRowContext(ctx, finishAssetMigration, arg.FinishedAt, arg.ID)
	var i AssetMigration
	err := row.Scan(
		&i.ID,
		&i.OldConfigVersionID,
		&i.NewConfigVersionID,
		&i.Status,
		&i.LastError,
		&i.CreatedAt,
		&i.StartedAt,
		&i.LastAttemptAt,
		&i.FinishedAt,
	)
	return i, err
}

const getUnfinishedAssetMigration = `SELECT id, old_config_version_id, new_config_version_id, status, last_error,
       created_at, started_at, last_attempt_at, finished_at
FROM asset_migrations
WHERE status != 'finished'
ORDER BY id
LIMIT 1
`

func (q *Queries) GetUnfinishedAssetMigration(ctx context.Context) (AssetMigration, error) {
	row := q.db.QueryRowContext(ctx, getUnfinishedAssetMigration)
	var i AssetMigration
	err := row.Scan(
		&i.ID,
		&i.OldConfigVersionID,
		&i.NewConfigVersionID,
		&i.Status,
		&i.LastError,
		&i.CreatedAt,
		&i.StartedAt,
		&i.LastAttemptAt,
		&i.FinishedAt,
	)
	return i, err
}

const insertAssetMigration = `INSERT INTO asset_migrations (
    old_config_version_id, new_config_version_id, status, created_at
) VALUES (?, ?, 'pending', ?)
RETURNING id, old_config_version_id, new_config_version_id, status, last_error,
          created_at, started_at, last_attempt_at, finished_at
`

type InsertAssetMigrationParams struct {
	OldConfigVersionID int64
	NewConfigVersionID int64
	CreatedAt          int64
}

func (q *Queries) InsertAssetMigration(ctx context.Context, arg InsertAssetMigrationParams) (AssetMigration, error) {
	row := q.db.QueryRowContext(ctx, insertAssetMigration, arg.OldConfigVersionID, arg.NewConfigVersionID, arg.CreatedAt)
	var i AssetMigration
	err := row.Scan(
		&i.ID,
		&i.OldConfigVersionID,
		&i.NewConfigVersionID,
		&i.Status,
		&i.LastError,
		&i.CreatedAt,
		&i.StartedAt,
		&i.LastAttemptAt,
		&i.FinishedAt,
	)
	return i, err
}

const recordAssetMigrationError = `UPDATE asset_migrations
SET status = 'running', last_attempt_at = ?, last_error = ?
WHERE id = ?
RETURNING id, old_config_version_id, new_config_version_id, status, last_error,
          created_at, started_at, last_attempt_at, finished_at
`

type RecordAssetMigrationErrorParams struct {
	LastAttemptAt int64
	LastError     string
	ID            int64
}

func (q *Queries) RecordAssetMigrationError(ctx context.Context, arg RecordAssetMigrationErrorParams) (AssetMigration, error) {
	row := q.db.QueryRowContext(ctx, recordAssetMigrationError, arg.LastAttemptAt, arg.LastError, arg.ID)
	var i AssetMigration
	err := row.Scan(
		&i.ID,
		&i.OldConfigVersionID,
		&i.NewConfigVersionID,
		&i.Status,
		&i.LastError,
		&i.CreatedAt,
		&i.StartedAt,
		&i.LastAttemptAt,
		&i.FinishedAt,
	)
	return i, err
}

const startAssetMigration = `UPDATE asset_migrations
SET status = 'running', started_at = CASE WHEN started_at = 0 THEN ? ELSE started_at END,
    last_attempt_at = ?, last_error = ''
WHERE id = ?
RETURNING id, old_config_version_id, new_config_version_id, status, last_error,
          created_at, started_at, last_attempt_at, finished_at
`

type StartAssetMigrationParams struct {
	StartedAt     int64
	LastAttemptAt int64
	ID            int64
}

func (q *Queries) StartAssetMigration(ctx context.Context, arg StartAssetMigrationParams) (AssetMigration, error) {
	row := q.db.QueryRowContext(ctx, startAssetMigration, arg.StartedAt, arg.LastAttemptAt, arg.ID)
	var i AssetMigration
	err := row.Scan(
		&i.ID,
		&i.OldConfigVersionID,
		&i.NewConfigVersionID,
		&i.Status,
		&i.LastError,
		&i.CreatedAt,
		&i.StartedAt,
		&i.LastAttemptAt,
		&i.FinishedAt,
	)
	return i, err
}
