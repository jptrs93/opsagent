package runner

import (
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/ctrd"
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

func TestCountEnvVars(t *testing.T) {
	plain := "value"
	secretID := int32(1)
	configID := int32(2)
	got := countEnvVars(map[string]*apigen.EnvVarValue{
		"PLAIN":  {Value: &plain},
		"SECRET": {SecretID: &secretID},
		"CONFIG": {ConfigID: &configID},
		"ASSET":  {Asset: "bundle", AssetID: 3, Version: 4},
		"NIL":    nil,
	})

	if got.plain != 1 || got.secret != 1 || got.config != 1 || got.asset != 1 {
		t.Fatalf("countEnvVars() = %+v, want one of each", got)
	}
}

func TestFormatMountPaths(t *testing.T) {
	got := formatMountPaths([]ctrd.Mount{
		{Source: "/host/var", Dest: "/var"},
		{Source: "/host/config", Dest: "/etc/config", ReadOnly: true},
	})
	want := "/host/var->/var(rw);/host/config->/etc/config(ro)"
	if got != want {
		t.Fatalf("formatMountPaths() = %q, want %q", got, want)
	}
}
