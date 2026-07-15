package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// TestSecondaryFreshBootAndRoundTrip initialises a secondary store from scratch,
// writes a config + status, reopens it, and verifies everything reads back.
func TestSecondaryFreshBootAndRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	store := NewSecondaryStorage(dbPath)

	cfg := &apigen.DeploymentConfig{
		ID:           7,
		NodeID:       23,
		ConfigID:     apigen.DeploymentIdentifier{SpaceID: 1, Machine: "m1", Name: "api"},
		Version:      3,
		UpdatedAt:    time.UnixMilli(1000),
		Spec:         *nonEmptySpec(),
		DesiredState: apigen.DesiredState{Version: "v3", Running: true},
	}
	store.MustWriteDeploymentConfig(cfg)
	store.MustWriteDeploymentStatus(7, func(s *apigen.DeploymentStatus) bool {
		s.BumpUpdatedAt()
		s.DeploymentID = 7
		s.Preparer = apigen.PreparerStatus{DeploymentConfigVersion: 3, Artifact: "art", Status: apigen.PreparationStatus_READY}
		s.Runner = apigen.RunnerStatus{
			DeploymentConfigVersion: 3,
			Status:                  apigen.RunningStatus_RUNNING,
			Endpoints: []*apigen.Endpoint{{
				Ordinal: 0,
				Address: "fd00::7",
				Machine: "m1",
				State:   apigen.EndpointState_ENDPOINT_READY,
			}},
			NetworkDiagnostics: []string{"listener is IPv4-only"},
		}
		return true
	})

	// Reopen: loadCache must read everything back from disk.
	store2 := NewSecondaryStorage(dbPath)
	got, _, unsub := store2.MustFetchSnapshotAndSubscribe("m1")
	defer unsub()
	if len(got) != 1 {
		t.Fatalf("expected 1 deployment for m1, got %d", len(got))
	}
	rc := got[0].Config
	if rc.NodeID != 23 || rc.Version != 3 || rc.ConfigID.SpaceID != 1 || rc.ConfigID.Name != "api" || rc.ConfigID.Machine != "m1" {
		t.Fatalf("config not round-tripped: %+v / %+v", rc, rc.ConfigID)
	}
	rs := got[0].Status
	if rs.Preparer.Status != apigen.PreparationStatus_READY || rs.Preparer.Artifact != "art" {
		t.Fatalf("status not round-tripped: %+v", rs)
	}
	if len(rs.Runner.Endpoints) != 1 || rs.Runner.Endpoints[0].Address != "fd00::7" || rs.Runner.Endpoints[0].State != apigen.EndpointState_ENDPOINT_READY {
		t.Fatalf("runner endpoints not round-tripped: %+v", rs.Runner.Endpoints)
	}
	if len(rs.Runner.NetworkDiagnostics) != 1 || rs.Runner.NetworkDiagnostics[0] != "listener is IPv4-only" {
		t.Fatalf("runner diagnostics not round-tripped: %+v", rs.Runner.NetworkDiagnostics)
	}
	if rs.UpdatedAt.IsZero() {
		t.Fatalf("expected non-zero HLC clock, got zero")
	}
}

func TestSecondaryMigrationBackfillsConfigsAndDropsHistoryNodeID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE deployment_configs (
			deployment_id INTEGER PRIMARY KEY,
			node_id INTEGER NOT NULL DEFAULT -1,
			space_id INTEGER NOT NULL DEFAULT 1,
			machine TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0,
			version INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL,
			updated_by INTEGER NOT NULL DEFAULT 0,
			spec_blob BLOB NOT NULL,
			desired_version TEXT NOT NULL DEFAULT '',
			desired_running INTEGER NOT NULL DEFAULT 0,
			deleted INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE deployment_config_history (
			deployment_id INTEGER NOT NULL,
			node_id INTEGER NOT NULL DEFAULT -1,
			version INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			updated_by INTEGER NOT NULL DEFAULT 0,
			spec_blob BLOB NOT NULL,
			desired_version TEXT NOT NULL DEFAULT '',
			desired_running INTEGER NOT NULL DEFAULT 0,
			deleted INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (deployment_id, version)
		);
		INSERT INTO deployment_configs (
			deployment_id, node_id, space_id, machine, name, created_at, version, updated_at,
			updated_by, spec_blob, desired_version, desired_running, deleted
		) VALUES
			(7, 42, 1, 'm1', 'api', 1000, 1, 1000, 0, x'', 'v1', 1, 0),
			(8, -1, 1, 'm1', 'old-api', 1000, 1, 1000, 0, x'', 'v1', 0, 1);
		INSERT INTO deployment_config_history (
			deployment_id, node_id, version, updated_at, updated_by, spec_blob,
			desired_version, desired_running, deleted
		) VALUES (8, -1, 1, 1000, 0, x'', 'v1', 0, 1)`); err != nil {
		t.Fatalf("seed deployed database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	store := NewSecondaryStorage(dbPath)
	defer store.db.Close()
	configs, _, unsub := store.MustFetchSnapshotAndSubscribe("m1")
	unsub()
	if len(configs) != 1 || configs[0].Config.NodeID != 42 {
		t.Fatalf("active config after migration = %+v, want node ID 42", configs)
	}
	var deletedNodeID int64
	if err := store.db.QueryRow(`SELECT node_id FROM deployment_configs WHERE deployment_id = 8`).Scan(&deletedNodeID); err != nil {
		t.Fatalf("read backfilled config node ID: %v", err)
	}
	if deletedNodeID != 42 {
		t.Fatalf("backfilled config node ID = %d, want 42", deletedNodeID)
	}
	if tableHasColumn(store.db, "deployment_config_history", "node_id") {
		t.Fatal("deployment_config_history.node_id still exists")
	}
}

func TestSecondaryNormalizesMissingNodeID(t *testing.T) {
	store := NewSecondaryStorage(filepath.Join(t.TempDir(), "secondary.db"))
	defer store.db.Close()
	store.MustWriteDeploymentConfig(&apigen.DeploymentConfig{
		ID:           7,
		ConfigID:     apigen.DeploymentIdentifier{SpaceID: 1, Machine: "m1", Name: "api"},
		Version:      1,
		UpdatedAt:    time.UnixMilli(1000),
		Spec:         *nonEmptySpec(),
		DesiredState: apigen.DesiredState{Version: "v1", Running: true},
	})

	configs, _, unsub := store.MustFetchSnapshotAndSubscribe("m1")
	unsub()
	if len(configs) != 1 || configs[0].Config.NodeID != -1 {
		t.Fatalf("missing node ID normalized to %+v, want -1", configs)
	}
}

// nonEmptySpec returns a spec that encodes to non-empty bytes (an empty
// DeploymentSpec{} encodes to nil, which violates spec_blob NOT NULL).
func nonEmptySpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{User: "1000"}},
		Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
	}
}
