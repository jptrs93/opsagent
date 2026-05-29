package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestSecondaryFreshBootAndRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	store := NewSecondaryStorageAdapter(dbPath)

	cfg := &apigen.DeploymentConfig{
		ID:           7,
		ConfigID:     apigen.DeploymentIdentifier{Environment: "prod", Machine: "m1", Name: "api"},
		Version:      3,
		UpdatedAt:    time.UnixMilli(1000),
		Spec:         *nonEmptySpec(),
		DesiredState: apigen.DesiredState{Version: "v3", Running: true},
	}
	store.MustWriteDeploymentConfig(context.Background(), cfg)
	store.MustWriteDeploymentStatus(context.Background(), 7, func(s *apigen.DeploymentStatus) bool {
		s.BumpUpdatedAt()
		s.DeploymentID = 7
		s.Preparer = apigen.PreparerStatus{DeploymentConfigVersion: 3, Artifact: "art", Status: apigen.PreparationStatus_READY}
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
	if rs.Preparer.Status != apigen.PreparationStatus_READY || rs.Preparer.Artifact != "art" {
		t.Fatalf("status not round-tripped: %+v", rs)
	}
	if rs.UpdatedAt.IsZero() {
		t.Fatalf("expected non-zero HLC clock, got zero")
	}
}

// nonEmptySpec returns a spec that encodes to non-empty bytes (an empty
// DeploymentSpec{} encodes to nil, which violates spec_blob NOT NULL).
func nonEmptySpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{Runner: apigen.RunnerConfig{Systemd: apigen.SystemdRunnerConfig{Name: "test"}}}
}
