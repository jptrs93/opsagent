package pq

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

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

// WriteEventsInRange returns every logged event with after < seq <= upTo,
// mutations in publication order. after < 0 includes the genesis events at
// seq 0.
func (q *Queries) WriteEventsInRange(ctx context.Context, after, upTo int64) ([]*apigen.CoreWriteUpdate, error) {
	return q.writeEventsInRangeOfTypes(ctx, after, upTo, nil)
}

func (q *Queries) writeEventsInRangeOfTypes(ctx context.Context, after, upTo int64, types []apigen.CoreEntityType) ([]*apigen.CoreWriteUpdate, error) {
	args := []any{after, upTo}
	filter := ""
	if len(types) > 0 {
		marks := make([]string, 0, len(types))
		for _, t := range types {
			marks = append(marks, "?")
			args = append(args, int64(t))
		}
		filter = ` AND m.entity_type IN (` + strings.Join(marks, ", ") + `)`
	}
	rows, err := q.db.QueryContext(ctx, `SELECT e.seq, e.time, e.actor, m.entity_type, m.entity_id, m.op, m.payload
FROM write_events e JOIN write_event_mutations m ON m.seq = e.seq
WHERE e.seq > ? AND e.seq <= ?`+filter+`
ORDER BY e.seq, m.idx`, args...)
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
	StampEntityID(entity, id)
	if op == apigen.AuthzVerb_AUTHZ_VERB_CREATE {
		return &apigen.CoreMutation{Create: &apigen.CreateMutation{EntityType: t, EntityID: id, Entity: entity}}, nil
	}
	return &apigen.CoreMutation{Update: &apigen.UpdateMutation{EntityType: t, EntityID: id, Entity: entity}}, nil
}

// LatestMutation returns an entity's newest logged mutation, a delete
// included, or sql.ErrNoRows when the log never saw the entity.
func (q *Queries) LatestMutation(ctx context.Context, t apigen.CoreEntityType, id int64) (*apigen.CoreMutation, error) {
	var op int64
	var payload []byte
	if err := q.db.QueryRowContext(ctx, `SELECT op, payload FROM write_event_mutations WHERE entity_type = ? AND entity_id = ? ORDER BY seq DESC, idx DESC LIMIT 1`,
		int64(t), id).Scan(&op, &payload); err != nil {
		return nil, err
	}
	return decodeWriteEventMutation(t, id, apigen.AuthzVerb(op), payload)
}

// requireCompleteWriteLog refuses a database whose write log stops short of
// its global seq. The log is the only source the materialised tables are
// built from, and only a start on v0.0.614 or later fills it, so a database
// that skipped that release has to step through it first. It runs before
// anything else touches the file, so a refused database goes back to the
// release it came from unchanged.
func requireCompleteWriteLog(db *sql.DB) {
	ctx := context.Background()
	q := &Queries{db: &conn{DBTX: db, root: db}}
	tables, err := q.existingTables(ctx, []string{"global_seq", "write_events"})
	if err != nil {
		panic(fmt.Errorf("write log: %w", err))
	}
	if !slices.Contains(tables, "global_seq") {
		return
	}
	seq, err := q.GetGlobalSeq(ctx)
	if err != nil {
		panic(fmt.Errorf("write log: %w", err))
	}
	logged := int64(-1)
	if slices.Contains(tables, "write_events") {
		if logged, err = q.LatestWriteEventSeq(ctx); err != nil {
			panic(fmt.Errorf("write log: %w", err))
		}
	}
	if seq > 0 && logged < seq {
		panic(fmt.Sprintf("the write log ends at seq %d but the database is at seq %d: start this database on v0.0.614 once before upgrading", logged, seq))
	}
}

// seedWriteLogGenesis writes seq 0 of a fresh database: the two spaces every
// cluster starts with, materialised and logged like every later write, at
// the time the log was born.
func seedWriteLogGenesis(db *sql.DB) {
	ctx := context.Background()
	q := &Queries{db: &conn{DBTX: db, root: db}}
	logged, err := q.LatestWriteEventSeq(ctx)
	if err != nil {
		panic(fmt.Errorf("write log genesis: %w", err))
	}
	if logged >= 0 {
		return
	}
	genesis := &apigen.CoreWriteUpdate{Time: time.Now().UnixMilli()}
	for _, sp := range []apigen.Space{{ID: 0, Name: "_system"}, {ID: 1, Name: "global"}} {
		genesis.Mutations = append(genesis.Mutations, &apigen.CoreMutation{Create: &apigen.CreateMutation{EntityType: apigen.CoreEntityType_CORE_ENTITY_SPACE, EntityID: int64(sp.ID), Entity: &apigen.CoreEntity{Space: &sp}}})
	}
	err = q.Tx(ctx, func(tx *Queries) error {
		if err := tx.ReduceUpdate(ctx, genesis); err != nil {
			return err
		}
		return tx.InsertWriteEvent(ctx, genesis)
	})
	if err != nil {
		panic(fmt.Errorf("write log genesis: %w", err))
	}
}
