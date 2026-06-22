package runner

import (
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestContainerRunnerShouldPublishStopped(t *testing.T) {
	tests := []struct {
		name string
		st   apigen.RunnerStatus
		want bool
	}{
		{
			name: "already stopped",
			st: apigen.RunnerStatus{
				Status: apigen.RunningStatus_STOPPED,
			},
			want: false,
		},
		{
			name: "stopped stale pid",
			st: apigen.RunnerStatus{
				Status:     apigen.RunningStatus_STOPPED,
				RunningPid: 123,
			},
			want: true,
		},
		{
			name: "no deployment",
			st: apigen.RunnerStatus{
				Status: apigen.RunningStatus_NO_DEPLOYMENT,
			},
			want: false,
		},
		{
			name: "running",
			st: apigen.RunnerStatus{
				Status: apigen.RunningStatus_RUNNING,
			},
			want: true,
		},
		{
			name: "crashed",
			st: apigen.RunnerStatus{
				Status: apigen.RunningStatus_CRASHED,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &containerRunner{status: tt.st}
			if got := r.shouldPublishStopped(); got != tt.want {
				t.Fatalf("shouldPublishStopped() = %v, want %v", got, tt.want)
			}
		})
	}
}
