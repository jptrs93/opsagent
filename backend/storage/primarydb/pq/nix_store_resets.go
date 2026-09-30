package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type NixStoreResetEventParams struct {
	EventMeta
	Repo        string
	RequestedAt int64
}

// NixStoreResetRow is one event row; the newest row per repository is the
// live request. EntityID is the id of the repository's first row.
type NixStoreResetRow struct {
	EventMeta
	ID          int64
	EntityID    int64
	Repo        string
	RequestedAt int64
}

const nixStoreResetColumns = `id, global_seq, event_time, author, event_type, repo, requested_at,
       (SELECT MIN(p.id) FROM nix_store_reset_event_log p WHERE p.repo = nix_store_reset_event_log.repo)`

func scanNixStoreResetRow(row scanner) (NixStoreResetRow, error) {
	var r NixStoreResetRow
	err := row.Scan(&r.ID, &r.GlobalSeq, &r.EventTime, &r.Author, &r.EventType, &r.Repo, &r.RequestedAt, &r.EntityID)
	return r, err
}

func (q *Queries) InsertNixStoreResetEvent(ctx context.Context, arg NixStoreResetEventParams) (NixStoreResetRow, error) {
	result, err := q.db.ExecContext(ctx, `INSERT INTO nix_store_reset_event_log (global_seq, event_time, author, repo, event_type, requested_at) VALUES (?, ?, ?, ?, ?, ?)`,
		arg.GlobalSeq, arg.EventTime, arg.Author, arg.Repo, arg.EventType, arg.RequestedAt)
	if err != nil {
		return NixStoreResetRow{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return NixStoreResetRow{}, err
	}
	return scanNixStoreResetRow(q.db.QueryRowContext(ctx, `SELECT `+nixStoreResetColumns+` FROM nix_store_reset_event_log WHERE id = ?`, id))
}

func (q *Queries) NixStoreResetExists(ctx context.Context, repo string) (bool, error) {
	var count int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM nix_store_reset_event_log WHERE repo = ?`, repo).Scan(&count)
	return count > 0, err
}

// ListNixStoreResets returns the newest request per repository.
func (q *Queries) ListNixStoreResets(ctx context.Context) ([]*apigen.NixStoreReset, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT repo, requested_at FROM nix_store_reset_event_log
WHERE id IN (SELECT MAX(id) FROM nix_store_reset_event_log GROUP BY repo) ORDER BY repo`)
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
