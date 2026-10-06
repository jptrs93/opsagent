package scheduler

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/scheduledinstances"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func assertInstanceMutationsMatchRows(t *testing.T, store *state.Service, update state.WriteUpdate) {
	t.Helper()
	for _, m := range mutationsOf(update, instanceType) {
		persisted, err := store.Queries().GetScheduledInstance(context.Background(), m.EntityID())
		if errors.Is(err, sql.ErrNoRows) && m.Entity().Value.ScheduledInstance.State.IsFinal() {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if persisted.Seq != update.Seq || !bytes.Equal(persisted.Value.Encode(), m.Entity().Value.ScheduledInstance.Encode()) {
			t.Fatal("published instance differs from written row")
		}
	}
}

func TestDesiredAndStatusChangesIncludeImmediateSchedule(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := nodes.EnsurePrimaryNode(store, "primary", "primary", testUnderlay)
	startScheduler(t, store, newFakeBarrier())
	sub, unsub := store.SubscribeUpdates()
	defer unsub()
	before := globalSeq(t, store)
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, testRunningSpec("v1"))
	created := <-sub
	statetest.AssertUpdateMatchesRows(t, store, created)
	if created.Seq != before+1 || len(mutationsOf(created, deploymentType)) != 1 || len(mutationsOf(created, instanceType)) != 1 {
		t.Fatalf("create did not include scheduling: %+v", created)
	}
	inst := *mutationsOf(created, instanceType)[0].Entity().Value.ScheduledInstance
	markRunning(t, store, inst.ID, cfg.Meta.SpecVersion, apigen.RunningStatus_RUNNING_STATUS_RUNNING)
	running := <-sub
	statetest.AssertUpdateMatchesRows(t, store, running)
	if hasCore(running) || !running.Has(instanceStatusType) || running.Seq != created.Seq+1 {
		t.Fatalf("status with no target changes published core: %+v", running)
	}
	if globalSeq(t, store) != running.Seq {
		t.Fatal("observed commit and database disagree on sequence")
	}
	updated := statetest.UpdateDeploymentSpec(store, apigen.Context{}, cfg.Deployment.ID, testRunningSpec("v2"))
	stopping := <-sub
	statetest.AssertUpdateMatchesRows(t, store, stopping)
	stopped := mutationsOf(stopping, instanceType)
	if stopping.Seq != running.Seq+1 || len(mutationsOf(stopping, deploymentType)) != 1 || len(stopped) != 1 || stopped[0].Entity().Value.ScheduledInstance.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
		t.Fatalf("RECREATE did not atomically stop previous placement: %+v", stopping)
	}
	markRunning(t, store, inst.ID, cfg.Meta.SpecVersion, apigen.RunningStatus_RUNNING_STATUS_STOPPED)
	finalized := <-sub
	statetest.AssertUpdateMatchesRows(t, store, finalized)
	if finalized.Seq != stopping.Seq+1 || len(mutationsOf(finalized, instanceType)) != 2 || len(mutationsOf(finalized, instanceStatusType)) != 1 {
		t.Fatalf("finalization and replacement not one commit: %+v", finalized)
	}
	assertInstanceMutationsMatchRows(t, store, finalized)
	active := statetest.NonFinalInstances(store, cfg.Deployment.ID)
	if len(active) != 1 || active[0].ID == inst.ID || active[0].Deployment.Version != updated.Meta.Version {
		t.Fatalf("cache not final: %+v", active)
	}
	// The terminal observation and final target are visible to a later
	// subscriber, and both HLC history and authored history retain the retired
	// placement.
	if globalSeq(t, store) != finalized.Seq || len(erru.Must(store.Queries().ListScheduledInstanceStatusHistorySince(context.Background(), inst.ID, time.Time{}))) != 2 {
		t.Fatal("database or retained history disagrees with commit")
	}
	if liveEntity(t, store, instanceType, active[0].ID).Value.ScheduledInstance.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
		t.Fatal("bootstrap does not carry the replacement")
	}
}

func TestStaleReportIsDroppedAndCannotDriveScheduler(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := nodes.EnsurePrimaryNode(store, "primary", "primary", testUnderlay)
	barrier := newFakeBarrier()
	barrier.held = true
	startScheduler(t, store, barrier)
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, rolloverSpec("v1"))
	older := statetest.NonFinalInstances(store, cfg.Deployment.ID)[0]
	markRunning(t, store, older.ID, cfg.Meta.SpecVersion, apigen.RunningStatus_RUNNING_STATUS_RUNNING)
	current := *latestStatus(store, older.ID)
	updated := statetest.UpdateDeploymentSpec(store, apigen.Context{}, cfg.Deployment.ID, rolloverSpec("v2"))
	var standby uint64
	for _, inst := range statetest.NonFinalInstances(store, cfg.Deployment.ID) {
		if inst.ID != older.ID {
			standby = inst.ID
		}
	}
	before := fingerprint(t, store)
	beforeSeq := globalSeq(t, store)
	sub, unsub := store.SubscribeUpdates()
	defer unsub()
	stale := current
	stale.UpdatedAt = apigen.TimeOf(current.UpdatedAtTime().Add(-time.Nanosecond))
	stale.Runner.Value.Status = apigen.RunningStatus_RUNNING_STATUS_STOPPED
	scheduledinstances.WriteReplicatedStatus(store, &stale)
	if !bytes.Equal(before, fingerprint(t, store)) {
		t.Fatal("delayed STOPPED report changed the bootstrap or the sequence")
	}
	if len(erru.Must(store.Queries().ListScheduledInstanceStatusHistorySince(context.Background(), older.ID, time.Time{}))) != 1 {
		t.Fatal("delayed report entered history")
	}
	select {
	case <-sub:
		t.Fatal("stale report published")
	default:
	}
	markRunning(t, store, standby, updated.Meta.SpecVersion, apigen.RunningStatus_RUNNING_STATUS_RUNNING)
	promoted := <-sub
	statetest.AssertUpdateMatchesRows(t, store, promoted)
	if len(mutationsOf(promoted, instanceType)) != 2 || !promoted.Has(instanceStatusType) || promoted.Seq != beforeSeq+1 {
		t.Fatalf("promotion is not atomic: %+v", promoted)
	}
	assertInstanceMutationsMatchRows(t, store, promoted)
}

func TestDrainDeadlineSurvivesRepeatedTriggers(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	ctx := context.Background()
	node := nodes.EnsurePrimaryNode(store, "primary", "primary", testUnderlay)
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
	older := statetest.NonFinalInstances(store, cfg.Deployment.ID)[0]
	updated := statetest.UpdateDeploymentSpec(store, apigen.Context{}, cfg.Deployment.ID, rolloverSpec("v2"))
	var newer uint64
	for _, inst := range statetest.NonFinalInstances(store, cfg.Deployment.ID) {
		if inst.ID != older.ID {
			newer = inst.ID
		}
	}
	markRunning(t, store, newer, updated.Meta.SpecVersion, apigen.RunningStatus_RUNNING_STATUS_RUNNING)
	drain := erru.Must(store.Queries().GetScheduledInstance(context.Background(), older.ID))
	if drain.Value.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		t.Fatalf("older placement = %v, want draining", drain.Value.State)
	}
	// The deadline is measured from the persisted event time, which the
	// promoting commit stamped, not from the scheduler clock.
	drainedAt := time.UnixMilli(drain.EventTime)
	before := globalSeq(t, store)
	now = drainedAt.Add(drainTimeout - time.Millisecond)
	sweep(t, store)
	sweep(t, store)
	if globalSeq(t, store) != before || !reflect.DeepEqual(drain, erru.Must(store.Queries().GetScheduledInstance(context.Background(), older.ID))) {
		t.Fatal("reconcile reset persisted wait or consumed a sequence")
	}
	now = drainedAt.Add(drainTimeout + time.Millisecond)
	sweep(t, store)
	if erru.Must(store.Queries().GetScheduledInstance(context.Background(), older.ID)).Value.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
		t.Fatal("persisted deadline did not retire drain")
	}
}
