package pq

import (
	"context"
	"database/sql"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const spaceColumns = `space_id, name, event_type`

func scanSpace(row scanner) (apigen.Space, error) {
	var e apigen.Space
	var eventType int64
	err := row.Scan(&e.ID, &e.Name, &eventType)
	e.Deleted = eventType == int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE)
	return e, err
}

func (q *Queries) listSpaces(ctx context.Context, where string, args ...any) ([]apigen.Space, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+spaceColumns+` FROM space_event_log `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apigen.Space
	for rows.Next() {
		e, err := scanSpace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) ListSpaces(ctx context.Context) ([]apigen.Space, error) {
	return q.listSpaces(ctx, `WHERE id IN (SELECT MAX(id) FROM space_event_log GROUP BY space_id) AND event_type != 3 ORDER BY space_id`)
}

func (q *Queries) ListSpacesAtSeq(ctx context.Context, seq int64) ([]apigen.Space, error) {
	return q.listSpaces(ctx, `WHERE global_seq = ? ORDER BY id`, seq)
}

// GetSpace returns the live space, or sql.ErrNoRows when it never existed or
// was deleted.
func (q *Queries) GetSpace(ctx context.Context, spaceID int64) (apigen.Space, error) {
	e, err := scanSpace(q.db.QueryRowContext(ctx, `SELECT `+spaceColumns+` FROM space_event_log WHERE space_id = ? ORDER BY id DESC LIMIT 1`, spaceID))
	if err == nil && e.Deleted {
		return apigen.Space{}, sql.ErrNoRows
	}
	return e, err
}

func (q *Queries) NextSpaceID(ctx context.Context) (int64, error) {
	return q.nextEntityID(ctx, "space_event_log", "space_id")
}

type SpaceEventParams struct {
	EventMeta
	SpaceID int64
	Name    string
}

func (q *Queries) InsertSpaceEvent(ctx context.Context, arg SpaceEventParams) error {
	_, err := q.db.ExecContext(ctx, `INSERT INTO space_event_log (global_seq, event_time, author, space_id, event_type, name) VALUES (?, ?, ?, ?, ?, ?)`,
		arg.GlobalSeq, arg.EventTime, arg.Author, arg.SpaceID, arg.EventType, arg.Name)
	return err
}
