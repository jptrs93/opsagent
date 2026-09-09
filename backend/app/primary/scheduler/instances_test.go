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

func latestStatus(s *state.Service, instanceID int32) *apigen.ScheduledInstanceStatus {
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

	primaryNode := nodes.EnsurePrimaryNode(store, "primary", "primary")
	secondaryNode := nodes.EnsurePrimaryNode(store, "secondary", "secondary")
	create := func(nodeID int32, name string, spec *apigen.DeploymentSpec) *apigen.DeploymentEvent {
		return statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, name, nodeID, spec)
	}
	containerSpec := statetest.SpecWithState("v1", true)
	primary := create(primaryNode.ID, "app", containerSpec)
	secondary := create(secondaryNode.ID, "app", containerSpec)
	system := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, internaldeploy.SpaceID, internaldeploy.SelfName, primaryNode.ID, testSystemSpecWithState("v1", true))

	seedStatus := func(cfg *apigen.DeploymentEvent, artifact string) *apigen.ScheduledInstance {
		inst := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
		scheduledinstances.WriteStatus(store, inst.ID, func(status *apigen.ScheduledInstanceStatus) bool {
			status.BumpUpdatedAt()
			status.Preparer = apigen.PreparerStatus{DeploymentSpecVersion: cfg.SpecVersion, Artifact: artifact, Inputs: apigen.InputsStatus_INPUTS_READY, Image: apigen.ImageStatus_IMAGE_READY}
			status.Runner = apigen.RunnerStatus{DeploymentSpecVersion: cfg.SpecVersion, RunningArtifact: artifact, Status: apigen.RunningStatus_RUNNING}
			return true
		})
		return inst
	}
	primaryInst := seedStatus(primary, "example/app:v1")
	seedStatus(secondary, "example/app:v1")
	seedStatus(system, "/var/lib/opendeploy/releases/v1/opendeploy")

	primaryConfigHistoryCount := len(erru.Must(store.Queries().ListDeploymentEvents(context.Background(), int64(primary.DeploymentID))))
	primaryConfigVersion := primary.SpecVersion

	count, err := nodes.InvalidateNodeRuntimeState(store, primaryNode.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("invalidated count = %d, want 1", count)
	}
	got := latestStatus(store, primaryInst.ID)
	if got != nil && (!got.Preparer.IsZero() || !got.Runner.IsZero()) {
		t.Fatalf("primary runtime status was not cleared: %+v", got)
	}
	if len(erru.Must(store.Queries().ListDeploymentEvents(context.Background(), int64(primary.DeploymentID)))) != primaryConfigHistoryCount {
		t.Fatal("runtime invalidation changed config history")
	}
	secondaryInst := statetest.NonFinalInstances(store, secondary.DeploymentID)[0]
	if latestStatus(store, secondaryInst.ID).Runner.Status != apigen.RunningStatus_RUNNING {
		t.Fatal("secondary runtime status was cleared")
	}
	systemInst := statetest.NonFinalInstances(store, system.DeploymentID)[0]
	if latestStatus(store, systemInst.ID).Runner.Status != apigen.RunningStatus_RUNNING {
		t.Fatal("primary system deployment runtime status was cleared")
	}
	for _, cfg := range erru.Must(store.Queries().ListActiveDeployments(context.Background())) {
		if cfg.DeploymentID == primary.DeploymentID && cfg.SpecVersion != primaryConfigVersion {
			t.Fatalf("primary spec version = %d, want %d", cfg.SpecVersion, primaryConfigVersion)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = state.Open(dbPath)
	got = latestStatus(store, primaryInst.ID)
	if got != nil && (!got.Preparer.IsZero() || !got.Runner.IsZero()) {
		t.Fatalf("persisted primary runtime status was not cleared correctly: %+v", got)
	}
}

func TestEnsureRunScheduledInstanceIsConcurrentAndIdempotent(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := nodes.EnsurePrimaryNode(store, "primary", "primary-id")
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "api", node.ID, statetest.SpecWithState("v1", true))

	const callers = 16
	ids := make(chan int32, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inst, _ := EnsureRunInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
			ids <- inst.ID
		}()
	}
	wg.Wait()
	close(ids)
	var want int32
	for id := range ids {
		if want == 0 {
			want = id
		}
		if id != want {
			t.Fatalf("EnsureRunScheduledInstance returned ids %d and %d", want, id)
		}
	}
	active := statetest.NonFinalInstances(store, cfg.DeploymentID)
	if len(active) != 1 || active[0].ID != want {
		t.Fatalf("active instances = %+v, want one id %d", active, want)
	}
}

func testSystemSpecWithState(version string, running bool) *apigen.DeploymentSpec {
	spec := internaldeploy.SelfSpec()
	if err := spec.SetWorkloadState(version, running); err != nil {
		panic(err)
	}
	return spec
}

func TestInvalidationPublishesTombstonesAndRetainsAllHistory(t *testing.T) {
	s := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	node := nodes.EnsurePrimaryNode(s, "primary", "primary")
	dep := statetest.MustCreateDeploymentForNode(s, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, statetest.SpecWithState("v1", true))
	inst := statetest.CreateScheduledInstance(s, dep.DeploymentID, dep.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	nodes.SetNodeStatusByIdentifier(s, node.Identifier, true, time.Now())
	scheduledinstances.WriteStatus(s, inst.ID, func(st *apigen.ScheduledInstanceStatus) bool {
		st.BumpUpdatedAt()
		st.Runner = apigen.RunnerStatus{DeploymentSpecVersion: dep.SpecVersion, Status: apigen.RunningStatus_RUNNING}
		return true
	})
	before := s.BuildSnapshot(context.Background())
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	if count, err := nodes.InvalidateNodeRuntimeState(s, node.ID); err != nil || count != 1 {
		t.Fatalf("invalidation = %d, %v", count, err)
	}
	var update apigen.CoreUpdate
	select {
	case published := <-sub:
		if published.HasCore() || !published.HasObserved() || published.Seq != before.Seq+1 {
			t.Fatalf("tombstone publication: %+v", published)
		}
		statetest.AssertUpdateMatchesRows(t, s, published)
		update = published
	case <-time.After(time.Second):
		t.Fatal("no tombstone published")
	}
	if len(update.NodeStatuses) != 1 || len(update.InstanceStatuses) != 1 {
		t.Fatalf("tombstones = %+v", update)
	}
	n, i := update.NodeStatuses[0], update.InstanceStatuses[0]
	if n.IsConnected || !n.LastConnectedAt.IsZero() || n.RemoteAddress != "" || n.OpendeployVersion != "" || !n.UpdatedAt.After(before.NodeStatuses[0].UpdatedAt) {
		t.Fatalf("node tombstone = %+v", n)
	}
	if !i.Preparer.IsZero() || !i.Runner.IsZero() || !i.UpdatedAt.After(before.InstanceStatuses[0].UpdatedAt) {
		t.Fatalf("instance tombstone = %+v", i)
	}
	after := s.BuildSnapshot(context.Background())
	if after.Seq != before.Seq+1 || !bytes.Equal(after.NodeStatuses[0].Encode(), n.Encode()) || !bytes.Equal(after.InstanceStatuses[0].Encode(), i.Encode()) {
		t.Fatal("reconnect snapshot disagrees with live tombstones")
	}
	// A delayed worker status remains available in history without replacing
	// the tombstone in either the cache or a reconnect snapshot.
	scheduledinstances.WriteReplicatedStatus(s, before.InstanceStatuses[0])
	if !bytes.Equal(s.BuildSnapshot(context.Background()).InstanceStatuses[0].Encode(), i.Encode()) {
		t.Fatal("late observation resurrected cleared status")
	}
	if len(erru.Must(s.Queries().ListScheduledInstanceStatusHistorySince(context.Background(), inst.ID, time.Time{}))) != 2 || len(erru.Must(s.Queries().ListNodeStatusHistorySince(context.Background(), node.ID, time.Time{}))) != 2 {
		t.Fatal("invalidation deleted history")
	}
	scheduledinstances.WriteStatus(s, inst.ID, func(st *apigen.ScheduledInstanceStatus) bool {
		st.BumpUpdatedAt()
		st.Preparer = apigen.PreparerStatus{DeploymentSpecVersion: dep.SpecVersion}
		return true
	})
	if !latestStatus(s, inst.ID).UpdatedAt.After(i.UpdatedAt) {
		t.Fatal("local writer did not advance past tombstone")
	}
}

func TestMergedCommitFinalCacheAndRollback(t *testing.T) {
	s := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	ctx := context.Background()
	node := nodes.EnsurePrimaryNode(s, "primary", "primary")
	dep := statetest.MustCreateDeploymentForNode(s, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, statetest.SpecWithState("v1", true))
	inst := statetest.CreateScheduledInstance(s, dep.DeploymentID, dep.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	scheduledinstances.WriteStatus(s, inst.ID, func(st *apigen.ScheduledInstanceStatus) bool {
		st.BumpUpdatedAt()
		code := int32(1)
		st.Runner = apigen.RunnerStatus{Status: apigen.RunningStatus_RUNNING, DeploymentSpecVersion: dep.SpecVersion, ExitCode: &code}
		return true
	})
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	before := s.BuildSnapshot(ctx)
	beforeCache := s.FetchScheduledSnapshot(nil)
	beforeHistory := erru.Must(s.Queries().ListScheduledInstanceStatusHistorySince(context.Background(), inst.ID, time.Time{}))
	fail := errors.New("scheduler rejected transaction")
	reject := true
	calls := 0
	s.RegisterUpdateTrigger(func(ctx context.Context, q *pq.Queries, update *state.Update) error {
		seq := update.Seq
		if !update.HasObserved() {
			return nil
		}
		calls++
		status, err := q.GetLatestScheduledInstanceStatus(ctx, inst.ID)
		if err != nil {
			return err
		}
		if status.Runner.ExitCode == nil || *status.Runner.ExitCode != 2 {
			return fmt.Errorf("hook did not see triggering status")
		}
		event, err := q.AppendScheduledInstanceEvent(ctx, seq, inst, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED, time.Now())
		if err != nil {
			return err
		}
		update.ScheduledInstanceEvents = []*apigen.ScheduledInstanceEvent{event}
		if reject {
			return fail
		}
		return nil
	})
	write := func() {
		scheduledinstances.WriteStatus(s, inst.ID, func(st *apigen.ScheduledInstanceStatus) bool {
			st.BumpUpdatedAt()
			*st.Runner.ExitCode = 2
			st.Runner.Status = apigen.RunningStatus_STOPPED
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
	if !reflect.DeepEqual(before, s.BuildSnapshot(ctx)) || !reflect.DeepEqual(beforeCache, s.FetchScheduledSnapshot(nil)) || !reflect.DeepEqual(beforeHistory, erru.Must(s.Queries().ListScheduledInstanceStatusHistorySince(context.Background(), inst.ID, time.Time{}))) {
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
	if update.Seq != before.Seq+1 || !update.HasCore() || len(update.ScheduledInstanceEvents) != 1 || !update.HasObserved() || len(update.InstanceStatuses) != 1 || update.InstanceStatuses[0].Runner.Status != apigen.RunningStatus_STOPPED {
		t.Fatalf("merged publication: %+v", update)
	}
	if erru.Must(s.Queries().GetScheduledInstance(ctx, inst.ID)).Value.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
		t.Fatal("triggering writer restored finalized cache entry")
	}
	if got := len(erru.Must(s.Queries().ListScheduledInstanceStatusHistorySince(context.Background(), inst.ID, time.Time{}))); got != len(beforeHistory)+1 {
		t.Fatalf("history length = %d", got)
	}
}
