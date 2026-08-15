package pq

import "context"

func (q *Queries) SeedEventIDFloor(ctx context.Context, floor int64) error {
	if _, err := q.db.ExecContext(ctx,
		`INSERT INTO events (id, ts, author_id, entity_type, entity_id, action, blob)
		 VALUES (?, 0, 0, 0, 0, 0, x'') ON CONFLICT(id) DO NOTHING`, floor); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `DELETE FROM events WHERE id = ?`, floor)
	return err
}
