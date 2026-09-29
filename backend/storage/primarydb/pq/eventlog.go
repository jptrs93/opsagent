package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// EventMeta is the envelope shared by every append-only entity table: the
// commit that wrote the row, the producer clock, the acting user, and whether
// the row creates, updates, or deletes its entity. Live state is the newest
// row per entity whose EventType is not delete.
type EventMeta struct {
	GlobalSeq int64
	EventTime int64
	Author    int64
	EventType apigen.AuthzVerb
}

func (m EventMeta) deleted() bool { return m.EventType == apigen.AuthzVerb_AUTHZ_VERB_DELETE }

// nextEntityID hands out the next id for a table whose entity ids are never
// reused. Callers run under the store's write lock, so the read and the
// insert that follows cannot interleave with another writer.
func (q *Queries) nextEntityID(ctx context.Context, table, column string) (int64, error) {
	var next int64
	err := q.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(`+column+`), 0) + 1 FROM `+table).Scan(&next)
	return next, err
}
