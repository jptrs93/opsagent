package pq

import (
	"context"
	"database/sql"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func scanSpace(row scanner) (apigen.Space, bool, error) {
	var e apigen.Space
	var eventType int64
	err := row.Scan(&e.ID, &e.Name, &eventType)
	return e, eventType != int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE), err
}

func (q *Queries) ListSpaces(ctx context.Context) ([]apigen.Space, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT space_id, name, event_type FROM space_event_log WHERE `+latestPer("space_event_log", "space_id")+` ORDER BY space_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apigen.Space
	for rows.Next() {
		e, _, err := scanSpace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetSpace returns the live space, or sql.ErrNoRows when it never existed or
// was deleted.
func (q *Queries) GetSpace(ctx context.Context, spaceID int64) (apigen.Space, error) {
	e, live, err := scanSpace(q.db.QueryRowContext(ctx, `SELECT space_id, name, event_type FROM space_event_log WHERE space_id = ? ORDER BY id DESC LIMIT 1`, spaceID))
	if err == nil && !live {
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
