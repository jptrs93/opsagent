package state

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func TestSubscriberOverflowClosesOnlyTheSlowSubscriber(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	node := testNode(s, "primary")
	_, slow, unsubscribeSlow := Subscribe(s, func() struct{} { return struct{}{} }, func(WriteUpdate) (int, bool) { return 0, true })
	defer unsubscribeSlow()
	_, fast, unsubscribeFast := Subscribe(s, func() struct{} { return struct{}{} }, func(u WriteUpdate) (WriteUpdate, bool) {
		return u, u.Has(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT)
	})
	defer unsubscribeFast()
	receive := func(want string, check func(WriteUpdate) bool) {
		t.Helper()
		select {
		case got, ok := <-fast:
			if !ok || !check(got) {
				t.Fatalf("fast subscriber expected %s, received %+v (open=%v)", want, got, ok)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("fast subscriber did not receive %s", want)
		}
	}
	dep := mustCreateDeploymentForNode(s, apigen.Context{}, defaultSpaceID, "overflow", node.ID, envRefSpec(nil, nil))
	receive("create", func(u WriteUpdate) bool {
		return len(u.Mutations) == 1 && u.Mutations[0].EntityID() == int64(dep.DeploymentID) && u.Mutations[0].Kind() == apigen.AuthzVerb_AUTHZ_VERB_CREATE
	})
	s.Mu.Lock()
	for i := 0; i <= SubscriberBuffer; i++ {
		s.notifyLocked(t.Context(), WriteUpdate{})
	}
	s.Mu.Unlock()
	drained := 0
	for range slow {
		drained++
	}
	if drained != SubscriberBuffer {
		t.Fatalf("slow subscriber drained %d before close, want %d", drained, SubscriberBuffer)
	}
	unsubscribeSlow()
	deleteDeployment(s, apigen.Context{}, dep.DeploymentID)
	receive("delete", func(u WriteUpdate) bool {
		return len(u.Mutations) == 1 && u.Mutations[0].EntityID() == int64(dep.DeploymentID) && u.Mutations[0].Kind() == apigen.AuthzVerb_AUTHZ_VERB_DELETE
	})
}

func TestScheduledSubscriberDeliversCommittedStates(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	node := testNode(s, "primary")
	dep := mustCreateDeploymentForNode(s, apigen.Context{}, defaultSpaceID, "instances", node.ID, envRefSpec(nil, nil))
	inst := createScheduledInstanceForTest(s, dep.DeploymentID, dep.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	initial, updates, unsubscribe := s.MustFetchScheduledSnapshotAndSubscribe(nil)
	defer unsubscribe()
	if len(initial) != 1 || initial[0].Instance.ID != inst.ID {
		t.Fatalf("initial snapshot = %+v", initial)
	}
	setScheduledInstanceState(s, inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	select {
	case got, ok := <-updates:
		if !ok || len(got) != 1 || got[0].Instance.ID != inst.ID || got[0].Instance.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
			t.Fatalf("post-commit update = %+v (open=%v)", got, ok)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subscriber did not receive the committed state")
	}
	unsubscribe()
	if _, ok := <-updates; ok {
		t.Fatal("unsubscribe did not close the channel")
	}
	if after := s.FetchScheduledSnapshot(nil); len(after) != 0 {
		t.Fatalf("snapshot still lists the finalized instance: %+v", after)
	}
}

func TestScheduledSubscriberDeliversAnInstancePrunedByItsFinalizingCommit(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	ctx := context.Background()
	node := testNode(s, "primary")
	dep := mustCreateDeploymentForNode(s, apigen.Context{}, defaultSpaceID, "rollover", node.ID, envRefSpec(nil, nil))
	old := createScheduledInstanceForTest(s, dep.DeploymentID, dep.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	writeRunnerStatus(t, s, old.ID, apigen.RunningStatus_STOPPED)
	next := updateDeploymentSpec(s, apigen.Context{}, dep.DeploymentID, nonEmptySpec())
	if next.Version == dep.Version {
		t.Fatal("spec update did not produce a new version")
	}
	_, updates, unsubscribe := s.MustFetchScheduledSnapshotAndSubscribe(nil)
	defer unsubscribe()
	var created int32
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		current, err := q.GetScheduledInstance(ctx, old.ID)
		if err != nil {
			return nil, err
		}
		cfg, err := q.GetDeploymentEventByVersion(ctx, pq.GetDeploymentEventByVersionParams{DeploymentID: int64(next.DeploymentID), Version: int64(next.Version)})
		if err != nil {
			return nil, err
		}
		created, err = q.NextScheduledInstanceID(ctx)
		if err != nil {
			return nil, err
		}
		inst := &apigen.ScheduledInstance{ID: created, DeploymentID: cfg.DeploymentID, DeploymentVersion: cfg.Version, DeploymentSpecVersion: cfg.SpecVersion, NodeID: node.ID, SpaceID: cfg.Value.SpaceID}
		return pq.NewUpdate(
			pq.ScheduledInstanceMutation(apigen.AuthzVerb_AUTHZ_VERB_UPDATE, pq.ScheduledInstanceTransition(seq, current, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED, time.Now())),
			pq.ScheduledInstanceMutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE, pq.NewScheduledInstanceEvent(seq, inst, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING, time.Now())),
		), nil
	}))
	if _, err := s.q.GetScheduledInstance(ctx, old.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("finalized instance row survived its replacement: %v", err)
	}
	if _, err := s.q.GetDeploymentEventByVersion(ctx, pq.GetDeploymentEventByVersionParams{DeploymentID: int64(dep.DeploymentID), Version: int64(dep.Version)}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unpinned version row survived: %v", err)
	}
	select {
	case got, ok := <-updates:
		if !ok || len(got) != 2 {
			t.Fatalf("post-commit update = %+v (open=%v), want the finalized and the new instance", got, ok)
		}
		byID := map[int32]apigen.ScheduledInstanceState{}
		for _, st := range got {
			byID[st.Instance.ID] = st
		}
		gone, ok := byID[old.ID]
		if !ok || gone.Instance.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
			t.Fatalf("pruned instance %d not delivered as finalized: %+v", old.ID, got)
		}
		if gone.Config.DeploymentID != dep.DeploymentID || gone.Config.Version != dep.Version || gone.Config.Value.Name != "rollover" {
			t.Fatalf("pruned instance config = %+v, want version %d of deployment %d from the log", gone.Config, dep.Version, dep.DeploymentID)
		}
		if gone.Status.Runner.Status != apigen.RunningStatus_STOPPED {
			t.Fatalf("pruned instance status = %+v, want the STOPPED it ended on", gone.Status)
		}
		fresh, ok := byID[created]
		if !ok || fresh.Instance.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING || fresh.Config.Version != next.Version {
			t.Fatalf("new instance %d = %+v", created, fresh)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subscriber did not receive the finalizing commit")
	}
}
