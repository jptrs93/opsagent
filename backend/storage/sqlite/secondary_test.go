package sqlite

import (
	"context"
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
		Identity:     apigen.DeploymentIdentity{SpaceID: 1, Machine: "m1", Name: "api"},
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
	got, _, unsub := store2.MustFetchSnapshotAndSubscribe(nil)
	defer unsub()
	if len(got) != 1 {
		t.Fatalf("expected 1 deployment for m1, got %d", len(got))
	}
	rc := got[0].Config
	if rc.NodeID != 23 || rc.Version != 3 || rc.Identity.SpaceID != 1 || rc.Identity.Name != "api" {
		t.Fatalf("config not round-tripped: %+v / %+v", rc, rc.Identity)
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

func TestSecondaryRejectsMissingNodeID(t *testing.T) {
	store := NewSecondaryStorage(filepath.Join(t.TempDir(), "secondary.db"))
	defer store.db.Close()
	defer func() {
		if recover() == nil {
			t.Fatal("missing node ID was accepted")
		}
	}()
	store.MustWriteDeploymentConfig(&apigen.DeploymentConfig{
		ID:           7,
		Identity:     apigen.DeploymentIdentity{SpaceID: 1, Machine: "m1", Name: "api"},
		Version:      1,
		UpdatedAt:    time.UnixMilli(1000),
		Spec:         *nonEmptySpec(),
		DesiredState: apigen.DesiredState{Version: "v1", Running: true},
	})
}

func TestSecondaryRetiresStaleActiveDeploymentKeyBeforeCachingNewDeployment(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	store := NewSecondaryStorage(dbPath)
	store.MustWriteDeploymentConfig(&apigen.DeploymentConfig{
		ID:           7,
		NodeID:       23,
		Identity:     apigen.DeploymentIdentity{SpaceID: 1, Machine: "legacy-a", Name: "api"},
		Version:      4,
		UpdatedAt:    time.UnixMilli(1000),
		Spec:         *nonEmptySpec(),
		DesiredState: apigen.DesiredState{Version: "v1", Running: true},
	})

	store.MustWriteDeploymentConfig(&apigen.DeploymentConfig{
		ID:           8,
		NodeID:       23,
		Identity:     apigen.DeploymentIdentity{SpaceID: 1, Machine: "legacy-b", Name: "api"},
		Version:      1,
		UpdatedAt:    time.UnixMilli(2000),
		Spec:         *nonEmptySpec(),
		DesiredState: apigen.DesiredState{Version: "v2", Running: true},
	})
	store.MustWriteDeploymentConfig(&apigen.DeploymentConfig{
		ID:           9,
		NodeID:       24,
		Identity:     apigen.DeploymentIdentity{SpaceID: 1, Machine: "legacy-c", Name: "api"},
		Version:      1,
		UpdatedAt:    time.UnixMilli(3000),
		Spec:         *nonEmptySpec(),
		DesiredState: apigen.DesiredState{Version: "v3", Running: true},
	})

	if stale := store.configCache[7]; stale == nil || !stale.Deleted || stale.DesiredState.Running {
		t.Fatalf("stale cached deployment = %+v, want locally retired", stale)
	}
	staleRow, err := store.q.GetDeploymentConfig(context.Background(), 7)
	if err != nil {
		t.Fatalf("read stale deployment row: %v", err)
	}
	if staleRow.Deleted == 0 || staleRow.DesiredRunning != 0 {
		t.Fatalf("persisted stale deployment = %+v, want deleted and stopped", staleRow)
	}
	active := store.FetchDeploymentSnapshot(nil)
	if len(active) != 2 {
		t.Fatalf("active cached deployments = %+v, want deployments 8 and 9", active)
	}

	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	store = NewSecondaryStorage(dbPath)
	defer store.db.Close()
	active = store.FetchDeploymentSnapshot(nil)
	if len(active) != 2 {
		t.Fatalf("persisted active deployments = %+v, want deployments 8 and 9", active)
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
