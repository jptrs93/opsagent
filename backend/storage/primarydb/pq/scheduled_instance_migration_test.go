package pq

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

// The migration folds the legacy scheduled_instances + scheduled_instance_versions
// pair into scheduled_instance_event_log: every version row becomes an event row
// carrying the instance's identity, created_time is the first version's
// created_at, and the legacy tables are dropped.
func TestScheduledInstanceEventLogMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db := sqlitedb.MustOpen(dbPath)
	if _, err := db.Exec(`
		CREATE TABLE scheduled_instances (
			id                      INTEGER PRIMARY KEY AUTOINCREMENT,
			deployment_id           INTEGER NOT NULL,
			deployment_version      INTEGER NOT NULL DEFAULT 0,
			deployment_spec_version INTEGER NOT NULL,
			node_id                 INTEGER NOT NULL,
			instance_ordinal        INTEGER NOT NULL DEFAULT 0,
			space_id                INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE scheduled_instance_versions (
			id                    INTEGER PRIMARY KEY AUTOINCREMENT,
			scheduled_instance_id INTEGER NOT NULL,
			version               INTEGER NOT NULL,
			created_at            INTEGER NOT NULL,
			state                 INTEGER NOT NULL,
			global_seq            INTEGER NOT NULL DEFAULT 0,
			UNIQUE (scheduled_instance_id, version)
		);
		INSERT INTO scheduled_instances (id, deployment_id, deployment_version, deployment_spec_version, node_id, instance_ordinal, space_id)
		VALUES (1, 10, 3, 2, 7, 0, 4), (2, 10, 4, 3, 7, 0, 4);
		INSERT INTO scheduled_instance_versions (scheduled_instance_id, version, created_at, state, global_seq)
		VALUES (1, 1, 1000, 3, 51), (1, 2, 2000, 4, 52), (1, 3, 3000, 2, 55),
		       (2, 1, 2500, 3, 53);
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	q := Open(dbPath)
	defer q.Close()
	ctx := context.Background()

	first, err := q.GetScheduledInstance(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := ScheduledInstanceRow{ID: 1, CreatedTime: 1000, DeploymentID: 10, DeploymentVersion: 3,
		DeploymentSpecVersion: 2, NodeID: 7, InstanceOrdinal: 0, SpaceID: 4, State: 2}
	if first != want {
		t.Fatalf("migrated instance 1 = %+v, want %+v", first, want)
	}

	nonFinal, err := q.ListNonFinalScheduledInstances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(nonFinal) != 1 || nonFinal[0].ID != 2 || nonFinal[0].CreatedTime != 2500 || nonFinal[0].State != 3 {
		t.Fatalf("non-final instances after migration = %+v", nonFinal)
	}

	db = q.sqlDB()
	var events, seqs int64
	if err := db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT global_seq) FROM scheduled_instance_event_log`).Scan(&events, &seqs); err != nil {
		t.Fatal(err)
	}
	if events != 4 || seqs != 4 {
		t.Fatalf("event log has %d rows / %d distinct seqs, want 4 / 4", events, seqs)
	}
	for _, table := range []string{"scheduled_instances", "scheduled_instance_versions"} {
		if _, err := db.Exec(`SELECT * FROM ` + table); err == nil {
			t.Fatalf("legacy table %s still present after migration", table)
		}
	}

	next, err := q.NextScheduledInstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if next != 3 {
		t.Fatalf("NextScheduledInstanceID = %d, want 3", next)
	}

	// Re-opening must not duplicate the copy: the events already present make
	// the guarded INSERT a no-op and the drops fail as already applied.
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	q = Open(dbPath)
	defer q.Close()
	if err := q.sqlDB().QueryRow(`SELECT COUNT(*) FROM scheduled_instance_event_log`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 4 {
		t.Fatalf("event log has %d rows after reopen, want 4", events)
	}
}
