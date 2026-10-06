package pq

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func commitForTest(t *testing.T, q *Queries, seq, now, actor int64, ms ...apigen.CoreMutation) {
	t.Helper()
	ctx := context.Background()
	u := &apigen.CoreWriteUpdate{Seq: seq, Time: now, Actor: actor, Mutations: ms}
	if err := q.Tx(ctx, func(tx *Queries) error {
		if err := tx.ReduceUpdate(ctx, u); err != nil {
			return err
		}
		if err := tx.InsertWriteEvent(ctx, u); err != nil {
			return err
		}
		return tx.SetGlobalSeq(ctx, seq)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRebuildKeepsTheCountersTheLogCarries(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	q := Open(dbPath)
	defer q.Close()
	d := &apigen.Deployment{SpaceID: 1, Name: "app", Spec: testSpec(), Scheduling: apigen.DedicatedScheduling(true, 1)}
	entity := entityOf(apigen.CoreEntityValueOneof{Deployment: d})
	payload := entity.Encode()
	const typ = int64(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT)
	for _, row := range []struct {
		seq, op, version, spec int64
	}{{1, 1, 7, 3}, {2, 2, 8, 4}} {
		if _, err := q.db.ExecContext(ctx, `INSERT INTO write_events (seq, time, actor) VALUES (?, ?, 0)`, row.seq, row.seq*1000); err != nil {
			t.Fatal(err)
		}
		if _, err := q.db.ExecContext(ctx, `INSERT INTO write_event_mutations (seq, idx, entity_type, entity_id, op, payload, version, spec_version) VALUES (?, 0, ?, 5, ?, ?, ?, ?)`,
			row.seq, typ, row.op, payload, row.version, row.spec); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.SetGlobalSeq(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := q.Tx(ctx, func(tx *Queries) error { return tx.RebuildFromLog(ctx) }); err != nil {
		t.Fatal(err)
	}
	var version, spec int64
	if err := q.db.QueryRowContext(ctx, `SELECT version, spec_version FROM deployments WHERE id = 5`).Scan(&version, &spec); err != nil {
		t.Fatal(err)
	}
	if version != 8 || spec != 4 {
		t.Fatalf("rebuilt deployment 5 as version %d spec %d, the log carries 8 and 4", version, spec)
	}
	var versions int64
	if err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployment_versions`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 1 {
		t.Fatalf("deployment_versions after the rebuild: %d rows", versions)
	}
	next := &apigen.DeploymentRecord{Deployment: *d, Meta: apigen.EntityMeta{Version: 9}}
	next.Deployment.ID = 5
	commitForTest(t, q, 3, 3000, 0, DeploymentMutation(next).Wire())
	var logged, loggedSpec *int64
	if err := q.db.QueryRowContext(ctx, `SELECT version, spec_version FROM write_event_mutations WHERE seq = 3`).Scan(&logged, &loggedSpec); err != nil {
		t.Fatal(err)
	}
	if logged != nil || loggedSpec != nil {
		t.Fatalf("a new write logged counters: %v %v", logged, loggedSpec)
	}
}

func TestOpenRefusesADatabaseWithoutTheDataModelFormat(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	db := sqlitedb.MustOpenWriter(dbPath)
	if _, err := db.Exec(`CREATE TABLE write_event_mutations (seq INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Open accepted a database without a format_version row")
		}
		if msg, _ := r.(string); !strings.Contains(msg, "v0.0.616") {
			t.Fatalf("refusal does not name the release to step through: %v", r)
		}
	}()
	Open(dbPath).Close()
}

func TestOpenRefusesADatabaseFromANewerRelease(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	Open(dbPath).Close()
	db := sqlitedb.MustOpenWriter(dbPath)
	if _, err := db.Exec(`UPDATE format_version SET version = ?`, DataModelFormatVersion+1); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		r := recover()
		if msg, _ := r.(string); r == nil || !strings.Contains(msg, "newer release") {
			t.Fatalf("Open accepted a newer format: %v", r)
		}
	}()
	Open(dbPath).Close()
}
