package scheduler

import (
	"bytes"
	"context"
	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/scheduledinstances"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func TestDesiredAndStatusChangesIncludeImmediateSchedule(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	ctx := context.Background()
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	startScheduler(t, store, newFakeBarrier())
	sub, unsub := store.SubscribeUpdates()
	defer unsub()
	before := store.BuildSnapshot(ctx).Seq
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, testRunningSpec("v1"))
	created := <-sub
	if created.Seq != before+1 || !created.HasCore() || len(created.DeploymentEvents) != 1 || len(created.ScheduledInstanceEvents) != 1 {
		t.Fatalf("create did not include scheduling: %+v", created)
	}
	inst := created.ScheduledInstanceEvents[0].Value
	markRunning(t, store, inst.ID, cfg.SpecVersion, apigen.RunningStatus_RUNNING)
	running := <-sub
	if running.HasCore() || !running.HasObserved() || running.Seq != created.Seq+1 {
		t.Fatalf("status with no target changes published core: %+v", running)
	}
	if store.BuildSnapshot(ctx).Seq != running.Seq {
		t.Fatal("observed commit and snapshot disagree on sequence")
	}
	updated := statetest.UpdateDeploymentSpec(store, apigen.Context{}, cfg.DeploymentID, testRunningSpec("v2"))
	stopping := <-sub
	if stopping.Seq != running.Seq+1 || !stopping.HasCore() || len(stopping.DeploymentEvents) != 1 || len(stopping.ScheduledInstanceEvents) != 1 || stopping.ScheduledInstanceEvents[0].Value.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
		t.Fatalf("RECREATE did not atomically stop previous placement: %+v", stopping)
	}
	markRunning(t, store, inst.ID, cfg.SpecVersion, apigen.RunningStatus_STOPPED)
	finalized := <-sub
	if finalized.Seq != stopping.Seq+1 || !finalized.HasCore() || len(finalized.ScheduledInstanceEvents) != 2 || !finalized.HasObserved() || len(finalized.InstanceStatuses) != 1 {
		t.Fatalf("finalization and replacement not one commit: %+v", finalized)
	}
	for _, event := range finalized.ScheduledInstanceEvents {
		persisted := erru.Must(store.Queries().GetScheduledInstance(context.Background(), event.ScheduledInstanceID))
		if event.Seq != finalized.Seq || !bytes.Equal(persisted.Encode(), event.Encode()) {
			t.Fatal("published event differs from written row")
		}
	}
	active := statetest.NonFinalInstances(store, cfg.DeploymentID)
	if len(active) != 1 || active[0].ID == inst.ID || active[0].DeploymentVersion != updated.Version {
		t.Fatalf("cache not final: %+v", active)
	}
	// The terminal observation and final target are visible in one later snapshot,
	// and both HLC history and authored history retain the retired placement.
	snapshot := store.BuildSnapshot(ctx)
	if snapshot.Seq != finalized.Seq || len(erru.Must(store.Queries().ListScheduledInstanceStatusHistorySince(context.Background(), inst.ID, time.Time{}))) != 2 {
		t.Fatal("snapshot or retained history disagrees with commit")
	}
}

func TestStaleReportIsHistoryAndCannotDriveScheduler(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	ctx := context.Background()
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	barrier := newFakeBarrier()
	barrier.held = true
	startScheduler(t, store, barrier)
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, rolloverSpec("v1"))
	older := statetest.NonFinalInstances(store, cfg.DeploymentID)[0]
	markRunning(t, store, older.ID, cfg.SpecVersion, apigen.RunningStatus_RUNNING)
	current := *latestStatus(store, older.ID)
	updated := statetest.UpdateDeploymentSpec(store, apigen.Context{}, cfg.DeploymentID, rolloverSpec("v2"))
	var standby int32
	for _, inst := range statetest.NonFinalInstances(store, cfg.DeploymentID) {
		if inst.ID != older.ID {
			standby = inst.ID
		}
	}
	before := store.BuildSnapshot(ctx)
	sub, unsub := store.SubscribeUpdates()
	defer unsub()
	stale := current
	stale.UpdatedAt = current.UpdatedAt.Add(-time.Nanosecond)
	stale.Runner.Status = apigen.RunningStatus_STOPPED
	scheduledinstances.WriteReplicatedStatus(store, &stale)
	if !bytes.Equal(before.Encode(), store.BuildSnapshot(ctx).Encode()) {
		t.Fatal("delayed STOPPED report changed snapshot")
	}
	if len(erru.Must(store.Queries().ListScheduledInstanceStatusHistorySince(context.Background(), older.ID, time.Time{}))) != 2 {
		t.Fatal("delayed history discarded")
	}
	select {
	case <-sub:
		t.Fatal("stale report published")
	default:
	}
	markRunning(t, store, standby, updated.SpecVersion, apigen.RunningStatus_RUNNING)
	promoted := <-sub
	if !promoted.HasCore() || len(promoted.ScheduledInstanceEvents) != 2 || !promoted.HasObserved() || promoted.Seq != before.Seq+1 {
		t.Fatalf("promotion is not atomic: %+v", promoted)
	}
	for _, event := range promoted.ScheduledInstanceEvents {
		if event.Seq != promoted.Seq || !bytes.Equal(event.Encode(), erru.Must(store.Queries().GetScheduledInstance(context.Background(), event.ScheduledInstanceID)).Encode()) {
			t.Fatal("promotion publication differs from rows")
		}
	}
}

func TestDrainDeadlineSurvivesRepeatedTriggers(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	ctx := context.Background()
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	barrier := newFakeBarrier()
	barrier.held = true
	scheduling := New(store, barrier)
	now := time.Now().Truncate(time.Millisecond)
	scheduling.now = func() time.Time { return now }
	if err := scheduling.Start(ctx); err != nil {
		t.Fatal(err)
	}
	testSchedulers.Store(store, scheduling)
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, rolloverSpec("v1"))
	older := statetest.NonFinalInstances(store, cfg.DeploymentID)[0]
	updated := statetest.UpdateDeploymentSpec(store, apigen.Context{}, cfg.DeploymentID, rolloverSpec("v2"))
	var newer int32
	for _, inst := range statetest.NonFinalInstances(store, cfg.DeploymentID) {
		if inst.ID != older.ID {
			newer = inst.ID
		}
	}
	markRunning(t, store, newer, updated.SpecVersion, apigen.RunningStatus_RUNNING)
	drain := erru.Must(store.Queries().GetScheduledInstance(context.Background(), older.ID))
	before := store.BuildSnapshot(ctx).Seq
	now = now.Add(drainTimeout - time.Millisecond)
	sweep(t, store)
	sweep(t, store)
	if store.BuildSnapshot(ctx).Seq != before || !bytes.Equal(drain.Encode(), erru.Must(store.Queries().GetScheduledInstance(context.Background(), older.ID)).Encode()) {
		t.Fatal("reconcile reset persisted wait or consumed a sequence")
	}
	now = now.Add(2 * time.Millisecond)
	sweep(t, store)
	if erru.Must(store.Queries().GetScheduledInstance(context.Background(), older.ID)).Value.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
		t.Fatal("persisted deadline did not retire drain")
	}
}
