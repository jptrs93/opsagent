package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// legacySecondarySchema is the pre-unification secondary schema: divergent
// column names (seq_no / preparer_seq_no / runner_seq_no) and no
// environment/name columns. Used to simulate an in-place upgrade.
const legacySecondarySchema = `
CREATE TABLE IF NOT EXISTS deployment_configs (
    deployment_id   INTEGER PRIMARY KEY,
    machine         TEXT    NOT NULL DEFAULT '',
    seq_no          INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL,
    updated_by      INTEGER NOT NULL DEFAULT 0,
    spec_blob       BLOB    NOT NULL,
    desired_version TEXT    NOT NULL DEFAULT '',
    desired_running INTEGER NOT NULL DEFAULT 0,
    deleted         INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS deployment_status (
    deployment_id           INTEGER PRIMARY KEY,
    status_seq_no           INTEGER NOT NULL,
    timestamp               INTEGER NOT NULL,
    preparer_seq_no         INTEGER,
    preparer_artifact       TEXT,
    preparer_status         INTEGER,
    runner_seq_no           INTEGER,
    runner_pid              INTEGER,
    runner_artifact         TEXT,
    runner_status           INTEGER,
    runner_num_restarts     INTEGER,
    runner_last_restart_at  INTEGER
);
CREATE TABLE IF NOT EXISTS deployment_status_history (
    deployment_id           INTEGER NOT NULL,
    status_seq_no           INTEGER NOT NULL,
    timestamp               INTEGER NOT NULL,
    preparer_seq_no         INTEGER,
    preparer_artifact       TEXT,
    preparer_status         INTEGER,
    runner_seq_no           INTEGER,
    runner_pid              INTEGER,
    runner_artifact         TEXT,
    runner_status           INTEGER,
    runner_num_restarts     INTEGER,
    runner_last_restart_at  INTEGER,
    PRIMARY KEY (deployment_id, status_seq_no)
);
`

func TestSecondaryFreshBootAndRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	store := NewSecondaryStorageAdapter(dbPath)

	cfg := &apigen.DeploymentConfig{
		ID:           7,
		ConfigID:     &apigen.DeploymentIdentifier{Environment: "prod", Machine: "m1", Name: "api"},
		Version:      3,
		UpdatedAt:    time.UnixMilli(1000),
		Spec:         nonEmptySpec(),
		DesiredState: &apigen.DesiredState{Version: "v3", Running: true},
	}
	store.MustWriteDeploymentConfig(context.Background(), cfg)
	store.MustWriteDeploymentStatus(context.Background(), 7, func(s *apigen.DeploymentStatus) bool {
		s.BumpUpdatedAt()
		s.DeploymentID = 7
		s.Preparer = &apigen.PreparerStatus{DeploymentConfigVersion: 3, Artifact: "art", Status: apigen.PreparationStatus_READY}
		return true
	})

	// Reopen: loadCache must read everything back from disk.
	store2 := NewSecondaryStorageAdapter(dbPath)
	got, _ := store2.MustFetchSnapshotAndSubscribe(context.Background(), "m1")
	if len(got) != 1 {
		t.Fatalf("expected 1 deployment for m1, got %d", len(got))
	}
	rc := got[0].Config
	if rc.Version != 3 || rc.ConfigID.Environment != "prod" || rc.ConfigID.Name != "api" || rc.ConfigID.Machine != "m1" {
		t.Fatalf("config not round-tripped: %+v / %+v", rc, rc.ConfigID)
	}
	rs := got[0].Status
	if rs.Preparer == nil || rs.Preparer.Status != apigen.PreparationStatus_READY || rs.Preparer.Artifact != "art" {
		t.Fatalf("status not round-tripped: %+v", rs)
	}
	if rs.UpdatedAt.IsZero() {
		t.Fatalf("expected non-zero HLC clock, got zero")
	}
}

func TestSecondaryLegacyUpgrade(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")

	// Build a legacy-schema DB with one config + one status row using the OLD
	// column names, then close it.
	seedLegacyDB(t, dbPath)

	// Open through the real init path: schema.sql is a no-op on existing
	// tables, secondary migrations rename/add columns in place.
	store := NewSecondaryStorageAdapter(dbPath)
	got, _ := store.MustFetchSnapshotAndSubscribe(context.Background(), "m9")
	if len(got) != 1 {
		t.Fatalf("expected 1 deployment after upgrade, got %d", len(got))
	}
	cfg := got[0].Config
	// seq_no -> version preserved; environment/name added (empty until repushed).
	if cfg.Version != 5 {
		t.Fatalf("expected version 5 (renamed from seq_no), got %d", cfg.Version)
	}
	if cfg.ConfigID.Machine != "m9" {
		t.Fatalf("expected machine m9, got %q", cfg.ConfigID.Machine)
	}
	st := got[0].Status
	if st.Runner == nil || st.Runner.DeploymentConfigVersion != 5 || st.Runner.RunningPid != 42 {
		t.Fatalf("runner status not preserved across rename: %+v", st.Runner)
	}
	if st.UpdatedAt.UnixNano() != 111 {
		t.Fatalf("expected updated_at 111 preserved (renamed from status_seq_no), got %d", st.UpdatedAt.UnixNano())
	}
}

// nonEmptySpec returns a spec that encodes to non-empty bytes (an empty
// DeploymentSpec{} encodes to nil, which violates spec_blob NOT NULL).
func nonEmptySpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{Runner: &apigen.RunnerConfig{}}
}

func seedLegacyDB(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := db.Exec(legacySecondarySchema); err != nil {
		t.Fatalf("exec legacy schema: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO deployment_configs (deployment_id, machine, seq_no, updated_at, updated_by, spec_blob, desired_version, desired_running, deleted)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		9, "m9", 5, 1000, 0, nonEmptySpec().Encode(), "v5", 1, 0); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO deployment_status (deployment_id, status_seq_no, timestamp,
		     preparer_seq_no, preparer_artifact, preparer_status,
		     runner_seq_no, runner_pid, runner_artifact, runner_status,
		     runner_num_restarts, runner_last_restart_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		9, 111, 2000, 5, "art", int(apigen.PreparationStatus_READY),
		5, 42, "art", int(apigen.RunningStatus_RUNNING), 0, nil); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}
}
