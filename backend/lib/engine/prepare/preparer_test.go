package prepare

import "testing"

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
