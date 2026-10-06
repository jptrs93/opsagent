package pq

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// NixStoreResetRow is one repository's newest reset request and the
// envelope of the write that made it. ID is the stream entity id;
// RequestedAt is the clock of the newest request, epoch ms.
type NixStoreResetRow struct {
	ID          uint64
	Repo        string
	RequestedAt int64
	Seq         int64
	EventTime   int64
	Author      int64
	CreatedTime int64
}

func (r NixStoreResetRow) Entity() *apigen.NixStoreReset {
	return &apigen.NixStoreReset{ID: r.ID, Repo: r.Repo}
}

const nixStoreResetColumns = `id, repo, requested_at, seq, event_time, author, created_time`

func scanNixStoreResetRow(row scanner) (NixStoreResetRow, error) {
	var r NixStoreResetRow
	err := row.Scan(&r.ID, &r.Repo, &r.RequestedAt, &r.Seq, &r.EventTime, &r.Author, &r.CreatedTime)
	return r, err
}

// GetNixStoreReset returns the repository's request, or sql.ErrNoRows when
// none was ever made.
func (q *Queries) GetNixStoreReset(ctx context.Context, repo string) (NixStoreResetRow, error) {
	return scanNixStoreResetRow(q.db.QueryRowContext(ctx, `SELECT `+nixStoreResetColumns+` FROM nix_store_resets WHERE repo = ?`, repo))
}

func (q *Queries) ListNixStoreResetRows(ctx context.Context) ([]NixStoreResetRow, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+nixStoreResetColumns+` FROM nix_store_resets ORDER BY repo`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NixStoreResetRow
	for rows.Next() {
		r, err := scanNixStoreResetRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// reduceNixStoreReset records the newest request per repository; the
// request time is the write's clock.
func (q *Queries) reduceNixStoreReset(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, r *apigen.NixStoreReset) error {
	if r == nil {
		return fmt.Errorf("payload has no reset")
	}
	return q.upsert(ctx, meta, `INSERT INTO nix_store_resets (id, repo, requested_at, seq, event_time, author, created_time) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET repo = excluded.repo, requested_at = excluded.requested_at, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, r.Repo, env.EventTime, env.Seq, env.EventTime, env.Author, env.EventTime)
}

func (q *Queries) deleteNixStoreResetRow(ctx context.Context, id uint64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM nix_store_resets WHERE id = ?`, id)
	return err
}
