package pq

import "context"

func (q *Queries) GetGlobalSeq(ctx context.Context) (int64, error) {
	var seq int64
	err := q.db.QueryRowContext(ctx, `SELECT value FROM global_seq WHERE id = 1`).Scan(&seq)
	return seq, err
}

func (q *Queries) SetGlobalSeq(ctx context.Context, seq int64) error {
	_, err := q.db.ExecContext(ctx, `UPDATE global_seq SET value = ? WHERE id = 1`, seq)
	return err
}
