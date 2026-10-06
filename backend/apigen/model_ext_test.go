package apigen

import (
	"testing"
	"time"
)

func TestScheduledInstanceStatusBumpUpdatedAtUsesWallClock(t *testing.T) {
	previous := time.Now().Add(time.Hour)
	status := ScheduledInstanceStatus{UpdatedAt: Some(previous)}

	status.BumpUpdatedAt()

	want := previous.Round(0).Add(time.Nanosecond)
	if !status.UpdatedAt.Value.Equal(want) {
		t.Fatalf("UpdatedAt = %v, want %v", status.UpdatedAt, want)
	}
	if status.UpdatedAt.Value != status.UpdatedAt.Value.Round(0) {
		t.Fatal("UpdatedAt retained a monotonic clock reading")
	}
}

// The rollup gates runner start, holds prepare-log streams open, and marks an
// instance quiescent. It is derived, so each stage pair must collapse to the
// PreparationStatus the rest of the engine expects.
func TestPreparerStatusRollup(t *testing.T) {
	cases := []struct {
		name   string
		status PreparerStatus
		want   PreparationStatus
	}{
		{"nothing started", PreparerStatus{}, PreparationStatus_PREPARATION_STATUS_UNSPECIFIED},
		{"resolving inputs", PreparerStatus{Inputs: InputsStatus_INPUTS_STATUS_RESOLVING}, PreparationStatus_PREPARATION_STATUS_PREPARING},
		{"inputs failed", PreparerStatus{Inputs: InputsStatus_INPUTS_STATUS_FAILED}, PreparationStatus_PREPARATION_STATUS_FAILED},
		{"between stages", PreparerStatus{Inputs: InputsStatus_INPUTS_STATUS_READY}, PreparationStatus_PREPARATION_STATUS_PREPARING},
		{"building", PreparerStatus{Inputs: InputsStatus_INPUTS_STATUS_READY, Image: Some(ImageStatus_IMAGE_STATUS_BUILDING)}, PreparationStatus_PREPARATION_STATUS_PREPARING},
		{"pulling", PreparerStatus{Inputs: InputsStatus_INPUTS_STATUS_READY, Image: Some(ImageStatus_IMAGE_STATUS_PULLING)}, PreparationStatus_PREPARATION_STATUS_PULLING},
		{"downloading", PreparerStatus{Inputs: InputsStatus_INPUTS_STATUS_READY, Image: Some(ImageStatus_IMAGE_STATUS_DOWNLOADING)}, PreparationStatus_PREPARATION_STATUS_DOWNLOADING},
		{"ready", PreparerStatus{Inputs: InputsStatus_INPUTS_STATUS_READY, Image: Some(ImageStatus_IMAGE_STATUS_READY)}, PreparationStatus_PREPARATION_STATUS_READY},
		{"image failed", PreparerStatus{Inputs: InputsStatus_INPUTS_STATUS_READY, Image: Some(ImageStatus_IMAGE_STATUS_FAILED)}, PreparationStatus_PREPARATION_STATUS_FAILED},
		// An input retry on an already-prepared instance must not demote the
		// rollup: the artifact is built and the runner gate reads the rollup.
		{"input retry keeps ready", PreparerStatus{Inputs: InputsStatus_INPUTS_STATUS_RESOLVING, Image: Some(ImageStatus_IMAGE_STATUS_READY)}, PreparationStatus_PREPARATION_STATUS_READY},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.status.Rollup(); got != tc.want {
				t.Fatalf("Rollup() = %v, want %v", got, tc.want)
			}
		})
	}
}
