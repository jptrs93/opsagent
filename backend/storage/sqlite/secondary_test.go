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

	cfg := &apigen.DeploymentConfig2{
		ID:        7,
		NodeID:    23,
		Identity:  apigen.DeploymentIdentity{SpaceID: 1, Name: "api"},
		Version:   3,
		UpdatedAt: time.UnixMilli(1000),
		Spec:      *testSpecWithState("v3", true),
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
	store.MustWriteDeploymentConfig(&apigen.DeploymentConfig2{
		ID:        7,
		Identity:  apigen.DeploymentIdentity{SpaceID: 1, Name: "api"},
		Version:   1,
		UpdatedAt: time.UnixMilli(1000),
		Spec:      *testSpecWithState("v1", true),
	})
}

func TestSecondaryRetiresStaleActiveDeploymentKeyBeforeCachingNewDeployment(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	store := NewSecondaryStorage(dbPath)
	store.MustWriteDeploymentConfig(&apigen.DeploymentConfig2{
		ID:        7,
		NodeID:    23,
		Identity:  apigen.DeploymentIdentity{SpaceID: 1, Name: "api"},
		Version:   4,
		UpdatedAt: time.UnixMilli(1000),
		Spec:      *testSpecWithState("v1", true),
	})

	store.MustWriteDeploymentConfig(&apigen.DeploymentConfig2{
		ID:        8,
		NodeID:    23,
		Identity:  apigen.DeploymentIdentity{SpaceID: 1, Name: "api"},
		Version:   1,
		UpdatedAt: time.UnixMilli(2000),
		Spec:      *testSpecWithState("v2", true),
	})
	store.MustWriteDeploymentConfig(&apigen.DeploymentConfig2{
		ID:        9,
		NodeID:    24,
		Identity:  apigen.DeploymentIdentity{SpaceID: 1, Name: "api"},
		Version:   1,
		UpdatedAt: time.UnixMilli(3000),
		Spec:      *testSpecWithState("v3", true),
	})

	if stale := store.configCache[7]; stale == nil || !stale.Deleted || stale.WorkloadRunning() {
		t.Fatalf("stale cached deployment = %+v, want locally retired", stale)
	}
	staleRow, err := store.q.GetDeploymentConfig(context.Background(), 7)
	if err != nil {
		t.Fatalf("read stale deployment row: %v", err)
	}
	if staleRow.Deleted == 0 || staleRow.DesiredRunning != 0 {
		t.Fatalf("persisted stale deployment = %+v, want deleted and stopped", staleRow)
	}
	staleSpec, err := apigen.DecodeDeploymentSpec2(staleRow.SpecBlob)
	if err != nil {
		t.Fatalf("decode persisted stale deployment: %v", err)
	}
	if staleSpec.WorkloadVersion() != "v1" || staleSpec.WorkloadRunning() {
		t.Fatalf("persisted stale workload = %q/%v, want stopped v1", staleSpec.WorkloadVersion(), staleSpec.WorkloadRunning())
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

// nonEmptySpec returns a valid spec that encodes to non-empty bytes.
func nonEmptySpec() *apigen.DeploymentSpec2 {
	return &apigen.DeploymentSpec2{
		Container1Spec: &apigen.ContainerSpec{
			Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "example/app"}},
			Runtime: apigen.ContainerRuntime{User: "1000"},
		},
		Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
	}
}

func testSpecWithState(version string, running bool) *apigen.DeploymentSpec2 {
	spec := nonEmptySpec()
	if err := spec.SetWorkloadState(version, running); err != nil {
		panic(err)
	}
	return spec
}
