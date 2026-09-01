package state

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func encodeLegacyDeploymentBlob(id, version, specVersion, spaceVersion int32, createdAt, updatedAt int64, author, nodeID int32, spec *apigen.DeploymentSpec, spaceID int32, name string, deleted bool) []byte {
	var b []byte
	b = apigen.AppendInt32Field(b, id, 1)
	b = apigen.AppendInt32Field(b, nodeID, 2)
	b = apigen.AppendInt64Field(b, createdAt, 4)
	b = apigen.AppendInt64Field(b, updatedAt, 5)
	b = apigen.AppendInt32Field(b, author, 6)
	b = apigen.AppendInt32Field(b, specVersion, 7)
	b = apigen.AppendTag(b, 8, apigen.BytesType)
	b = apigen.AppendBytes(b, spec.Encode())
	b = apigen.AppendBoolField(b, deleted, 9)
	b = apigen.AppendInt32Field(b, spaceID, 10)
	b = apigen.AppendStringField(b, name, 11)
	b = apigen.AppendInt32Field(b, spaceVersion, 12)
	b = apigen.AppendInt32Field(b, version, 13)
	return b
}

func TestPreSplitDatabaseUpgrade(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	db := sqlitedb.MustOpen(dbPath)
	if _, err := db.Exec(`CREATE TABLE deployment_event_log (
		id                       INTEGER PRIMARY KEY AUTOINCREMENT,
		global_seq               INTEGER NOT NULL,
		created_at               INTEGER NOT NULL,
		author                   INTEGER NOT NULL,
		deployment_id            INTEGER NOT NULL CHECK (deployment_id BETWEEN 1 AND 16777215),
		version                  INTEGER NOT NULL,
		spec_version             INTEGER NOT NULL,
		space_assignment_version INTEGER NOT NULL,
		name_version             INTEGER NOT NULL,
		value                    BLOB NOT NULL,
		event_type               INTEGER NOT NULL DEFAULT 0,
		UNIQUE (deployment_id, version)
	)`); err != nil {
		t.Fatal(err)
	}
	spec1 := testSpecWithState("v1", true)
	spec2 := testSpecWithState("v2", true)
	insert := func(seq, createdAt, updatedAt int64, id, version, specVersion int32, blob []byte, eventType int64) {
		if _, err := db.Exec(`INSERT INTO deployment_event_log (
			global_seq, created_at, author, deployment_id, version, spec_version,
			space_assignment_version, name_version, value, event_type
		) VALUES (?, ?, 5, ?, ?, ?, 1, 1, ?, ?)`,
			seq, updatedAt, id, version, specVersion, blob, eventType); err != nil {
			t.Fatal(err)
		}
	}
	insert(1, 1000, 1000, 7, 1, 1, encodeLegacyDeploymentBlob(7, 1, 1, 1, 1000, 1000, 5, 3, spec1, 1, "api", false), pq.DeploymentEventCreate)
	insert(2, 1000, 2000, 7, 2, 2, encodeLegacyDeploymentBlob(7, 2, 2, 1, 1000, 2000, 5, 3, spec2, 1, "api", false), pq.DeploymentEventUpdate)
	insert(3, 3000, 3000, 8, 1, 1, encodeLegacyDeploymentBlob(8, 1, 1, 1, 3000, 3000, 5, 3, spec1, 1, "gone", false), pq.DeploymentEventCreate)
	insert(4, 3000, 4000, 8, 2, 1, encodeLegacyDeploymentBlob(8, 2, 1, 1, 3000, 4000, 5, 3, spec1, 1, "gone", true), pq.DeploymentEventDelete)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store := Open(dbPath)
	defer store.Close()

	cfg := store.FetchDeployment(7)
	if cfg == nil {
		t.Fatal("deployment 7 not loaded")
	}
	if cfg.Def.NodeID != 3 || cfg.Def.SpaceID != 1 || cfg.Def.Name != "api" || cfg.Def.Spec.WorkloadVersion() != "v2" {
		t.Fatalf("def not decoded from legacy blob: %+v", cfg.Def)
	}
	if cfg.Version != 2 || cfg.SpecVersion != 2 || cfg.SpaceVersion != 1 || cfg.NameVersion != 1 || cfg.Author != 5 {
		t.Fatalf("envelope not read from columns: %+v", cfg)
	}
	if cfg.CreatedTime.UnixMilli() != 1000 || cfg.EventTime.UnixMilli() != 2000 {
		t.Fatalf("times = %d/%d, want 1000/2000 (created_time backfill from v1 row)", cfg.CreatedTime.UnixMilli(), cfg.EventTime.UnixMilli())
	}
	if cfg.Deleted() {
		t.Fatal("deployment 7 read as deleted")
	}
	if cfg.NodeID != 3 || cfg.SpaceID != 1 || cfg.Name != "api" || cfg.Spec.WorkloadVersion() != "v2" {
		t.Fatalf("legacy flat mirror not populated: %+v", cfg)
	}

	gone := store.FetchDeployment(8)
	if gone == nil || !gone.Deleted() {
		t.Fatalf("deployment 8 tombstone not read from event_type: %+v", gone)
	}
	if gone.Def.Spec.WorkloadVersion() != "v1" {
		t.Fatal("tombstone lost its spec")
	}
	if got := store.FetchDeletedDeploymentSnapshot(nil, 10); len(got) != 1 || got[0].ID != 8 {
		t.Fatalf("deleted snapshot = %+v, want deployment 8", got)
	}

	updated := updateDeployment(store, apigen.Context{}, 7, DeploymentUpdate{Spec: testSpecWithState("v3", true)})
	if updated.Version != 3 || updated.SpecVersion != 3 || updated.SpaceVersion != 1 || updated.NameVersion != 1 {
		t.Fatalf("post-migration update derived versions wrong: %+v", updated)
	}
	if updated.CreatedTime.UnixMilli() != 1000 {
		t.Fatalf("created_time not carried forward: %d", updated.CreatedTime.UnixMilli())
	}

	history := store.MustFetchDeploymentHistory(7)
	if len(history) != 3 || history[0].Def.Spec.WorkloadVersion() != "v1" || history[2].Def.Spec.WorkloadVersion() != "v3" {
		t.Fatalf("history = %d entries", len(history))
	}

	store.Close()
	store = Open(dbPath)
	defer store.Close()
	if cfg := store.FetchDeployment(7); cfg == nil || cfg.CreatedTime.UnixMilli() != 1000 {
		t.Fatalf("reopen after migration: %+v", cfg)
	}
}
