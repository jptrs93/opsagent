package state

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestOverflowClosesSubscriberAndResubscribeOmitsRemovedInstance(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "secondary.db"))
	defer s.Close()
	state := apigen.ScheduledInstanceState{Instance: apigen.ScheduledInstance{ID: 1}}
	s.scheduled[1] = &state
	_, updates, unsubscribe := s.MustFetchScheduledSnapshotAndSubscribe(nil)
	defer unsubscribe()
	s.mu.Lock()
	for i := 0; i <= subscriberBuffer; i++ {
		s.notifyInstanceLocked(1)
	}
	delete(s.scheduled, 1)
	s.mu.Unlock()
	drained := 0
	for range updates {
		drained++
	}
	if drained != subscriberBuffer {
		t.Fatalf("drained %d before close, want %d", drained, subscriberBuffer)
	}
	unsubscribe()
	snapshot, next, unsubscribeNext := s.MustFetchScheduledSnapshotAndSubscribe(nil)
	defer unsubscribeNext()
	if len(snapshot) != 0 {
		t.Fatalf("resubscribe snapshot still holds the removed instance: %+v", snapshot)
	}
	select {
	case _, ok := <-next:
		t.Fatalf("fresh subscriber channel delivered or closed unexpectedly (open=%v)", ok)
	default:
	}
}
