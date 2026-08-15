package pq

import "context"

func (q *Queries) MaxLegacyVersionRowID(ctx context.Context) (int64, error) {
	row := q.db.QueryRowContext(ctx, `SELECT MAX(
		(SELECT COALESCE(MAX(id), 0) FROM config_versions),
		(SELECT COALESCE(MAX(id), 0) FROM secret_versions),
		(SELECT COALESCE(MAX(id), 0) FROM asset_versions))`)
	var max int64
	err := row.Scan(&max)
	return max, err
}

func (q *Queries) SetEventSeqFloor(ctx context.Context, floor int64) error {
	if _, err := q.db.ExecContext(ctx,
		`UPDATE sqlite_sequence SET seq = ? WHERE name = 'events' AND seq < ?`, floor, floor); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx,
		`INSERT INTO sqlite_sequence (name, seq)
		 SELECT 'events', ? WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'events')`, floor)
	return err
}
