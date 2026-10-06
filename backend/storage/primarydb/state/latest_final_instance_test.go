package state

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jptrs93/goutil/erru"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

func instanceIDs(states []apigen.ScheduledInstanceState) []uint64 {
	out := make([]uint64, 0, len(states))
	for _, state := range states {
		out = append(out, state.Instance.ID)
	}
	return out
}

func onlyInstance(t *testing.T, states []apigen.ScheduledInstanceState) apigen.ScheduledInstanceState {
	t.Helper()
	if len(states) != 1 {
		t.Fatalf("instances = %v, want exactly one", instanceIDs(states))
	}
	return states[0]
}

func runningDeploymentSpec() *apigen.DeploymentSpec {
	return testSpecWithVersion("v1")
}

func seedDeployment(t *testing.T, store *Service, name string) *apigen.DeploymentRecord {
	t.Helper()
	node := testNode(store, "primary")
	return mustCreateDeploymentForNode(store, apigen.Context{}, defaultSpaceID, name, node.ID, runningDeploymentSpec())
}

func writeRunnerStatus(t *testing.T, store *Service, instanceID uint64, status apigen.RunningStatus) {
	t.Helper()
	writeInstanceStatusForTest(store, instanceID, func(st *apigen.ScheduledInstanceStatus) {
		st.BumpUpdatedAt()
		st.Runner = runnerStatus(status, 4242)
	})
}

// TestFinalizedInstanceIsRetainedForDisplay covers the split the display view
// exists for: reconciliation must never see a finalized placement, because it
// owns no address and running anything for it would be wrong, while the UI has
// nothing else to show for a deployment that is deliberately stopped.
func TestFinalizedInstanceIsRetainedForDisplay(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	cfg := seedDeployment(t, store, "app")

	inst := createScheduledInstanceForTest(store, cfg.Deployment.ID, cfg.Meta.Version, cfg.Deployment.PlacementNodeID(), 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	writeRunnerStatus(t, store, inst.ID, apigen.RunningStatus_RUNNING_STATUS_STOPPED)
	setScheduledInstanceState(store, inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)

	if live := store.FetchScheduledSnapshot(nil); len(live) != 0 {
		t.Fatalf("reconciliation snapshot = %v, want empty", instanceIDs(live))
	}

	shown := onlyInstance(t, snapshotInstances(store, nil))
	if shown.Instance.ID != inst.ID {
		t.Fatalf("displayed instance = %d, want %d", shown.Instance.ID, inst.ID)
	}
	if shown.Status.Value.Runner.Value.Status != apigen.RunningStatus_RUNNING_STATUS_STOPPED {
		t.Fatalf("displayed status = %v, want the STOPPED it ended on", shown.Status.Value.Runner.Value.Status)
	}
	if shown.Config.Deployment.ID != cfg.Deployment.ID {
		t.Fatal("displayed instance lost the spec version it was pinned to")
	}
}

// TestRetainedFinalInstanceSurvivesRestart: the retained view is memory, and the
// UI must not go blank for every stopped deployment because the primary
// restarted.
func TestRetainedFinalInstanceSurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := Open(dbPath)
	cfg := seedDeployment(t, store, "app")
	inst := createScheduledInstanceForTest(store, cfg.Deployment.ID, cfg.Meta.Version, cfg.Deployment.PlacementNodeID(), 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	writeRunnerStatus(t, store, inst.ID, apigen.RunningStatus_RUNNING_STATUS_STOPPED)
	setScheduledInstanceState(store, inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	store = Open(dbPath)
	t.Cleanup(func() { _ = store.Close() })

	if live := store.FetchScheduledSnapshot(nil); len(live) != 0 {
		t.Fatalf("reconciliation snapshot after restart = %v, want empty", instanceIDs(live))
	}
	shown := onlyInstance(t, snapshotInstances(store, nil))
	if shown.Instance.ID != inst.ID {
		t.Fatalf("displayed instance after restart = %d, want %d", shown.Instance.ID, inst.ID)
	}
	if shown.Status.Value.Runner.Value.Status != apigen.RunningStatus_RUNNING_STATUS_STOPPED {
		t.Fatalf("displayed status after restart = %v, want STOPPED", shown.Status.Value.Runner.Value.Status)
	}
}

// TestNewInstanceEvictsTheRetainedRun keeps the retained view at one entry per
// ordinal. Without eviction every incarnation a deployment ever ran would stack
// up in the row.
func TestNewInstanceEvictsTheRetainedRun(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	cfg := seedDeployment(t, store, "app")

	older := createScheduledInstanceForTest(store, cfg.Deployment.ID, cfg.Meta.Version, cfg.Deployment.PlacementNodeID(), 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	setScheduledInstanceState(store, older.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	newer := createScheduledInstanceForTest(store, cfg.Deployment.ID, cfg.Meta.Version, cfg.Deployment.PlacementNodeID(), 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)

	shown := onlyInstance(t, snapshotInstances(store, nil))
	if shown.Instance.ID != newer.ID {
		t.Fatalf("displayed instance = %d, want the live %d", shown.Instance.ID, newer.ID)
	}

	// Finalizing the run a live instance already replaced must not resurrect it
	// into the ordinal's slot: RECREATE creates the replacement in the same pass
	// that retires the placement it supersedes, so this ordering is the norm.
	setScheduledInstanceState(store, older.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	shown = onlyInstance(t, snapshotInstances(store, nil))
	if shown.Instance.ID != newer.ID {
		t.Fatalf("displayed instance = %d, want the live %d", shown.Instance.ID, newer.ID)
	}
}

// TestRetainedRunIsPerOrdinal: ordinals are independent slots, so one stopping
// must not blank out another's last run.
func TestRetainedRunIsPerOrdinal(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	cfg := seedDeployment(t, store, "app")

	first := createScheduledInstanceForTest(store, cfg.Deployment.ID, cfg.Meta.Version, cfg.Deployment.PlacementNodeID(), 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	second := createScheduledInstanceForTest(store, cfg.Deployment.ID, cfg.Meta.Version, cfg.Deployment.PlacementNodeID(), 1, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	setScheduledInstanceState(store, first.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	setScheduledInstanceState(store, second.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)

	shown := snapshotInstances(store, nil)
	if len(shown) != 2 {
		t.Fatalf("displayed instances = %v, want the last run of both ordinals", instanceIDs(shown))
	}
}

// TestDisplaySnapshotAppliesPredicate: the predicate is an access boundary on
// every other snapshot path, so the retained entries must not slip past it.
func TestDisplaySnapshotAppliesPredicate(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	visible := seedDeployment(t, store, "visible")
	hidden := seedDeployment(t, store, "hidden")

	shownInst := createScheduledInstanceForTest(store, visible.Deployment.ID, visible.Meta.Version, visible.Deployment.PlacementNodeID(), 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	hiddenInst := createScheduledInstanceForTest(store, hidden.Deployment.ID, hidden.Meta.Version, hidden.Deployment.PlacementNodeID(), 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	setScheduledInstanceState(store, shownInst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	setScheduledInstanceState(store, hiddenInst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)

	predicate := storage.ScheduledInstancePredicate(func(state apigen.ScheduledInstanceState) bool {
		return state.Instance.Deployment.DeploymentID == visible.Deployment.ID
	})
	got := onlyInstance(t, snapshotInstances(store, predicate))
	if got.Instance.ID != shownInst.ID {
		t.Fatalf("displayed instance = %d, want %d", got.Instance.ID, shownInst.ID)
	}
}

func snapshotInstances(store *Service, predicate storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState {
	entries := erru.Must(store.q.Snapshot(context.Background()))
	type pin struct {
		deployment uint64
		version    uint32
	}
	deployments := map[pin]*apigen.DeploymentRecord{}
	instances := map[uint64]*apigen.ScheduledInstance{}
	statuses := map[uint64]*apigen.ScheduledInstanceStatus{}
	var order []uint64
	for _, e := range entries {
		entity, meta := e.Entity.Value, e.Meta
		switch {
		case entity.Deployment != nil:
			deployments[pin{e.EntityID, meta.Version}] = &apigen.DeploymentRecord{Deployment: *entity.Deployment, Meta: meta}
		case entity.ScheduledInstance != nil:
			if _, seen := instances[e.EntityID]; !seen {
				order = append(order, e.EntityID)
			}
			instances[e.EntityID] = entity.ScheduledInstance
		case entity.ScheduledInstanceStatus != nil:
			statuses[e.EntityID] = entity.ScheduledInstanceStatus
		}
	}
	out := []apigen.ScheduledInstanceState{}
	for _, id := range order {
		inst := instances[id]
		st := apigen.ScheduledInstanceState{Instance: *inst}
		if d := deployments[pin{inst.Deployment.DeploymentID, inst.Deployment.Version}]; d != nil {
			st.Config = *d
		}
		if status := statuses[id]; status != nil {
			st.Status = apigen.Some(*status)
		}
		if predicate == nil || predicate(st) {
			out = append(out, st)
		}
	}
	return out
}
