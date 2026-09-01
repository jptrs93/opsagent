package state

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestDeploymentEventLogMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	specV1 := testSpecWithState("v1", true).Encode()
	specV2 := testSpecWithState("v2", true).Encode()

	db := sqlitedb.MustOpen(dbPath)
	stmts := []string{
		`CREATE TABLE deployments (
			deployment_id INTEGER PRIMARY KEY,
			node_id INTEGER NOT NULL DEFAULT -1,
			name TEXT NOT NULL DEFAULT '',
			deleted_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE deployment_spec_versions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			deployment_id INTEGER NOT NULL,
			version INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			author INTEGER NOT NULL DEFAULT 0,
			spec_blob BLOB NOT NULL,
			global_seq INTEGER NOT NULL DEFAULT 0,
			UNIQUE (deployment_id, version)
		)`,
		`CREATE TABLE deployment_space_versions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			deployment_id INTEGER NOT NULL,
			version INTEGER NOT NULL,
			author INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			space_id INTEGER NOT NULL,
			global_seq INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE scheduled_instances (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			deployment_id INTEGER NOT NULL,
			deployment_spec_version INTEGER NOT NULL,
			node_id INTEGER NOT NULL,
			instance_ordinal INTEGER NOT NULL DEFAULT 0,
			deployment_space_version_id INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE scheduled_instance_versions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			scheduled_instance_id INTEGER NOT NULL,
			version INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			state INTEGER NOT NULL,
			global_seq INTEGER NOT NULL DEFAULT 0,
			UNIQUE (scheduled_instance_id, version)
		)`,
		`CREATE TABLE global_seq (id INTEGER PRIMARY KEY CHECK (id = 1), value INTEGER NOT NULL)`,
		`INSERT INTO global_seq (id, value) VALUES (1, 5)`,
		`INSERT INTO deployments (deployment_id, node_id, name, deleted_at) VALUES (1, 1, 'web', 0)`,
		`INSERT INTO deployments (deployment_id, node_id, name, deleted_at) VALUES (2, 1, 'old', 5000)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt[:40], err)
		}
	}
	insertSpec := func(dep, version, createdAt, author, seq int64, blob []byte) {
		if _, err := db.Exec(`INSERT INTO deployment_spec_versions (deployment_id, version, created_at, author, spec_blob, global_seq)
			VALUES (?, ?, ?, ?, ?, ?)`, dep, version, createdAt, author, blob, seq); err != nil {
			t.Fatal(err)
		}
	}
	insertSpace := func(dep, version, createdAt, author, spaceID, seq int64) int64 {
		res, err := db.Exec(`INSERT INTO deployment_space_versions (deployment_id, version, author, created_at, space_id, global_seq)
			VALUES (?, ?, ?, ?, ?, ?)`, dep, version, author, createdAt, spaceID, seq)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	insertSpec(1, 1, 1000, 7, 1, specV1)
	insertSpace(1, 1, 1000, 7, 1, 1)
	insertSpec(1, 2, 2000, 8, 2, specV2)
	movedRowID := insertSpace(1, 2, 3000, 9, 2, 3)
	insertSpec(2, 1, 4000, 7, 4, specV1)
	insertSpace(2, 1, 4000, 7, 1, 4)
	insertSpec(2, 2, 5000, 7, 5, specV1)
	if _, err := db.Exec(`INSERT INTO scheduled_instances (deployment_id, deployment_spec_version, node_id, instance_ordinal, deployment_space_version_id)
		VALUES (1, 2, 1, 0, ?)`, movedRowID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO scheduled_instance_versions (scheduled_instance_id, version, created_at, state, global_seq)
		VALUES (1, 1, 3000, 1, 3)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store := Open(dbPath)
	web := store.FetchDeployment(1)
	if web == nil || web.Version != 3 || web.SpecVersion != 2 || web.SpaceVersion != 2 || web.SpaceID != 2 ||
		web.Deleted || web.Name != "web" || web.NodeID != 1 || web.Author != 9 ||
		web.CreatedAt.UnixMilli() != 1000 || web.UpdatedAt.UnixMilli() != 3000 {
		t.Fatalf("migrated web = %+v, want v3 specV2 spaceV2 space 2 author 9", web)
	}
	if web.WorkloadVersion() != "v2" {
		t.Fatalf("migrated web workload = %q, want v2", web.WorkloadVersion())
	}
	old := store.FetchDeployment(2)
	if old == nil || old.Version != 2 || old.SpecVersion != 2 || old.SpaceVersion != 1 || !old.Deleted {
		t.Fatalf("migrated old = %+v, want deleted v2 specV2 spaceV1", old)
	}

	events, err := store.q.ListDeploymentEvents(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 ||
		events[0].EventType != pq.DeploymentEventCreate || events[0].SpecVersion != 1 || events[0].SpaceAssignmentVersion != 1 || events[0].GlobalSeq != 1 ||
		events[1].EventType != pq.DeploymentEventUpdate || events[1].SpecVersion != 2 || events[1].SpaceAssignmentVersion != 1 ||
		events[2].EventType != pq.DeploymentEventUpdate || events[2].SpecVersion != 2 || events[2].SpaceAssignmentVersion != 2 {
		t.Fatalf("web events = %+v, want folded create then spec update then space move", events)
	}
	oldEvents, err := store.q.ListDeploymentEvents(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldEvents) != 2 || oldEvents[1].EventType != pq.DeploymentEventDelete {
		t.Fatalf("old events = %+v, want create then delete", oldEvents)
	}

	inst, err := store.q.GetScheduledInstance(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if inst.SpaceID != 2 {
		t.Fatalf("backfilled instance space = %d, want 2", inst.SpaceID)
	}

	created := store.MustCreateDeploymentForNode(apigen.Context{}, DefaultSpaceID, "fresh", 1, nonEmptySpec())
	if created.ID != 3 {
		t.Fatalf("new deployment id = %d, want 3 (no id reuse)", created.ID)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = Open(dbPath)
	defer store.Close()
	reEvents, err := store.q.ListDeploymentEvents(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(reEvents) != 3 {
		t.Fatalf("reopen event count = %d, want 3: migration must not re-run", len(reEvents))
	}
}
