package apigen

import (
	"testing"
	"time"
)

func TestScheduledInstanceStatusBumpUpdatedAtUsesWallClock(t *testing.T) {
	previous := time.Now().Add(time.Hour)
	status := ScheduledInstanceStatus{UpdatedAt: previous}

	status.BumpUpdatedAt()

	want := previous.Round(0).Add(time.Nanosecond)
	if !status.UpdatedAt.Equal(want) {
		t.Fatalf("UpdatedAt = %v, want %v", status.UpdatedAt, want)
	}
	if status.UpdatedAt != status.UpdatedAt.Round(0) {
		t.Fatal("UpdatedAt retained a monotonic clock reading")
	}
}
