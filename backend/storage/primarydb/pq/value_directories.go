package pq

import (
	"context"
	"database/sql"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const valueDirectoryColumns = `directory_id, space_id, name, parent_id, created_at, author, event_type`

func scanValueDirectory(row scanner) (*apigen.ValueDirectory, error) {
	e := &apigen.ValueDirectory{}
	var createdAt, eventType int64
	if err := row.Scan(&e.ID, &e.SpaceID, &e.Name, &e.ParentID, &createdAt, &e.Author, &eventType); err != nil {
		return nil, err
	}
	e.CreatedAt = time.UnixMilli(createdAt)
	e.Deleted = eventType == int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE)
	return e, nil
}

const liveValueDirectories = `FROM value_directory_event_log WHERE id IN (SELECT MAX(id) FROM value_directory_event_log GROUP BY directory_id) AND event_type != 3`

func (q *Queries) listValueDirectories(ctx context.Context, where string, args ...any) ([]*apigen.ValueDirectory, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+valueDirectoryColumns+` `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.ValueDirectory
	for rows.Next() {
		e, err := scanValueDirectory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetValueDirectoryByID returns the live directory, or sql.ErrNoRows when it
// never existed or was deleted.
func (q *Queries) GetValueDirectoryByID(ctx context.Context, directoryID int64) (*apigen.ValueDirectory, error) {
	e, err := scanValueDirectory(q.db.QueryRowContext(ctx, `SELECT `+valueDirectoryColumns+` FROM value_directory_event_log WHERE directory_id = ? ORDER BY id DESC LIMIT 1`, directoryID))
	if err == nil && e.Deleted {
		return nil, sql.ErrNoRows
	}
	return e, err
}

func (q *Queries) ListValueDirectories(ctx context.Context) ([]*apigen.ValueDirectory, error) {
	return q.listValueDirectories(ctx, liveValueDirectories+` ORDER BY space_id, parent_id, name`)
}

func (q *Queries) ListValueDirectoriesAtSeq(ctx context.Context, seq int64) ([]*apigen.ValueDirectory, error) {
	return q.listValueDirectories(ctx, `FROM value_directory_event_log WHERE global_seq = ? ORDER BY id`, seq)
}

func (q *Queries) CountChildValueDirectories(ctx context.Context, parentID int64) (int64, error) {
	var count int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) `+liveValueDirectories+` AND parent_id = ?`, parentID).Scan(&count)
	return count, err
}

type CountValueDirectorySiblingsWithNameParams struct {
	SpaceID  int64
	ParentID int64
	Name     string
	ID       int64
}

func (q *Queries) CountValueDirectorySiblingsWithName(ctx context.Context, arg CountValueDirectorySiblingsWithNameParams) (int64, error) {
	var count int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) `+liveValueDirectories+` AND space_id = ? AND parent_id = ? AND name = ? AND directory_id != ?`,
		arg.SpaceID, arg.ParentID, arg.Name, arg.ID).Scan(&count)
	return count, err
}

func (q *Queries) NextValueDirectoryID(ctx context.Context) (int64, error) {
	return q.nextEntityID(ctx, "value_directory_event_log", "directory_id")
}

type ValueDirectoryEventParams struct {
	EventMeta
	DirectoryID int64
	SpaceID     int64
	Name        string
	ParentID    int64
	CreatedAt   int64
}

// ValueDirectoryEvent builds the parameters that carry a directory's current
// document forward into a new event row.
func ValueDirectoryEvent(d *apigen.ValueDirectory, meta EventMeta) ValueDirectoryEventParams {
	return ValueDirectoryEventParams{EventMeta: meta, DirectoryID: int64(d.ID), SpaceID: int64(d.SpaceID), Name: d.Name, ParentID: int64(d.ParentID), CreatedAt: d.CreatedAt.UnixMilli()}
}

func (q *Queries) InsertValueDirectoryEvent(ctx context.Context, arg ValueDirectoryEventParams) (*apigen.ValueDirectory, error) {
	return scanValueDirectory(q.db.QueryRowContext(ctx, `INSERT INTO value_directory_event_log (global_seq, event_time, author, directory_id, event_type, space_id, name, parent_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING `+valueDirectoryColumns, arg.GlobalSeq, arg.EventTime, arg.Author, arg.DirectoryID, arg.EventType, arg.SpaceID, arg.Name, arg.ParentID, arg.CreatedAt))
}
