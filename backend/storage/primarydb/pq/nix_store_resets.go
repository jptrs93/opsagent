package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (q *Queries) UpsertNixStoreReset(ctx context.Context, repo string, requestedAt int64) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO nix_store_resets (repo, requested_at) VALUES (?, ?)
		ON CONFLICT(repo) DO UPDATE SET requested_at = excluded.requested_at`, repo, requestedAt)
	return err
}

func (q *Queries) ListNixStoreResets(ctx context.Context) ([]*apigen.NixStoreReset, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT repo, requested_at FROM nix_store_resets ORDER BY repo`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.NixStoreReset
	for rows.Next() {
		item := &apigen.NixStoreReset{}
		if err := rows.Scan(&item.Repo, &item.RequestedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
