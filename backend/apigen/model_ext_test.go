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

// The rollup gates runner start, holds prepare-log streams open, and marks an
// instance quiescent. It is derived, so each stage pair must collapse to the
// PreparationStatus the rest of the engine expects.
func TestPreparerStatusRollup(t *testing.T) {
	cases := []struct {
		name   string
		status PreparerStatus
		want   PreparationStatus
	}{
		{"nothing started", PreparerStatus{}, PreparationStatus_PREPARATION_STATUS_UNKNOWN},
		{"resolving inputs", PreparerStatus{Inputs: InputsStatus_INPUTS_RESOLVING}, PreparationStatus_PREPARING},
		{"inputs failed", PreparerStatus{Inputs: InputsStatus_INPUTS_FAILED}, PreparationStatus_FAILED},
		{"between stages", PreparerStatus{Inputs: InputsStatus_INPUTS_READY}, PreparationStatus_PREPARING},
		{"building", PreparerStatus{Inputs: InputsStatus_INPUTS_READY, Image: ImageStatus_IMAGE_BUILDING}, PreparationStatus_PREPARING},
		{"pulling", PreparerStatus{Inputs: InputsStatus_INPUTS_READY, Image: ImageStatus_IMAGE_PULLING}, PreparationStatus_PULLING},
		{"downloading", PreparerStatus{Inputs: InputsStatus_INPUTS_READY, Image: ImageStatus_IMAGE_DOWNLOADING}, PreparationStatus_DOWNLOADING},
		{"ready", PreparerStatus{Inputs: InputsStatus_INPUTS_READY, Image: ImageStatus_IMAGE_READY}, PreparationStatus_READY},
		{"image failed", PreparerStatus{Inputs: InputsStatus_INPUTS_READY, Image: ImageStatus_IMAGE_FAILED}, PreparationStatus_FAILED},
		// An input retry on an already-prepared instance must not demote the
		// rollup: the artifact is built and the runner gate reads the rollup.
		{"input retry keeps ready", PreparerStatus{Inputs: InputsStatus_INPUTS_RESOLVING, Image: ImageStatus_IMAGE_READY}, PreparationStatus_READY},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.status.Rollup(); got != tc.want {
				t.Fatalf("Rollup() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Every legacy PreparationStatus the backfill migration maps into stage pairs
// must re-derive to itself, or upgraded rows would change meaning.
func TestRollupRoundTripsMigrationBackfill(t *testing.T) {
	backfill := map[PreparationStatus]PreparerStatus{
		PreparationStatus_PREPARATION_STATUS_UNKNOWN: {Inputs: InputsStatus_INPUTS_STATUS_UNKNOWN, Image: ImageStatus_IMAGE_STATUS_UNKNOWN},
		PreparationStatus_PREPARING:                  {Inputs: InputsStatus_INPUTS_READY, Image: ImageStatus_IMAGE_BUILDING},
		PreparationStatus_DOWNLOADING:                {Inputs: InputsStatus_INPUTS_READY, Image: ImageStatus_IMAGE_DOWNLOADING},
		PreparationStatus_READY:                      {Inputs: InputsStatus_INPUTS_READY, Image: ImageStatus_IMAGE_READY},
		PreparationStatus_FAILED:                     {Inputs: InputsStatus_INPUTS_READY, Image: ImageStatus_IMAGE_FAILED},
		PreparationStatus_PULLING:                    {Inputs: InputsStatus_INPUTS_READY, Image: ImageStatus_IMAGE_PULLING},
	}
	for legacy, staged := range backfill {
		if got := staged.Rollup(); got != legacy {
			t.Errorf("legacy %v backfilled to inputs=%v image=%v, which re-derives to %v", legacy, staged.Inputs, staged.Image, got)
		}
	}
}
