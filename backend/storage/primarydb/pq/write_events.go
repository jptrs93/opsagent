package pq

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (q *Queries) InsertWriteEvent(ctx context.Context, u *apigen.CoreWriteUpdate) error {
	if _, err := q.db.ExecContext(ctx, `INSERT INTO write_events (seq, time, actor) VALUES (?, ?, ?)`, u.Seq, u.Time, u.Actor); err != nil {
		return err
	}
	for i := range u.Mutations {
		m := &u.Mutations[i]
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
	events, err := q.loggedWriteEventsInRange(ctx, after, upTo)
	if err != nil {
		return nil, err
	}
	out := make([]*apigen.CoreWriteUpdate, 0, len(events))
	for _, e := range events {
		out = append(out, e.update)
	}
	return out, nil
}

// loggedEvent is one logged write with, per mutation index, the deployment
// counters the log carries for it.
type loggedEvent struct {
	update *apigen.CoreWriteUpdate
	logged map[int]loggedDeploymentFacts
}

func (q *Queries) loggedWriteEventsInRange(ctx context.Context, after, upTo int64) ([]loggedEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT e.seq, e.time, e.actor, m.entity_type, m.entity_id, m.op, m.payload, m.version, m.spec_version
FROM write_events e JOIN write_event_mutations m ON m.seq = e.seq
WHERE e.seq > ? AND e.seq <= ?
ORDER BY e.seq, m.idx`, after, upTo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []loggedEvent
	for rows.Next() {
		var seq, eventTime, actor, entityType, op int64
		var entityID uint64
		var payload []byte
		var version, specVersion sql.NullInt64
		if err := rows.Scan(&seq, &eventTime, &actor, &entityType, &entityID, &op, &payload, &version, &specVersion); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].update.Seq != seq {
			out = append(out, loggedEvent{update: &apigen.CoreWriteUpdate{Seq: seq, Time: eventTime, Actor: actor}})
		}
		m, err := decodeWriteEventMutation(apigen.CoreEntityType(entityType), entityID, apigen.AuthzVerb(op), payload)
		if err != nil {
			return nil, fmt.Errorf("write event %d: %w", seq, err)
		}
		current := &out[len(out)-1]
		if version.Valid && specVersion.Valid && version.Int64 > 0 && specVersion.Int64 > 0 {
			if current.logged == nil {
				current.logged = map[int]loggedDeploymentFacts{}
			}
			current.logged[len(current.update.Mutations)] = loggedDeploymentFacts{version: uint32(version.Int64), specVersion: uint32(specVersion.Int64)}
		}
		current.update.Mutations = append(current.update.Mutations, m)
	}
	return out, rows.Err()
}

func decodeWriteEventMutation(t apigen.CoreEntityType, id uint64, op apigen.AuthzVerb, payload []byte) (apigen.CoreMutation, error) {
	if op == apigen.AuthzVerb_AUTHZ_VERB_DELETE {
		return apigen.DeleteMutationOf(t, id), nil
	}
	entity, err := apigen.DecodeCoreEntity(payload)
	if err != nil {
		return apigen.CoreMutation{}, err
	}
	StampEntityID(entity, id)
	if op == apigen.AuthzVerb_AUTHZ_VERB_CREATE {
		return apigen.CreateMutationOf(t, id, *entity), nil
	}
	return apigen.UpdateMutationOf(t, id, *entity), nil
}

// LatestMutation returns an entity's newest logged mutation, a delete
// included, or sql.ErrNoRows when the log never saw the entity.
func (q *Queries) LatestMutation(ctx context.Context, t apigen.CoreEntityType, id uint64) (*apigen.CoreMutation, error) {
	var op int64
	var payload []byte
	if err := q.db.QueryRowContext(ctx, `SELECT op, payload FROM write_event_mutations WHERE entity_type = ? AND entity_id = ? ORDER BY seq DESC, idx DESC LIMIT 1`,
		int64(t), id).Scan(&op, &payload); err != nil {
		return nil, err
	}
	m, err := decodeWriteEventMutation(t, id, apigen.AuthzVerb(op), payload)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// refuseLegacyDatabase refuses a database that v0.0.615 never opened: its
// entity history still sits in the per-entity event tables that the
// v0.0.615 materialisation folded into the write log, and that code is gone.
func refuseLegacyDatabase(db *sql.DB) {
	var n int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'deployment_event_log'`).Scan(&n); err != nil {
		panic(fmt.Errorf("legacy check: %w", err))
	}
	if n > 0 {
		panic("this database predates the write log materialisation: start it on v0.0.615 once before upgrading")
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
		genesis.Mutations = append(genesis.Mutations, apigen.CreateMutationOf(apigen.CoreEntityType_CORE_ENTITY_SPACE, sp.ID, entityOf(apigen.CoreEntityValueOneof{Space: &sp})))
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

// loggedDeploymentFacts are the version and spec_version the write log
// carries beside a deployment mutation from before the reducer derived them.
// The spec counter cannot be re-derived from those payloads: specs that
// differed only in fields dropped since decode equal now, so a rebuild takes
// the logged numbers where they exist.
type loggedDeploymentFacts struct {
	version, specVersion uint32
}

func writeEventTypeFilter(types []apigen.CoreEntityType) (string, []any) {
	if len(types) == 0 {
		return "", nil
	}
	marks := make([]string, 0, len(types))
	args := make([]any, 0, len(types))
	for _, t := range types {
		marks = append(marks, "?")
		args = append(args, int64(t))
	}
	return ` AND m.entity_type IN (` + strings.Join(marks, ", ") + `)`, args
}
