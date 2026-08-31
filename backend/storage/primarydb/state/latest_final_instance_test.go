package state

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

func instanceIDs(states []apigen.ScheduledInstanceState) []int32 {
	out := make([]int32, 0, len(states))
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
	return &apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{
		Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "example/app"}},
		Version: "v1",
		Running: true,
	}}
}

func seedDeployment(t *testing.T, store *Service, name string) *apigen.Deployment {
	t.Helper()
	node := store.EnsurePrimaryNode("primary", "primary")
	return store.MustCreateDeploymentForNode(apigen.Context{}, DefaultSpaceID, name, node.ID, runningDeploymentSpec())
}

func writeRunnerStatus(t *testing.T, store *Service, instanceID int32, status apigen.RunningStatus) {
	t.Helper()
	store.MustWriteScheduledInstanceStatus(instanceID, func(st *apigen.ScheduledInstanceStatus) bool {
		st.BumpUpdatedAt()
		st.Runner = apigen.RunnerStatus{Status: status, RunningPid: 4242}
		return true
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

	inst := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	writeRunnerStatus(t, store, inst.ID, apigen.RunningStatus_STOPPED)
	store.SetScheduledInstanceState(inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)

	if live := store.FetchScheduledSnapshot(nil); len(live) != 0 {
		t.Fatalf("reconciliation snapshot = %v, want empty", instanceIDs(live))
	}

	shown := onlyInstance(t, store.FetchScheduledSnapshotWithLatestFinal(nil))
	if shown.Instance.ID != inst.ID {
		t.Fatalf("displayed instance = %d, want %d", shown.Instance.ID, inst.ID)
	}
	if shown.Status.Runner.Status != apigen.RunningStatus_STOPPED {
		t.Fatalf("displayed status = %v, want the STOPPED it ended on", shown.Status.Runner.Status)
	}
	if shown.Config.ID != cfg.ID {
		t.Fatal("displayed instance lost the config version it was pinned to")
	}
}

// TestRetainedFinalInstanceSurvivesRestart: the retained view is memory, and the
// UI must not go blank for every stopped deployment because the primary
// restarted.
func TestRetainedFinalInstanceSurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := Open(dbPath)
	cfg := seedDeployment(t, store, "app")
	inst := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	writeRunnerStatus(t, store, inst.ID, apigen.RunningStatus_STOPPED)
	store.SetScheduledInstanceState(inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	store = Open(dbPath)
	t.Cleanup(func() { _ = store.Close() })

	if live := store.FetchScheduledSnapshot(nil); len(live) != 0 {
		t.Fatalf("reconciliation snapshot after restart = %v, want empty", instanceIDs(live))
	}
	shown := onlyInstance(t, store.FetchScheduledSnapshotWithLatestFinal(nil))
	if shown.Instance.ID != inst.ID {
		t.Fatalf("displayed instance after restart = %d, want %d", shown.Instance.ID, inst.ID)
	}
	if shown.Status.Runner.Status != apigen.RunningStatus_STOPPED {
		t.Fatalf("displayed status after restart = %v, want STOPPED", shown.Status.Runner.Status)
	}
}

// TestNewInstanceEvictsTheRetainedRun keeps the retained view at one entry per
// ordinal. Without eviction every incarnation a deployment ever ran would stack
// up in the row.
func TestNewInstanceEvictsTheRetainedRun(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	cfg := seedDeployment(t, store, "app")

	older := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	store.SetScheduledInstanceState(older.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	newer := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)

	shown := onlyInstance(t, store.FetchScheduledSnapshotWithLatestFinal(nil))
	if shown.Instance.ID != newer.ID {
		t.Fatalf("displayed instance = %d, want the live %d", shown.Instance.ID, newer.ID)
	}

	// Finalizing the run a live instance already replaced must not resurrect it
	// into the ordinal's slot: RECREATE creates the replacement in the same pass
	// that retires the placement it supersedes, so this ordering is the norm.
	store.SetScheduledInstanceState(older.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	shown = onlyInstance(t, store.FetchScheduledSnapshotWithLatestFinal(nil))
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

	first := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	second := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 1, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	store.SetScheduledInstanceState(first.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	store.SetScheduledInstanceState(second.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)

	shown := store.FetchScheduledSnapshotWithLatestFinal(nil)
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

	shownInst := store.CreateScheduledInstance(visible.ID, visible.Version, visible.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	hiddenInst := store.CreateScheduledInstance(hidden.ID, hidden.Version, hidden.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	store.SetScheduledInstanceState(shownInst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	store.SetScheduledInstanceState(hiddenInst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)

	predicate := storage.ScheduledInstancePredicate(func(state apigen.ScheduledInstanceState) bool {
		return state.Instance.DeploymentID == visible.ID
	})
	got := onlyInstance(t, store.FetchScheduledSnapshotWithLatestFinal(predicate))
	if got.Instance.ID != shownInst.ID {
		t.Fatalf("displayed instance = %d, want %d", got.Instance.ID, shownInst.ID)
	}
}
