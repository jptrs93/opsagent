package scheduler

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/scheduledinstances"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func latestStatus(s *state.Service, instanceID uint64) *apigen.ScheduledInstanceStatus {
	st, err := s.Queries().GetLatestScheduledInstanceStatus(context.Background(), instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return erru.Must(st, err)
}

func TestInvalidateNodeRuntimeStatePreservesConfigAndHistory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := state.Open(dbPath)
	defer func() { _ = store.Close() }()

	primaryNode := nodes.EnsurePrimaryNode(store, "primary", "primary", testUnderlay, "")
	secondaryNode := nodes.EnsurePrimaryNode(store, "secondary", "secondary", testUnderlay, "")
	create := func(nodeID uint64, name string, spec *apigen.DeploymentSpec) *apigen.DeploymentRecord {
		return statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, name, nodeID, spec)
	}
	containerSpec := statetest.SpecWithVersion("v1")
	primary := create(primaryNode.ID, "app", containerSpec)
	secondary := create(secondaryNode.ID, "app", containerSpec)
	system := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, internaldeploy.SpaceID, internaldeploy.SelfName, primaryNode.ID, testSystemSpecWithVersion("v1"))

	seedStatus := func(cfg *apigen.DeploymentRecord, artifact string) *apigen.ScheduledInstance {
		inst := statetest.CreateScheduledInstance(store, cfg.Deployment.ID, cfg.Meta.Version, cfg.Deployment.PlacementNodeID(), 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
		scheduledinstances.WriteStatus(store, inst.ID, func(status *apigen.ScheduledInstanceStatus) bool {
			status.BumpUpdatedAt()
			status.Preparer = apigen.Some(apigen.PreparerStatus{DeploymentSpecVersion: cfg.Meta.SpecVersion, Artifact: artifact, Inputs: apigen.InputsStatus_INPUTS_STATUS_READY, Image: apigen.Some(apigen.ImageStatus_IMAGE_STATUS_READY)})
			status.Runner = apigen.Some(apigen.RunnerStatus{DeploymentSpecVersion: cfg.Meta.SpecVersion, RunningArtifact: artifact, Status: apigen.RunningStatus_RUNNING_STATUS_RUNNING})
			return true
		})
		return inst
	}
	primaryInst := seedStatus(primary, "example/app:v1")
	seedStatus(secondary, "example/app:v1")
	seedStatus(system, "/var/lib/opendeploy/releases/v1/opendeploy")

	primaryConfigHistoryCount := len(erru.Must(store.Queries().ListDeploymentHistory(context.Background(), primary.Deployment.ID)))
	primaryConfigVersion := primary.Meta.SpecVersion

	count, err := nodes.InvalidateNodeRuntimeState(store, primaryNode.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("invalidated count = %d, want 1", count)
	}
	got := latestStatus(store, primaryInst.ID)
	if got != nil && (got.Preparer.Present || got.Runner.Present) {
		t.Fatalf("primary runtime status was not cleared: %+v", got)
	}
	if len(erru.Must(store.Queries().ListDeploymentHistory(context.Background(), primary.Deployment.ID))) != primaryConfigHistoryCount {
		t.Fatal("runtime invalidation changed config history")
	}
	secondaryInst := statetest.NonFinalInstances(store, secondary.Deployment.ID)[0]
	if latestStatus(store, secondaryInst.ID).Runner.Value.Status != apigen.RunningStatus_RUNNING_STATUS_RUNNING {
		t.Fatal("secondary runtime status was cleared")
	}
	systemInst := statetest.NonFinalInstances(store, system.Deployment.ID)[0]
	if latestStatus(store, systemInst.ID).Runner.Value.Status != apigen.RunningStatus_RUNNING_STATUS_RUNNING {
		t.Fatal("primary system deployment runtime status was cleared")
	}
	for _, cfg := range erru.Must(store.Queries().ListActiveDeployments(context.Background())) {
		if cfg.Deployment.ID == primary.Deployment.ID && cfg.Meta.SpecVersion != primaryConfigVersion {
			t.Fatalf("primary spec version = %d, want %d", cfg.Meta.SpecVersion, primaryConfigVersion)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = state.Open(dbPath)
	got = latestStatus(store, primaryInst.ID)
	if got != nil && (got.Preparer.Present || got.Runner.Present) {
		t.Fatalf("persisted primary runtime status was not cleared correctly: %+v", got)
	}
}

func TestEnsureRunScheduledInstanceIsConcurrentAndIdempotent(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := nodes.EnsurePrimaryNode(store, "primary", "primary-id", testUnderlay, "")
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "api", node.ID, statetest.SpecWithVersion("v1"))

	const callers = 16
	ids := make(chan uint64, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inst, _ := EnsureRunInstance(store, cfg.Deployment.ID, cfg.Meta.Version, cfg.Deployment.PlacementNodeID(), 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
			ids <- inst.ID
		}()
	}
	wg.Wait()
	close(ids)
	var want uint64
	for id := range ids {
		if want == 0 {
			want = id
		}
		if id != want {
			t.Fatalf("EnsureRunScheduledInstance returned ids %d and %d", want, id)
		}
	}
	active := statetest.NonFinalInstances(store, cfg.Deployment.ID)
	if len(active) != 1 || active[0].ID != want {
		t.Fatalf("active instances = %+v, want one id %d", active, want)
	}
}

func testSystemSpecWithVersion(version string) *apigen.DeploymentSpec {
	spec := internaldeploy.SelfSpec()
	if err := spec.SetWorkloadVersion(version); err != nil {
		panic(err)
	}
	return spec
}

func TestInvalidationPublishesTombstonesAndRetainsAllHistory(t *testing.T) {
	s := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	ctx := context.Background()
	node := nodes.EnsurePrimaryNode(s, "primary", "primary", testUnderlay, "")
	dep := statetest.MustCreateDeploymentForNode(s, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, statetest.SpecWithVersion("v1"))
	inst := statetest.CreateScheduledInstance(s, dep.Deployment.ID, dep.Meta.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	nodes.SetNodeStatusByIdentifier(s, node.Identifier, true, time.Now())
	scheduledinstances.WriteStatus(s, inst.ID, func(st *apigen.ScheduledInstanceStatus) bool {
		st.BumpUpdatedAt()
		st.Runner = apigen.Some(apigen.RunnerStatus{DeploymentSpecVersion: dep.Meta.SpecVersion, Status: apigen.RunningStatus_RUNNING_STATUS_RUNNING})
		return true
	})
	beforeSeq := globalSeq(t, s)
	beforeNode := erru.Must(s.Queries().GetLatestNodeStatus(ctx, node.ID))
	beforeInst := erru.Must(s.Queries().GetLatestScheduledInstanceStatus(ctx, inst.ID))
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	if count, err := nodes.InvalidateNodeRuntimeState(s, node.ID); err != nil || count != 1 {
		t.Fatalf("invalidation = %d, %v", count, err)
	}
	var update state.WriteUpdate
	select {
	case published := <-sub:
		if hasCore(published) || !hasObserved(published) || published.Seq != beforeSeq+1 {
			t.Fatalf("tombstone publication: %+v", published)
		}
		statetest.AssertUpdateMatchesRows(t, s, published)
		update = published
	case <-time.After(time.Second):
		t.Fatal("no tombstone published")
	}
	nodeStatuses, instanceStatuses := mutationsOf(update, nodeStatusType), mutationsOf(update, instanceStatusType)
	if len(nodeStatuses) != 1 || len(instanceStatuses) != 1 {
		t.Fatalf("tombstones = %+v", update)
	}
	n, i := nodeStatuses[0].Entity().Value.NodeStatus, instanceStatuses[0].Entity().Value.ScheduledInstanceStatus
	if n.IsConnected || n.LastConnectedAt.Present || n.RemoteAddress != "" || n.OpendeployVersion != "" || !n.UpdatedAtTime().After(beforeNode.UpdatedAtTime()) {
		t.Fatalf("node tombstone = %+v", n)
	}
	if i.Preparer.Present || i.Runner.Present || !i.UpdatedAtTime().After(beforeInst.UpdatedAtTime()) {
		t.Fatalf("instance tombstone = %+v", i)
	}
	if globalSeq(t, s) != beforeSeq+1 || !bytes.Equal(liveEntity(t, s, nodeStatusType, node.ID).Value.NodeStatus.Encode(), n.Encode()) || !bytes.Equal(liveEntity(t, s, instanceStatusType, inst.ID).Value.ScheduledInstanceStatus.Encode(), i.Encode()) {
		t.Fatal("reconnect bootstrap disagrees with live tombstones")
	}
	// A delayed worker status remains available in history without replacing
	// the tombstone in either the live view or a reconnect bootstrap.
	scheduledinstances.WriteReplicatedStatus(s, beforeInst)
	if !bytes.Equal(liveEntity(t, s, instanceStatusType, inst.ID).Value.ScheduledInstanceStatus.Encode(), i.Encode()) || !bytes.Equal(latestStatus(s, inst.ID).Encode(), i.Encode()) {
		t.Fatal("late observation resurrected cleared status")
	}
	if len(erru.Must(s.Queries().ListScheduledInstanceStatusHistorySince(ctx, inst.ID, time.Time{}))) != 2 || len(erru.Must(s.Queries().ListNodeStatusHistorySince(ctx, node.ID, time.Time{}))) != 2 {
		t.Fatal("invalidation deleted history")
	}
	scheduledinstances.WriteStatus(s, inst.ID, func(st *apigen.ScheduledInstanceStatus) bool {
		st.BumpUpdatedAt()
		st.Preparer = apigen.Some(apigen.PreparerStatus{DeploymentSpecVersion: dep.Meta.SpecVersion, Inputs: apigen.InputsStatus_INPUTS_STATUS_RESOLVING})
		return true
	})
	if !latestStatus(s, inst.ID).UpdatedAtTime().After(i.UpdatedAtTime()) {
		t.Fatal("local writer did not advance past tombstone")
	}
}

func TestMergedCommitFinalCacheAndRollback(t *testing.T) {
	s := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	ctx := context.Background()
	node := nodes.EnsurePrimaryNode(s, "primary", "primary", testUnderlay, "")
	dep := statetest.MustCreateDeploymentForNode(s, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, statetest.SpecWithVersion("v1"))
	inst := statetest.CreateScheduledInstance(s, dep.Deployment.ID, dep.Meta.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	scheduledinstances.WriteStatus(s, inst.ID, func(st *apigen.ScheduledInstanceStatus) bool {
		st.BumpUpdatedAt()
		code := int32(1)
		st.Runner = apigen.Some(apigen.RunnerStatus{Status: apigen.RunningStatus_RUNNING_STATUS_RUNNING, DeploymentSpecVersion: dep.Meta.SpecVersion, ExitCode: apigen.Some(code)})
		return true
	})
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	before := fingerprint(t, s)
	beforeSeq := globalSeq(t, s)
	beforeCache := s.FetchScheduledSnapshot(nil)
	beforeHistory := erru.Must(s.Queries().ListScheduledInstanceStatusHistorySince(context.Background(), inst.ID, time.Time{}))
	fail := errors.New("scheduler rejected transaction")
	reject := true
	calls := 0
	s.RegisterUpdateTrigger(func(ctx context.Context, q *pq.Queries, update *state.WriteUpdate) error {
		seq := update.Seq
		if !update.Has(instanceStatusType) {
			return nil
		}
		calls++
		status, err := q.GetLatestScheduledInstanceStatus(ctx, inst.ID)
		if err != nil {
			return err
		}
		if !status.Runner.Value.ExitCode.Present || status.Runner.Value.ExitCode.Value != 2 {
			return fmt.Errorf("hook did not see triggering status")
		}
		current, err := q.GetScheduledInstance(ctx, inst.ID)
		if err != nil {
			return err
		}
		event := pq.ScheduledInstanceTransition(seq, current, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED, time.UnixMilli(update.Time))
		if err := q.Apply(ctx, update, pq.ScheduledInstanceMutation(apigen.AuthzVerb_AUTHZ_VERB_UPDATE, event)); err != nil {
			return err
		}
		if reject {
			return fail
		}
		return nil
	})
	write := func() {
		scheduledinstances.WriteStatus(s, inst.ID, func(st *apigen.ScheduledInstanceStatus) bool {
			st.BumpUpdatedAt()
			st.Runner.Value.ExitCode = apigen.Some(int32(2))
			st.Runner.Value.Status = apigen.RunningStatus_RUNNING_STATUS_STOPPED
			return true
		})
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("failed scheduler did not reject status write")
			}
		}()
		write()
	}()
	if calls != 1 {
		t.Fatalf("callback calls = %d, want one without retries", calls)
	}
	if !bytes.Equal(before, fingerprint(t, s)) || !reflect.DeepEqual(beforeCache, s.FetchScheduledSnapshot(nil)) || !reflect.DeepEqual(beforeHistory, erru.Must(s.Queries().ListScheduledInstanceStatusHistorySince(context.Background(), inst.ID, time.Time{}))) {
		t.Fatal("rollback changed rows, sequence, history, or shared cache")
	}
	select {
	case <-sub:
		t.Fatal("rollback published")
	default:
	}
	reject = false
	write()
	update := <-sub
	statetest.AssertUpdateMatchesRows(t, s, update)
	statuses := mutationsOf(update, instanceStatusType)
	if update.Seq != beforeSeq+1 || len(mutationsOf(update, instanceType)) != 1 || len(statuses) != 1 || statuses[0].Entity().Value.ScheduledInstanceStatus.Runner.Value.Status != apigen.RunningStatus_RUNNING_STATUS_STOPPED {
		t.Fatalf("merged publication: %+v", update)
	}
	if erru.Must(s.Queries().GetScheduledInstance(ctx, inst.ID)).Value.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
		t.Fatal("triggering writer restored finalized cache entry")
	}
	if got := len(erru.Must(s.Queries().ListScheduledInstanceStatusHistorySince(context.Background(), inst.ID, time.Time{}))); got != len(beforeHistory)+1 {
		t.Fatalf("history length = %d", got)
	}
}
