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

func (s *countingStore) MustWriteScheduledInstanceStatus(instanceID uint64, f func(*apigen.ScheduledInstanceStatus) bool) {
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

func (s *countingStore) MustFetchScheduledSnapshotAndSubscribe(storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan []apigen.ScheduledInstanceState, func()) {
	return nil, nil, func() {}
}

// Preparation is re-driven for reasons unrelated to the artifact changing — an
// agent restart above all — and normally lands on the artifact already
// recorded. Republishing that identity bumps the clock, wakes subscribers and
// pushes a no-op to the primary, so it must be dropped.
func TestWriteStatusDropsUnchangedPreparerStatus(t *testing.T) {
	store := &countingStore{}
	dep := &apigen.DeploymentRecord{Deployment: apigen.Deployment{ID: 11}, Meta: apigen.EntityMeta{SpecVersion: 4}}

	ready := StatusUpdate{
		Artifact: "example/app:v1",
		Inputs:   apigen.InputsStatus_INPUTS_STATUS_READY,
		Image:    apigen.ImageStatus_IMAGE_STATUS_READY,
	}
	WriteStatus(store, 42, dep, ready)
	if store.writes != 1 {
		t.Fatalf("first write count = %d, want 1", store.writes)
	}
	first := store.status.UpdatedAt

	WriteStatus(store, 42, dep, ready)
	if store.writes != 1 {
		t.Fatalf("identical status was republished: write count = %d, want 1", store.writes)
	}
	if store.status.UpdatedAt != first {
		t.Fatal("identical status bumped UpdatedAt")
	}

	// Anything genuinely different still publishes.
	readyV2 := ready
	readyV2.Artifact = "example/app:v2"
	WriteStatus(store, 42, dep, readyV2)
	if store.writes != 2 {
		t.Fatalf("changed artifact was dropped: write count = %d, want 2", store.writes)
	}
	failed := readyV2
	failed.Image = apigen.ImageStatus_IMAGE_STATUS_FAILED
	WriteStatus(store, 42, dep, failed)
	if store.writes != 3 {
		t.Fatalf("changed status was dropped: write count = %d, want 3", store.writes)
	}

	// A stage moving on its own is a real transition even when the rollup does
	// not move, which is exactly the input-retry case.
	stillReady := ready
	WriteStatus(store, 42, dep, stillReady)
	resolving := stillReady
	resolving.Inputs = apigen.InputsStatus_INPUTS_STATUS_RESOLVING
	WriteStatus(store, 42, dep, resolving)
	if store.writes != 5 {
		t.Fatalf("inputs-only transition was dropped: write count = %d, want 5", store.writes)
	}
	if store.status.Preparer.Value.Rollup() != apigen.PreparationStatus_PREPARATION_STATUS_READY {
		t.Fatalf("rollup = %v, want READY to survive an inputs retry", store.status.Preparer.Value.Rollup())
	}
}

// InProgress is what holds a prepare-log stream open. PULLING counts: a remote
// image pull is preparation, and omitting it closed the stream early.
func TestInProgress(t *testing.T) {
	inProgress := []StatusUpdate{
		{Inputs: apigen.InputsStatus_INPUTS_STATUS_RESOLVING},
		{Inputs: apigen.InputsStatus_INPUTS_STATUS_READY, Image: apigen.ImageStatus_IMAGE_STATUS_BUILDING},
		{Inputs: apigen.InputsStatus_INPUTS_STATUS_READY, Image: apigen.ImageStatus_IMAGE_STATUS_DOWNLOADING},
		{Inputs: apigen.InputsStatus_INPUTS_STATUS_READY, Image: apigen.ImageStatus_IMAGE_STATUS_PULLING},
	}
	for _, u := range inProgress {
		p := apigen.PreparerStatus{Inputs: u.Inputs}
		if u.Image != apigen.ImageStatus_IMAGE_STATUS_UNSPECIFIED {
			p.SetImage(u.Image)
		}
		if !InProgress(p) {
			t.Errorf("InProgress(inputs=%v image=%v) = false, want true", u.Inputs, u.Image)
		}
	}
	settled := []StatusUpdate{
		{},
		{Inputs: apigen.InputsStatus_INPUTS_STATUS_READY, Image: apigen.ImageStatus_IMAGE_STATUS_READY},
		{Inputs: apigen.InputsStatus_INPUTS_STATUS_FAILED},
		{Inputs: apigen.InputsStatus_INPUTS_STATUS_READY, Image: apigen.ImageStatus_IMAGE_STATUS_FAILED},
		// An input retry on an already-prepared instance writes nothing to the
		// prepare log, so it must not hold a stream open.
		{Inputs: apigen.InputsStatus_INPUTS_STATUS_RESOLVING, Image: apigen.ImageStatus_IMAGE_STATUS_READY},
	}
	for _, u := range settled {
		p := apigen.PreparerStatus{Inputs: u.Inputs}
		if u.Image != apigen.ImageStatus_IMAGE_STATUS_UNSPECIFIED {
			p.SetImage(u.Image)
		}
		if InProgress(p) {
			t.Errorf("InProgress(inputs=%v image=%v) = true, want false", u.Inputs, u.Image)
		}
	}
}

// The guard keys off the status already recorded, so it must not swallow the
// very first write for an instance, whose stored status is still zero.
func TestWriteStatusAlwaysPublishesFirstStatus(t *testing.T) {
	store := &countingStore{}
	dep := &apigen.DeploymentRecord{Deployment: apigen.Deployment{ID: 11}, Meta: apigen.EntityMeta{SpecVersion: 4}}

	WriteStatus(store, 42, dep, StatusUpdate{Inputs: apigen.InputsStatus_INPUTS_STATUS_RESOLVING})
	if store.writes != 1 {
		t.Fatalf("first write count = %d, want 1", store.writes)
	}
	if store.status.Preparer.Value.Rollup() != apigen.PreparationStatus_PREPARATION_STATUS_PREPARING {
		t.Fatalf("preparer status = %v, want PREPARING", store.status.Preparer.Value.Rollup())
	}
	if store.status.Preparer.Value.Inputs != apigen.InputsStatus_INPUTS_STATUS_RESOLVING {
		t.Fatalf("inputs status = %v, want RESOLVING", store.status.Preparer.Value.Inputs)
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
	if handle.SpecVersion() != 7 {
		t.Fatalf("Version() = %d, want 7", handle.SpecVersion())
	}

	// Completion is intentionally idempotent for deferred cleanup paths.
	handle.Complete()
}

func TestFinishedHandleCancelReturns(t *testing.T) {
	handle := Finished(9)
	handle.Cancel()
	if handle.SpecVersion() != 9 {
		t.Fatalf("Version() = %d, want 9", handle.SpecVersion())
	}
}
