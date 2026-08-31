package state

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestFlipScheduledInstanceServing(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := store.EnsurePrimaryNode("primary", "primary")
	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, DefaultSpaceID, "app", node.ID, nonEmptySpec())

	serving := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	standby := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY)

	seq := store.FlipScheduledInstanceServing([]int32{serving.ID}, standby.ID)
	if seq == 0 {
		t.Fatal("flip allocated no sequence")
	}
	states := map[int32]apigen.ScheduledInstanceTarget{}
	for _, st := range store.FetchScheduledSnapshot(nil) {
		states[st.Instance.ID] = st.Instance.State
	}
	if got := states[serving.ID]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		t.Fatalf("old placement state = %v, want RUN_DRAINING", got)
	}
	if got := states[standby.ID]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
		t.Fatalf("promoted placement state = %v, want RUN_SERVING", got)
	}
	if again := store.FlipScheduledInstanceServing([]int32{serving.ID}, standby.ID); again != 0 {
		t.Fatalf("no-op flip allocated sequence %d", again)
	}
}

func TestFlipScheduledInstanceServingSnapshotAtomicity(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := store.EnsurePrimaryNode("primary", "primary")
	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, DefaultSpaceID, "app", node.ID, nonEmptySpec())

	current := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)

	var violations atomic.Int64
	var snapshots atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			servingCount := 0
			for _, st := range store.FetchScheduledSnapshot(nil) {
				if st.Instance.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
					servingCount++
				}
			}
			snapshots.Add(1)
			if servingCount != 1 {
				violations.Add(1)
			}
		}
	}()

	for i := 0; i < 50; i++ {
		standby := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY)
		if seq := store.FlipScheduledInstanceServing([]int32{current.ID}, standby.ID); seq == 0 {
			t.Fatalf("flip %d allocated no sequence", i)
		}
		store.SetScheduledInstanceState(current.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE)
		store.SetScheduledInstanceState(current.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
		current = standby
	}
	close(stop)
	wg.Wait()
	if snapshots.Load() == 0 {
		t.Fatal("snapshot reader never ran")
	}
	if n := violations.Load(); n != 0 {
		t.Fatalf("%d of %d snapshots observed a half-applied flip", n, snapshots.Load())
	}
}
