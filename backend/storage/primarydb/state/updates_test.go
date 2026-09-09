package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestSubscriberOverflowClosesOnlyTheSlowSubscriber(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	node := testNode(s, "primary")
	_, slow, unsubscribeSlow := Subscribe(s, func() struct{} { return struct{}{} }, func(Update) (int, bool) { return 0, true })
	defer unsubscribeSlow()
	_, fast, unsubscribeFast := Subscribe(s, func() struct{} { return struct{}{} }, func(u Update) (Update, bool) { return u, len(u.DeploymentEvents) > 0 })
	defer unsubscribeFast()
	receive := func(want string, check func(Update) bool) {
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
	receive("create", func(u Update) bool {
		return len(u.DeploymentEvents) == 1 && u.DeploymentEvents[0].DeploymentID == dep.DeploymentID
	})
	s.Mu.Lock()
	for i := 0; i <= SubscriberBuffer; i++ {
		s.notifyLocked(t.Context(), Update{})
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
	receive("delete", func(u Update) bool { return len(u.DeploymentEvents) == 1 && u.DeploymentEvents[0].Deleted() })
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
