package pq

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (q *Queries) ListSpaces(ctx context.Context) ([]apigen.Space, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, name FROM spaces ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apigen.Space
	for rows.Next() {
		var e apigen.Space
		if err := rows.Scan(&e.ID, &e.Name); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetSpace returns the live space, or sql.ErrNoRows when it never existed or
// was deleted.
func (q *Queries) GetSpace(ctx context.Context, spaceID uint64) (apigen.Space, error) {
	var e apigen.Space
	err := q.db.QueryRowContext(ctx, `SELECT id, name FROM spaces WHERE id = ?`, spaceID).Scan(&e.ID, &e.Name)
	return e, err
}

type spaceRow struct {
	rowEnvelope
	Space apigen.Space
}

func (q *Queries) listSpaceRows(ctx context.Context) ([]spaceRow, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, name, seq, event_time, author, created_time FROM spaces ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []spaceRow
	for rows.Next() {
		var r spaceRow
		if err := rows.Scan(&r.Space.ID, &r.Space.Name, &r.Seq, &r.EventTime, &r.Author, &r.CreatedTime); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) reduceSpace(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, s *apigen.Space) error {
	if s == nil {
		return fmt.Errorf("payload has no space")
	}
	return q.upsert(ctx, meta, `INSERT INTO spaces (id, name, seq, event_time, author, created_time) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET name = excluded.name, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, s.Name, env.Seq, env.EventTime, env.Author, env.EventTime)
}

func (q *Queries) deleteSpaceRow(ctx context.Context, id uint64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM spaces WHERE id = ?`, id)
	return err
}
