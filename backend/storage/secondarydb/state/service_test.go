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

	cfg := apigen.Deployment{
		ID:          7,
		SpecVersion: 3,
		EventTime:   time.UnixMilli(1000),
		Def:         apigen.DeploymentDef{NodeID: 23, SpaceID: 1, Name: "api", Spec: *testSpecWithState("v3", true)},
	}
	const instanceID int32 = 11
	store.MustWriteScheduledInstanceAssignment(&apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{
			ID:                    instanceID,
			NodeID:                23,
			DeploymentID:          7,
			DeploymentSpecVersion: 3,
			State:                 apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
		},
		Config: cfg,
	})
	store.MustWriteScheduledInstanceStatus(instanceID, func(s *apigen.ScheduledInstanceStatus) bool {
		s.BumpUpdatedAt()
		s.Preparer = apigen.PreparerStatus{
			DeploymentSpecVersion: 3,
			Artifact:              "art",
			Inputs:                apigen.InputsStatus_INPUTS_READY,
			Image:                 apigen.ImageStatus_IMAGE_READY,
		}
		s.Runner = apigen.RunnerStatus{
			DeploymentSpecVersion: 3,
			Status:                apigen.RunningStatus_RUNNING,
			NetworkDiagnostics:    []string{"listener is IPv4-only"},
		}
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
	if rc.Def.NodeID != 23 || rc.SpecVersion != 3 || rc.Def.SpaceID != 1 || rc.Def.Name != "api" {
		t.Fatalf("config not round-tripped: %+v", rc)
	}
	rs := got[0].Status
	if rs.Preparer.Rollup() != apigen.PreparationStatus_READY || rs.Preparer.Artifact != "art" {
		t.Fatalf("status not round-tripped: %+v", rs)
	}
	if rs.Preparer.Inputs != apigen.InputsStatus_INPUTS_READY || rs.Preparer.Image != apigen.ImageStatus_IMAGE_READY {
		t.Fatalf("preparer stages not round-tripped: %+v", rs.Preparer)
	}
	if len(rs.Runner.NetworkDiagnostics) != 1 || rs.Runner.NetworkDiagnostics[0] != "listener is IPv4-only" {
		t.Fatalf("runner diagnostics not round-tripped: %+v", rs.Runner.NetworkDiagnostics)
	}
	if rs.UpdatedAt.IsZero() {
		t.Fatalf("expected non-zero HLC clock, got zero")
	}
}

// TestSecondaryOlderAssignmentDoesNotStompPinnedConfig ensures a TERMINATE
// assignment for an older spec version cannot replace the pinned config of a
// newer scheduled instance for the same deployment.
func TestSecondaryOlderAssignmentDoesNotStompPinnedConfig(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	store := Open(dbPath)

	v1 := apigen.Deployment{
		ID:          12,
		SpecVersion: 1,
		Def:         apigen.DeploymentDef{NodeID: 3, SpaceID: 1, Name: "tls-ingress-one", Spec: apigen.DeploymentSpec{Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL}}},
	}
	v2 := v1
	v2.SpecVersion = 2
	v2.Def.Spec.Networking.Ingress = []*apigen.Ingress{{
		Kind:                 apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
		Hostname:             "one.ingress.opendeploy.test",
		TlsPassthroughConfig: &apigen.TlsPassthroughConfig{ContainerPort: 8443},
	}}

	store.MustWriteScheduledInstanceAssignment(&apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{
			ID: 14, DeploymentID: 12, DeploymentSpecVersion: 1, NodeID: 3,
			State: apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
		},
		Config: v1,
	})
	store.MustWriteScheduledInstanceAssignment(&apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{
			ID: 17, DeploymentID: 12, DeploymentSpecVersion: 2, NodeID: 3,
			State: apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
		},
		Config: v2,
	})
	store.MustWriteScheduledInstanceAssignment(&apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{
			ID: 14, DeploymentID: 12, DeploymentSpecVersion: 1, NodeID: 3,
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
	byID := map[int32]apigen.ScheduledInstanceState{}
	for _, item := range snapshot {
		byID[item.Instance.ID] = item
	}
	newer := byID[17]
	if newer.Config.SpecVersion != 2 {
		t.Fatalf("newer instance spec version = %d, want 2", newer.Config.SpecVersion)
	}
	if got := len(newer.Config.Def.Spec.Networking.Ingress); got != 1 {
		t.Fatalf("newer instance ingress count = %d, want 1 (older TERMINATE stomped pinned config)", got)
	}
	older := byID[14]
	if older.Config.SpecVersion != 1 || len(older.Config.Def.Spec.Networking.Ingress) != 0 {
		t.Fatalf("older terminate instance config = ver %d ingress %d, want v1 with no ingress",
			older.Config.SpecVersion, len(older.Config.Def.Spec.Networking.Ingress))
	}
}

// TestSecondaryFinalizeAbsentDropsInstanceDurably checks that an instance missing
// from the primary's snapshot is torn down rather than kept forever. The primary
// can never re-send a FINALIZED update for an instance it has already forgotten,
// so reconciling only on receipt would strand the assignment across restarts.
func TestSecondaryFinalizeAbsentDropsInstanceDurably(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	store := Open(dbPath)

	write := func(id, deploymentID int32) {
		store.MustWriteScheduledInstanceAssignment(&apigen.ScheduledInstanceState{
			Instance: apigen.ScheduledInstance{
				ID: id, DeploymentID: deploymentID, DeploymentSpecVersion: 1, NodeID: 5,
				State: apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
			},
			Config: apigen.Deployment{ID: deploymentID, SpecVersion: 1, Def: apigen.DeploymentDef{NodeID: 5, Spec: *nonEmptySpec()}},
		})
	}
	write(41, 8)
	write(42, 9)

	_, updates, unsub := store.MustFetchScheduledSnapshotAndSubscribe(nil)
	defer unsub()

	// The primary knows about 41 only.
	pruned := store.MustFinalizeScheduledInstancesAbsent(map[int32]struct{}{41: {}})
	if len(pruned) != 1 || pruned[0] != 42 {
		t.Fatalf("pruned = %v, want [42]", pruned)
	}

	// The operator reacts only to Instance.State, so a FINALIZED update must be
	// published for the workload to actually be stopped.
	select {
	case got := <-updates:
		if got.Instance.ID != 42 {
			t.Fatalf("notified instance = %d, want 42", got.Instance.ID)
		}
		if got.Instance.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
			t.Fatalf("notified state = %v, want FINALIZED", got.Instance.State)
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
		Container1Spec: &apigen.ContainerSpec{
			Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "example/app"}},
			Runtime: apigen.ContainerRuntime{User: "1000"},
		},
		Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
	}
}

func testSpecWithState(version string, running bool) *apigen.DeploymentSpec {
	spec := nonEmptySpec()
	if err := spec.SetWorkloadState(version, running); err != nil {
		panic(err)
	}
	return spec
}
