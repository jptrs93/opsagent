package state

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestNodeVersionsMigrationSeedsFromLegacyColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	legacy := sqlitedb.MustOpen(dbPath)
	if _, err := legacy.Exec(`
		CREATE TABLE nodes (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			enrollment_id INTEGER,
			enrolled_at   INTEGER NOT NULL DEFAULT 0,
			name          TEXT    NOT NULL,
			identifier    TEXT    NOT NULL DEFAULT '',
			roles         TEXT    NOT NULL DEFAULT '[]',
			addresses     TEXT    NOT NULL DEFAULT '[]',
			wg_public_key TEXT    NOT NULL DEFAULT '',
			allowed_spaces TEXT   NOT NULL DEFAULT '[0]',
			UNIQUE(name),
			UNIQUE(identifier),
			UNIQUE(enrollment_id)
		)`); err != nil {
		t.Fatalf("create legacy nodes table: %v", err)
	}
	if _, err := legacy.Exec(`
		INSERT INTO nodes (enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key, allowed_spaces)
		VALUES (NULL, 1000, 'primary', 'primary-id', '[0]', '["192.0.2.1"]', 'legacy-key', '[0,1]')`); err != nil {
		t.Fatalf("seed legacy node: %v", err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE enrollment_requests (
			id                       INTEGER PRIMARY KEY,
			created_at               INTEGER NOT NULL,
			updated_at               INTEGER NOT NULL,
			requesting_ip_address    TEXT    NOT NULL DEFAULT '',
			requesting_machine_id    TEXT    NOT NULL,
			opendeploy_version       TEXT    NOT NULL DEFAULT '',
			underlay_address         TEXT    NOT NULL DEFAULT '',
			status                   TEXT    NOT NULL DEFAULT 'waiting'
		)`); err != nil {
		t.Fatalf("create legacy enrollment_requests: %v", err)
	}
	if _, err := legacy.Exec(`
		INSERT INTO enrollment_requests (created_at, updated_at, requesting_machine_id)
		VALUES (1000, 1000, 'pending-machine')`); err != nil {
		t.Fatalf("seed legacy enrollment request: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	for pass := 1; pass <= 2; pass++ {
		store := Open(dbPath)
		nodes := store.ListNodes()
		if len(nodes) != 1 {
			t.Fatalf("pass %d: node count = %d, want 1", pass, len(nodes))
		}
		node := nodes[0]
		if node.Name != "primary" || node.WGPublicKey != "legacy-key" ||
			len(node.Addresses) != 1 || node.Addresses[0] != "192.0.2.1" ||
			len(node.Roles) != 1 || node.Roles[0] != NodeRolePrimary {
			t.Fatalf("pass %d: migrated node = %+v, want legacy column values", pass, node)
		}
		if node.Status != apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL {
			t.Fatalf("pass %d: migrated status = %v, want member normal", pass, node.Status)
		}
		if node.CreatedAt.UnixMilli() != 1000 || node.EnrolledAt.UnixMilli() != 1000 {
			t.Fatalf("pass %d: migrated timestamps = %+v, want backfilled from enrolled_at", pass, node)
		}
		versions, err := store.q.ListNodeVersionRows(context.Background(), int64(node.ID))
		if err != nil {
			t.Fatalf("pass %d: list node versions: %v", pass, err)
		}
		if len(versions) != 1 || versions[0].Version != 1 || versions[0].GlobalSeq != 0 || versions[0].CreatedAt != 1000 {
			t.Fatalf("pass %d: seeded versions = %+v, want one v1 row stamped 0", pass, versions)
		}
		requests, err := store.ListEnrollmentRequests()
		if err != nil {
			t.Fatalf("pass %d: list enrollment requests: %v", pass, err)
		}
		if len(requests) != 0 {
			t.Fatalf("pass %d: legacy enrollment requests survived: %+v", pass, requests)
		}
		store.Close()
	}

	check := sqlitedb.MustOpen(dbPath)
	defer check.Close()
	var legacyTables int
	if err := check.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'enrollment_requests'`).Scan(&legacyTables); err != nil {
		t.Fatalf("inspect sqlite_master: %v", err)
	}
	if legacyTables != 0 {
		t.Fatal("enrollment_requests table still exists")
	}
}
