package sqlite

import (
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
	if rc.Version != 3 || rc.ConfigID.SpaceID != 1 || rc.ConfigID.Name != "api" || rc.ConfigID.Machine != "m1" {
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

// nonEmptySpec returns a spec that encodes to non-empty bytes (an empty
// DeploymentSpec{} encodes to nil, which violates spec_blob NOT NULL).
func nonEmptySpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{User: "1000"}},
		Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
	}
}

func TestSecondaryStorageMigratesMissingNetworkingToHost(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	store := NewSecondaryStorage(dbPath)
	store.MustWriteDeploymentConfig(&apigen.DeploymentConfig{
		ID:       9,
		ConfigID: apigen.DeploymentIdentifier{SpaceID: 1, Machine: "m1", Name: "legacy"},
		Version:  1,
		Spec: apigen.DeploymentSpec{
			Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
			Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		},
	})
	if err := store.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	reopened := NewSecondaryStorage(dbPath)
	defer reopened.db.Close()
	cfg := reopened.configCache[9]
	if cfg == nil {
		t.Fatal("migrated config not found")
	}
	if cfg.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_HOST {
		t.Fatalf("networking mode = %v, want host", cfg.Spec.Networking.Mode)
	}
}
