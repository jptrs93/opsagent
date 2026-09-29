package pq

import (
	"context"
	"database/sql"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const assetDirectoryColumns = `directory_id, space_id, key, parent_id, created_at, author, event_type`

func scanAssetDirectory(row scanner) (apigen.AssetDirectory, error) {
	var e apigen.AssetDirectory
	var createdAt, eventType int64
	err := row.Scan(&e.ID, &e.SpaceID, &e.Key, &e.ParentID, &createdAt, &e.Author, &eventType)
	e.CreatedAt = time.UnixMilli(createdAt)
	e.Deleted = eventType == int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE)
	return e, err
}

const liveAssetDirectories = `FROM asset_directory_event_log WHERE id IN (SELECT MAX(id) FROM asset_directory_event_log GROUP BY directory_id) AND event_type != 3`

func (q *Queries) listAssetDirectories(ctx context.Context, where string, args ...any) ([]apigen.AssetDirectory, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+assetDirectoryColumns+` `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apigen.AssetDirectory
	for rows.Next() {
		e, err := scanAssetDirectory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetAssetDirectoryByID returns the live directory, or sql.ErrNoRows when it
// never existed or was deleted.
func (q *Queries) GetAssetDirectoryByID(ctx context.Context, directoryID int64) (apigen.AssetDirectory, error) {
	e, err := scanAssetDirectory(q.db.QueryRowContext(ctx, `SELECT `+assetDirectoryColumns+` FROM asset_directory_event_log WHERE directory_id = ? ORDER BY id DESC LIMIT 1`, directoryID))
	if err == nil && e.Deleted {
		return apigen.AssetDirectory{}, sql.ErrNoRows
	}
	return e, err
}

func (q *Queries) ListAssetDirectories(ctx context.Context) ([]apigen.AssetDirectory, error) {
	return q.listAssetDirectories(ctx, liveAssetDirectories+` ORDER BY space_id, parent_id, key`)
}

func (q *Queries) ListAssetDirectoriesAtSeq(ctx context.Context, seq int64) ([]apigen.AssetDirectory, error) {
	return q.listAssetDirectories(ctx, `FROM asset_directory_event_log WHERE global_seq = ? ORDER BY id`, seq)
}

func (q *Queries) CountChildAssetDirectories(ctx context.Context, parentID int64) (int64, error) {
	var count int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) `+liveAssetDirectories+` AND parent_id = ?`, parentID).Scan(&count)
	return count, err
}

type CountDirectorySiblingsWithKeyParams struct {
	SpaceID  int64
	ParentID int64
	Key      string
	ID       int64
}

func (q *Queries) CountDirectorySiblingsWithKey(ctx context.Context, arg CountDirectorySiblingsWithKeyParams) (int64, error) {
	var count int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) `+liveAssetDirectories+` AND space_id = ? AND parent_id = ? AND key = ? AND directory_id != ?`,
		arg.SpaceID, arg.ParentID, arg.Key, arg.ID).Scan(&count)
	return count, err
}

func (q *Queries) NextAssetDirectoryID(ctx context.Context) (int64, error) {
	return q.nextEntityID(ctx, "asset_directory_event_log", "directory_id")
}

type AssetDirectoryEventParams struct {
	EventMeta
	DirectoryID int64
	SpaceID     int64
	Key         string
	ParentID    int64
	CreatedAt   int64
}

// AssetDirectoryEvent builds the parameters that carry a directory's current
// document forward into a new event row.
func AssetDirectoryEvent(d apigen.AssetDirectory, meta EventMeta) AssetDirectoryEventParams {
	return AssetDirectoryEventParams{EventMeta: meta, DirectoryID: int64(d.ID), SpaceID: int64(d.SpaceID), Key: d.Key, ParentID: int64(d.ParentID), CreatedAt: d.CreatedAt.UnixMilli()}
}

func (q *Queries) InsertAssetDirectoryEvent(ctx context.Context, arg AssetDirectoryEventParams) (apigen.AssetDirectory, error) {
	return scanAssetDirectory(q.db.QueryRowContext(ctx, `INSERT INTO asset_directory_event_log (global_seq, event_time, author, directory_id, event_type, space_id, key, parent_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING `+assetDirectoryColumns, arg.GlobalSeq, arg.EventTime, arg.Author, arg.DirectoryID, arg.EventType, arg.SpaceID, arg.Key, arg.ParentID, arg.CreatedAt))
}
