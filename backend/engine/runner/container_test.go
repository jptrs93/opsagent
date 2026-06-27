package runner

import (
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/engine/preparer"
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

func TestDefaultVolumeDest(t *testing.T) {
	tests := []struct {
		name     string
		user     string
		override string
		want     string
	}{
		{name: "root", user: "", want: "/data"},
		{name: "named user", user: "app", want: "/data"},
		{name: "numeric user", user: "1000:1000", want: "/data"},
		{name: "override", user: "app", override: "/srv/data", want: "/srv/data"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultVolumeDest(tt.user, tt.override); got != tt.want {
				t.Fatalf("defaultVolumeDest(%q, %q) = %q, want %q", tt.user, tt.override, got, tt.want)
			}
		})
	}
}

func TestContainerMountsUsesExecutableAssetCachePath(t *testing.T) {
	dep := &apigen.DeploymentConfig{
		ID: 7,
		Spec: apigen.DeploymentSpec{Runner: apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{
			DisableDataVolume: true,
			AssetMounts: []*apigen.ContainerAssetMount{
				{AssetID: 8, Version: 2, Path: "/etc/app.conf"},
				{AssetID: 9, Version: 3, Path: "/docker-entrypoint-initdb.d/init.sh", Executable: true},
			},
		}}},
	}

	mounts, dataHost := containerMounts(dep)
	if dataHost != "" {
		t.Fatalf("dataHost = %q, want empty", dataHost)
	}
	if len(mounts) != 2 {
		t.Fatalf("mounts len = %d, want 2", len(mounts))
	}
	if mounts[0].Source != preparer.AssetCachePathWithMode(8, 2, false) || !mounts[0].ReadOnly {
		t.Fatalf("readonly asset mount = %+v", mounts[0])
	}
	if mounts[1].Source != preparer.AssetCachePathWithMode(9, 3, true) || !mounts[1].ReadOnly {
		t.Fatalf("executable asset mount = %+v", mounts[1])
	}
}

func TestBuildContainerRunnerUsesResourceOverrides(t *testing.T) {
	r := buildContainerRunner(nil, nil, nil, &apigen.DeploymentConfig{
		ID: 7,
		Spec: apigen.DeploymentSpec{Runner: apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{
			DisableDataVolume:   true,
			DevShmSizeKb:        65536,
			FileDescriptorLimit: 4096,
		}}},
	}, 3)
	if r.devShmSizeKB != 65536 {
		t.Fatalf("devShmSizeKB = %d, want 65536", r.devShmSizeKB)
	}
	if r.fileDescLimit != 4096 {
		t.Fatalf("fileDescLimit = %d, want 4096", r.fileDescLimit)
	}
}
