package state

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

// TestScheduledInstanceVersionsSplitMigration builds a v0.0.433-shape database
// — scheduled_instances still carrying created_at and state, no transition log
// — and checks that opening it backfills one baseline version row per
// instance, drops both columns along with the node_active and unique_run
// indexes, and loads instances with their original creation times and states.
// The second Open proves the migration is a no-op once applied, and a state
// transition afterwards appends to the log.
func TestScheduledInstanceVersionsSplitMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	db := sqlitedb.MustOpen(dbPath)
	for _, statement := range []string{
		`CREATE TABLE scheduled_instances (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at         INTEGER NOT NULL,
			deployment_id      INTEGER NOT NULL,
			deployment_version INTEGER NOT NULL,
			node_id            INTEGER NOT NULL,
			instance_ordinal   INTEGER NOT NULL DEFAULT 0,
			state              INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX idx_scheduled_instances_deployment_ordinal
			ON scheduled_instances(deployment_id, instance_ordinal, id)`,
		`CREATE INDEX idx_scheduled_instances_node_active
			ON scheduled_instances(node_id, state)`,
		`CREATE UNIQUE INDEX idx_scheduled_instances_unique_run
			ON scheduled_instances(deployment_id, deployment_version, node_id, instance_ordinal)
			WHERE state = 0`,
		`INSERT INTO scheduled_instances (id, created_at, deployment_id, deployment_version, node_id, state)
			VALUES (1, 1000, 7, 1, 1, 2)`,
		`INSERT INTO scheduled_instances (id, created_at, deployment_id, deployment_version, node_id, state)
			VALUES (2, 2000, 7, 2, 1, 0)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		store := Open(dbPath)
		serving := store.Scheduled[2]
		if serving == nil {
			t.Fatal("migrated serving instance missing from cache")
		}
		if got := serving.Instance.CreatedAt.UnixMilli(); got != 2000 {
			t.Fatalf("serving created_at = %d, want backfilled 2000", got)
		}
		if serving.Instance.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
			t.Fatalf("serving state = %v", serving.Instance.State)
		}
		if store.Scheduled[1] != nil {
			t.Fatal("finalized instance leaked into the live cache")
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// A post-migration transition must append to the log, not rewrite it.
	store := Open(dbPath)
	store.SetScheduledInstanceState(2, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('scheduled_instances')
		WHERE name IN ('created_at', 'state')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("scheduled_instances created_at/state columns were not dropped")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE name IN ('idx_scheduled_instances_node_active', 'idx_scheduled_instances_unique_run')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("node_active/unique_run indexes were not dropped")
	}
	rows, err := db.Query(`SELECT scheduled_instance_id, version, created_at, state
		FROM scheduled_instance_versions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][4]int64
	for rows.Next() {
		var r [4]int64
		if err := rows.Scan(&r[0], &r[1], &r[2], &r[3]); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 ||
		got[0] != [4]int64{1, 1, 1000, 2} ||
		got[1] != [4]int64{2, 1, 2000, 0} ||
		got[2][0] != 2 || got[2][1] != 2 || got[2][3] != 1 {
		t.Fatalf("scheduled_instance_versions rows = %v, want two backfilled baselines plus the appended terminate", got)
	}
}
