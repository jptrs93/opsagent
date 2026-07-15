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

func TestSecondaryMigrationLeavesLegacyNodeIDsForNewUpdates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE deployment_configs (
			deployment_id INTEGER PRIMARY KEY,
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
			deployment_id, space_id, machine, name, created_at, version, updated_at,
			updated_by, spec_blob, desired_version, desired_running, deleted
		) VALUES (7, 1, 'm1', 'api', 1000, 1, 1000, 0, x'', 'v1', 1, 0);
		INSERT INTO deployment_config_history (
			deployment_id, version, updated_at, updated_by, spec_blob,
			desired_version, desired_running, deleted
		) VALUES (7, 1, 1000, 0, x'', 'v1', 1, 0)`); err != nil {
		t.Fatalf("seed legacy database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	store := NewSecondaryStorage(dbPath)
	defer store.db.Close()
	legacy, _, unsub := store.MustFetchSnapshotAndSubscribe("m1")
	unsub()
	if len(legacy) != 1 || legacy[0].Config.NodeID != -1 {
		t.Fatalf("legacy config node ID = %+v, want -1", legacy)
	}
	var historyNodeID int64
	if err := store.db.QueryRow(`SELECT node_id FROM deployment_config_history WHERE deployment_id = 7 AND version = 1`).Scan(&historyNodeID); err != nil {
		t.Fatalf("read legacy history node ID: %v", err)
	}
	if historyNodeID != -1 {
		t.Fatalf("legacy history node ID = %d, want -1", historyNodeID)
	}

	store.MustWriteDeploymentConfig(&apigen.DeploymentConfig{
		ID:           7,
		NodeID:       42,
		ConfigID:     apigen.DeploymentIdentifier{SpaceID: 1, Machine: "m1", Name: "api"},
		Version:      2,
		UpdatedAt:    time.UnixMilli(2000),
		Spec:         *nonEmptySpec(),
		DesiredState: apigen.DesiredState{Version: "v2", Running: true},
	})
	updated, _, unsub := store.MustFetchSnapshotAndSubscribe("m1")
	unsub()
	if len(updated) != 1 || updated[0].Config.NodeID != 42 {
		t.Fatalf("updated config node ID = %+v, want 42", updated)
	}
	if err := store.db.QueryRow(`SELECT node_id FROM deployment_config_history WHERE deployment_id = 7 AND version = 1`).Scan(&historyNodeID); err != nil {
		t.Fatalf("read history node ID after update: %v", err)
	}
	if historyNodeID != -1 {
		t.Fatalf("history node ID after update = %d, want -1", historyNodeID)
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
