package pq

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
)

func (q *Queries) InsertWriteEvent(ctx context.Context, u *apigen.CoreWriteUpdate) error {
	if _, err := q.db.ExecContext(ctx, `INSERT INTO write_events (seq, time, actor) VALUES (?, ?, ?)`, u.Seq, u.Time, u.Actor); err != nil {
		return err
	}
	for i, m := range u.Mutations {
		var payload []byte
		if e := m.Entity(); e != nil {
			payload = e.Encode()
		}
		if _, err := q.db.ExecContext(ctx, `INSERT INTO write_event_mutations (seq, idx, entity_type, entity_id, op, payload) VALUES (?, ?, ?, ?, ?, ?)`,
			u.Seq, i, int64(m.Type()), m.EntityID(), int64(m.Kind()), payload); err != nil {
			return err
		}
	}
	return nil
}

func (q *Queries) LatestWriteEventSeq(ctx context.Context) (int64, error) {
	var seq sql.NullInt64
	if err := q.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM write_events`).Scan(&seq); err != nil {
		return 0, err
	}
	if !seq.Valid {
		return -1, nil
	}
	return seq.Int64, nil
}

func (q *Queries) WriteEventsInRange(ctx context.Context, after, upTo int64) ([]*apigen.CoreWriteUpdate, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT e.seq, e.time, e.actor, m.entity_type, m.entity_id, m.op, m.payload
FROM write_events e JOIN write_event_mutations m ON m.seq = e.seq
WHERE e.seq > ? AND e.seq <= ?
ORDER BY e.seq, m.idx`, after, upTo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.CoreWriteUpdate
	for rows.Next() {
		var seq, eventTime, entityType, entityID, op int64
		var actor int32
		var payload []byte
		if err := rows.Scan(&seq, &eventTime, &actor, &entityType, &entityID, &op, &payload); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].Seq != seq {
			out = append(out, &apigen.CoreWriteUpdate{Seq: seq, Time: eventTime, Actor: actor})
		}
		m, err := decodeWriteEventMutation(apigen.CoreEntityType(entityType), entityID, apigen.AuthzVerb(op), payload)
		if err != nil {
			return nil, fmt.Errorf("write event %d: %w", seq, err)
		}
		out[len(out)-1].Mutations = append(out[len(out)-1].Mutations, m)
	}
	return out, rows.Err()
}

func decodeWriteEventMutation(t apigen.CoreEntityType, id int64, op apigen.AuthzVerb, payload []byte) (*apigen.CoreMutation, error) {
	if op == apigen.AuthzVerb_AUTHZ_VERB_DELETE {
		return &apigen.CoreMutation{Delete: &apigen.DeleteMutation{EntityType: t, EntityID: id}}, nil
	}
	entity, err := apigen.DecodeCoreEntity(payload)
	if err != nil {
		return nil, err
	}
	if op == apigen.AuthzVerb_AUTHZ_VERB_CREATE {
		return &apigen.CoreMutation{Create: &apigen.CreateMutation{EntityType: t, EntityID: id, Entity: entity}}, nil
	}
	return &apigen.CoreMutation{Update: &apigen.UpdateMutation{EntityType: t, EntityID: id, Entity: entity}}, nil
}

// backfillWriteEvents brings the write log level with the entity tables at
// startup: a database from before the log, or one written by a build without
// it, has committed sequences the log never saw. The missing range is read
// back from the entity tables the way a replay would and appended, genesis
// rows at seq 0 included when the log is empty.
func backfillWriteEvents(db *sql.DB) {
	ctx := logu.AddTag(context.Background(), "Store")
	q := &Queries{db: &conn{DBTX: db, root: db}}
	logged, err := q.LatestWriteEventSeq(ctx)
	if err != nil {
		panic(fmt.Errorf("write event backfill: %w", err))
	}
	seq, err := q.GetGlobalSeq(ctx)
	if err != nil {
		panic(fmt.Errorf("write event backfill: %w", err))
	}
	if logged >= seq {
		return
	}
	ms, err := q.MutationsInRange(ctx, logged, seq)
	if err != nil {
		panic(fmt.Errorf("write event backfill: %w", err))
	}
	events := Events(ms)
	err = q.Tx(ctx, func(tx *Queries) error {
		for _, e := range events {
			if err := tx.InsertWriteEvent(ctx, e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		panic(fmt.Errorf("write event backfill: %w", err))
	}
	if len(events) > 0 {
		slog.InfoContext(ctx, fmt.Sprintf("write event log backfilled with %d events through seq %d", len(events), seq))
	}
}
