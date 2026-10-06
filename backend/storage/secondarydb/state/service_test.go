package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// TestSecondaryFreshBootAndRoundTrip initialises a secondary store from scratch,
// writes a scheduled instance assignment + status, reopens it, and verifies
// everything reads back.
func TestSecondaryFreshBootAndRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	store := Open(dbPath)

	cfg := apigen.DeploymentRecord{
		Deployment: apigen.Deployment{ID: 7, Scheduling: apigen.DedicatedScheduling(true, 23), SpaceID: 1, Name: "api", Spec: *testSpecWithVersion("v3")},
		Meta:       apigen.EntityMeta{Version: 3, SpecVersion: 3, UpdatedTime: 1000},
	}
	const instanceID uint64 = 11
	store.MustWriteScheduledInstanceAssignment(&apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{
			ID:         instanceID,
			NodeID:     23,
			Deployment: apigen.DeploymentRef{DeploymentID: 7, Version: 3},
			State:      apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
		},
		Config: cfg,
	})
	store.MustWriteScheduledInstanceStatus(instanceID, func(s *apigen.ScheduledInstanceStatus) bool {
		s.BumpUpdatedAt()
		s.Preparer = apigen.Some(apigen.PreparerStatus{
			DeploymentSpecVersion: 3,
			Artifact:              "art",
			Inputs:                apigen.InputsStatus_INPUTS_STATUS_READY,
			Image:                 apigen.Some(apigen.ImageStatus_IMAGE_STATUS_READY),
		})
		s.Runner = apigen.Some(apigen.RunnerStatus{
			DeploymentSpecVersion: 3,
			Status:                apigen.RunningStatus_RUNNING_STATUS_RUNNING,
			NetworkDiagnostics:    []string{"listener is IPv4-only"},
		})
		return true
	})

	// Reopen: loadCache must read everything back from disk.
	store2 := Open(dbPath)
	got, _, unsub := store2.MustFetchScheduledSnapshotAndSubscribe(nil)
	defer unsub()
	if len(got) != 1 {
		t.Fatalf("expected 1 scheduled instance, got %d", len(got))
	}
	rc := got[0].Config
	if rc.Deployment.PlacementNodeID() != 23 || rc.Meta.SpecVersion != 3 || rc.Deployment.SpaceID != 1 || rc.Deployment.Name != "api" {
		t.Fatalf("config not round-tripped: %+v", rc)
	}
	if !got[0].Status.Present {
		t.Fatalf("status not round-tripped: %+v", got[0])
	}
	rs := got[0].Status.Value
	preparer, runner := rs.Preparer.Value, rs.Runner.Value
	if !rs.Preparer.Present || preparer.Rollup() != apigen.PreparationStatus_PREPARATION_STATUS_READY || preparer.Artifact != "art" {
		t.Fatalf("status not round-tripped: %+v", rs)
	}
	if preparer.Inputs != apigen.InputsStatus_INPUTS_STATUS_READY || preparer.Image.Value != apigen.ImageStatus_IMAGE_STATUS_READY {
		t.Fatalf("preparer stages not round-tripped: %+v", preparer)
	}
	if !rs.Runner.Present || len(runner.NetworkDiagnostics) != 1 || runner.NetworkDiagnostics[0] != "listener is IPv4-only" {
		t.Fatalf("runner diagnostics not round-tripped: %+v", runner.NetworkDiagnostics)
	}
	if !rs.UpdatedAt.Present || rs.UpdatedAt.Value.IsZero() {
		t.Fatalf("expected non-zero HLC clock, got zero")
	}
}

// TestSecondaryOlderAssignmentDoesNotStompPinnedConfig ensures a TERMINATE
// assignment for an older spec version cannot replace the pinned config of a
// newer scheduled instance for the same deployment.
func TestSecondaryOlderAssignmentDoesNotStompPinnedConfig(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	store := Open(dbPath)

	v1Spec := *nonEmptySpec()
	v1Spec.Networking.Mode = apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL
	v1 := apigen.DeploymentRecord{
		Deployment: apigen.Deployment{ID: 12, Scheduling: apigen.DedicatedScheduling(false, 3), SpaceID: 1, Name: "tls-ingress-one", Spec: v1Spec},
		Meta:       apigen.EntityMeta{Version: 1, SpecVersion: 1},
	}
	v2 := v1
	v2.Meta.Version, v2.Meta.SpecVersion = 2, 2
	v2.Deployment.Spec.Networking.Ingress = []apigen.Ingress{{
		Hostname: "one.ingress.opendeploy.test",
		Config:   apigen.IngressConfig{Value: apigen.IngressConfigValueOneof{TlsPassthrough: &apigen.TlsPassthroughConfig{ContainerPort: 8443}}},
	}}

	store.MustWriteScheduledInstanceAssignment(&apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{
			ID: 14, Deployment: apigen.DeploymentRef{DeploymentID: 12, Version: 1}, NodeID: 3,
			State: apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
		},
		Config: v1,
	})
	store.MustWriteScheduledInstanceAssignment(&apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{
			ID: 17, Deployment: apigen.DeploymentRef{DeploymentID: 12, Version: 2}, NodeID: 3,
			State: apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
		},
		Config: v2,
	})
	store.MustWriteScheduledInstanceAssignment(&apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{
			ID: 14, Deployment: apigen.DeploymentRef{DeploymentID: 12, Version: 1}, NodeID: 3,
			State: apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE,
		},
		Config: v1,
	})

	assertPinnedAssignmentConfigs(t, store.FetchScheduledSnapshot(nil))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = Open(dbPath)
	t.Cleanup(func() { _ = store.Close() })
	assertPinnedAssignmentConfigs(t, store.FetchScheduledSnapshot(nil))
}

func assertPinnedAssignmentConfigs(t *testing.T, snapshot []apigen.ScheduledInstanceState) {
	t.Helper()
	byID := map[uint64]apigen.ScheduledInstanceState{}
	for _, item := range snapshot {
		byID[item.Instance.ID] = item
	}
	newer := byID[17]
	if newer.Config.Meta.SpecVersion != 2 {
		t.Fatalf("newer instance spec version = %d, want 2", newer.Config.Meta.SpecVersion)
	}
	if got := len(newer.Config.Deployment.Spec.Networking.Ingress); got != 1 {
		t.Fatalf("newer instance ingress count = %d, want 1 (older TERMINATE stomped pinned config)", got)
	}
	older := byID[14]
	if older.Config.Meta.SpecVersion != 1 || len(older.Config.Deployment.Spec.Networking.Ingress) != 0 {
		t.Fatalf("older terminate instance config = ver %d ingress %d, want v1 with no ingress",
			older.Config.Meta.SpecVersion, len(older.Config.Deployment.Spec.Networking.Ingress))
	}
}

// TestSecondaryFinalizeAbsentDropsInstanceDurably checks that an instance missing
// from the primary's snapshot is torn down rather than kept forever. The primary
// can never re-send a FINALIZED update for an instance it has already forgotten,
// so reconciling only on receipt would strand the assignment across restarts.
func TestSecondaryFinalizeAbsentDropsInstanceDurably(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	store := Open(dbPath)

	write := func(id, deploymentID uint64) {
		store.MustWriteScheduledInstanceAssignment(&apigen.ScheduledInstanceState{
			Instance: apigen.ScheduledInstance{
				ID: id, Deployment: apigen.DeploymentRef{DeploymentID: deploymentID, Version: 1}, NodeID: 5,
				State: apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
			},
			Config: apigen.DeploymentRecord{
				Deployment: apigen.Deployment{ID: deploymentID, Name: "app", Scheduling: apigen.DedicatedScheduling(false, 5), Spec: *nonEmptySpec()},
				Meta:       apigen.EntityMeta{Version: 1, SpecVersion: 1},
			},
		})
	}
	write(41, 8)
	write(42, 9)

	_, updates, unsub := store.MustFetchScheduledSnapshotAndSubscribe(nil)
	defer unsub()

	// The primary knows about 41 only.
	pruned := store.MustFinalizeScheduledInstancesAbsent(map[uint64]struct{}{41: {}})
	if len(pruned) != 1 || pruned[0] != 42 {
		t.Fatalf("pruned = %v, want [42]", pruned)
	}

	// The operator reacts only to Instance.State, so a FINALIZED update must be
	// published for the workload to actually be stopped.
	select {
	case batch := <-updates:
		if len(batch) != 1 || batch[0].Instance.ID != 42 {
			t.Fatalf("notified batch = %+v, want instance 42", batch)
		}
		if batch[0].Instance.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
			t.Fatalf("notified state = %v, want FINALIZED", batch[0].Instance.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no update published for the pruned instance")
	}

	if got := store.FetchScheduledSnapshot(nil); len(got) != 1 || got[0].Instance.ID != 41 {
		t.Fatalf("snapshot after prune = %+v, want only instance 41", got)
	}

	// Reopen: the durable row must be gone, not merely dropped from memory.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = Open(dbPath)
	t.Cleanup(func() { _ = store.Close() })
	if got := store.FetchScheduledSnapshot(nil); len(got) != 1 || got[0].Instance.ID != 41 {
		t.Fatalf("snapshot after reopen = %+v, want only instance 41", got)
	}
}

// nonEmptySpec returns a valid spec that encodes to non-empty bytes.
func nonEmptySpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{
		Workload: apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
			Source:          apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{RemoteImage: &apigen.RemoteImage{Image: "example/app"}}},
			Runtime:         apigen.ContainerRuntime{User: "1000"},
			UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE,
		}}},
		Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
	}
}

func testSpecWithVersion(version string) *apigen.DeploymentSpec {
	spec := nonEmptySpec()
	if err := spec.SetWorkloadVersion(version); err != nil {
		panic(err)
	}
	return spec
}
