package prepare

import (
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

// countingStore records how many status writes were actually committed, as
// opposed to attempted: the mutator returning false is the signal that a write
// was dropped.
type countingStore struct {
	status apigen.ScheduledInstanceStatus
	writes int
}

func (s *countingStore) MustWriteScheduledInstanceStatus(instanceID int32, f func(*apigen.ScheduledInstanceStatus) bool) {
	current := s.status
	if current.ScheduledInstanceID == 0 {
		current.ScheduledInstanceID = instanceID
	}
	if !f(&current) {
		return
	}
	s.status = current
	s.writes++
}

func (s *countingStore) MustFetchScheduledSnapshotAndSubscribe(storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan apigen.ScheduledInstanceState, func()) {
	return nil, nil, func() {}
}

// Preparation is re-driven for reasons unrelated to the artifact changing — an
// agent restart above all — and normally lands on the artifact already
// recorded. Republishing that identity bumps the clock, wakes subscribers and
// pushes a no-op to the primary, so it must be dropped.
func TestWriteStatusDropsUnchangedPreparerStatus(t *testing.T) {
	store := &countingStore{}
	dep := &apigen.DeploymentConfig{ID: 11, Version: 4}

	WriteStatus(store, 42, dep, "example/app:v1", apigen.PreparationStatus_READY)
	if store.writes != 1 {
		t.Fatalf("first write count = %d, want 1", store.writes)
	}
	first := store.status.UpdatedAt

	WriteStatus(store, 42, dep, "example/app:v1", apigen.PreparationStatus_READY)
	if store.writes != 1 {
		t.Fatalf("identical status was republished: write count = %d, want 1", store.writes)
	}
	if store.status.UpdatedAt != first {
		t.Fatal("identical status bumped UpdatedAt")
	}

	// Anything genuinely different still publishes.
	WriteStatus(store, 42, dep, "example/app:v2", apigen.PreparationStatus_READY)
	if store.writes != 2 {
		t.Fatalf("changed artifact was dropped: write count = %d, want 2", store.writes)
	}
	WriteStatus(store, 42, dep, "example/app:v2", apigen.PreparationStatus_FAILED)
	if store.writes != 3 {
		t.Fatalf("changed status was dropped: write count = %d, want 3", store.writes)
	}
}

// The guard keys off the status already recorded, so it must not swallow the
// very first write for an instance, whose stored status is still zero.
func TestWriteStatusAlwaysPublishesFirstStatus(t *testing.T) {
	store := &countingStore{}
	dep := &apigen.DeploymentConfig{ID: 11, Version: 4}

	WriteStatus(store, 42, dep, "", apigen.PreparationStatus_PREPARING)
	if store.writes != 1 {
		t.Fatalf("first write count = %d, want 1", store.writes)
	}
	if store.status.Preparer.Status != apigen.PreparationStatus_PREPARING {
		t.Fatalf("preparer status = %v, want PREPARING", store.status.Preparer.Status)
	}
}

func TestHandleCancelWaitsForCompletion(t *testing.T) {
	handle, ctx := NewHandle(7)
	cancelled := make(chan struct{})
	go func() {
		handle.Cancel()
		close(cancelled)
	}()

	<-ctx.Done()
	select {
	case <-cancelled:
		t.Fatal("Cancel returned before Complete")
	default:
	}

	handle.Complete()
	<-cancelled
	if handle.Version() != 7 {
		t.Fatalf("Version() = %d, want 7", handle.Version())
	}

	// Completion is intentionally idempotent for deferred cleanup paths.
	handle.Complete()
}

func TestFinishedHandleCancelReturns(t *testing.T) {
	handle := Finished(9)
	handle.Cancel()
	if handle.Version() != 9 {
		t.Fatalf("Version() = %d, want 9", handle.Version())
	}
}
