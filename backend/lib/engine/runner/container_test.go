package runner

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

func TestComputeContainerBackoff(t *testing.T) {
	tests := []struct {
		crashCount int
		want       time.Duration
	}{
		{crashCount: 0, want: time.Second},
		{crashCount: 1, want: time.Second},
		{crashCount: 2, want: 2 * time.Second},
		{crashCount: 7, want: 64 * time.Second},
		{crashCount: 12, want: 2048 * time.Second},
		{crashCount: 13, want: time.Hour},
		{crashCount: 100, want: time.Hour},
	}

	for _, tt := range tests {
		if got := computeContainerBackoff(tt.crashCount); got != tt.want {
			t.Errorf("computeContainerBackoff(%d) = %s, want %s", tt.crashCount, got, tt.want)
		}
	}
}

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
		"ASSET":  {Asset: "bundle", AssetID: 3},
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
			if got := defaultVolumeDest(tt.override); got != tt.want {
				t.Fatalf("defaultVolumeDest(%q) = %q, want %q", tt.override, got, tt.want)
			}
		})
	}
}

func TestContainerMountsUsesExecutableAssetCachePath(t *testing.T) {
	dep := &apigen.DeploymentConfig{
		ID: 7,
		Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{Runtime: apigen.ContainerRuntime{
			DefaultVolume: apigen.DefaultVolumeMount{Disabled: true},
			AssetMounts: []*apigen.AssetMount{
				{AssetID: 8, ContainerPath: "/etc/app.conf", Permission: apigen.FilePermission_READ_ONLY},
				{AssetID: 9, ContainerPath: "/docker-entrypoint-initdb.d/init.sh", Permission: apigen.FilePermission_READ_EXECUTE},
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
	if mounts[0].Source != runtimeinputs.AssetCachePathWithMode(8, false) || !mounts[0].ReadOnly {
		t.Fatalf("readonly asset mount = %+v", mounts[0])
	}
	if mounts[1].Source != runtimeinputs.AssetCachePathWithMode(9, true) || !mounts[1].ReadOnly {
		t.Fatalf("executable asset mount = %+v", mounts[1])
	}
}

func TestBuildContainerRunnerUsesResourceOverrides(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := buildContainerRunner(ctx, cancel, &fakeOperatorStore{}, nil, &apigen.DeploymentConfig{
		ID:       7,
		Version:  3,
		Identity: apigen.DeploymentIdentity{SpaceID: 5},
		Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{Runtime: apigen.ContainerRuntime{
			DefaultVolume:       apigen.DefaultVolumeMount{Disabled: true},
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
	if r.spaceID != 5 {
		t.Fatalf("spaceID = %d, want 5", r.spaceID)
	}
	if r.latestVersion != 3 {
		t.Fatalf("latestVersion = %d, want 3", r.latestVersion)
	}
}

func TestContainerMountsTranslatesV2MountsAndPermissions(t *testing.T) {
	oldVolumesDir := ainit.StaticConfig.VolumesDir
	ainit.StaticConfig.VolumesDir = "/var/lib/opendeploy-volumes"
	t.Cleanup(func() { ainit.StaticConfig.VolumesDir = oldVolumesDir })

	dep := &apigen.DeploymentConfig{
		ID: 7,
		Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{Runtime: apigen.ContainerRuntime{
			DefaultVolume: apigen.DefaultVolumeMount{ContainerPath: "/state"},
			CrossDeploymentMounts: []*apigen.CrossDeploymentMount{
				{DeploymentID: 12, ContainerPath: "/shared-ro", Permission: apigen.FilePermission_READ_ONLY},
				{DeploymentID: 13, ContainerPath: "/shared-rw", Permission: apigen.FilePermission_READ_WRITE},
			},
			Mounts: []*apigen.CustomHostMount{
				{HostPath: "/host/config", ContainerPath: "/config", Permission: apigen.FilePermission_READ_ONLY},
			},
		}}},
	}

	mounts, dataHost := containerMounts(dep)
	if dataHost != "/var/lib/opendeploy-volumes/7/default" {
		t.Fatalf("dataHost = %q", dataHost)
	}
	want := []ctrd.Mount{
		{Source: "/var/lib/opendeploy-volumes/7/default", Dest: "/state"},
		{Source: "/var/lib/opendeploy-volumes/12/default", Dest: "/shared-ro", ReadOnly: true},
		{Source: "/var/lib/opendeploy-volumes/13/default", Dest: "/shared-rw"},
		{Source: "/host/config", Dest: "/config", ReadOnly: true},
	}
	if !reflect.DeepEqual(mounts, want) {
		t.Fatalf("mounts = %#v; want %#v", mounts, want)
	}
}

func TestContainerRunnerSignalsMissingArtifactOnce(t *testing.T) {
	r := &containerRunner{artifactMissing: make(chan struct{}, 1)}
	r.notifyArtifactMissing()
	r.notifyArtifactMissing()

	select {
	case <-r.ArtifactMissing():
	default:
		t.Fatal("missing artifact was not signaled")
	}
	select {
	case <-r.ArtifactMissing():
		t.Fatal("missing artifact signal was queued more than once")
	default:
	}
}

func TestUsesLatestNetworkConfigAcrossNetproxyVersionUpgrade(t *testing.T) {
	previous := network.Default
	network.SetDefault(network.New(network.GeneratePrefix(), 7))
	t.Cleanup(func() { network.SetDefault(previous) })

	if !(&containerRunner{deploymentID: 7, configVersion: 1, latestVersion: 2}).usesLatestNetworkConfig() {
		t.Fatal("netproxy version-only upgrade should use the current internal network config")
	}
	if (&containerRunner{deploymentID: 8, configVersion: 1, latestVersion: 2}).usesLatestNetworkConfig() {
		t.Fatal("ordinary deployment with an older runner must not use the latest network config")
	}
}

func TestContainerNetAddresses(t *testing.T) {
	p := network.Prefix{0xfd, 0xab, 0xcd, 0xef, 0x12, 0x34}
	inboundWant, err := p.InboundAddr(5, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	outboundWant, err := p.OutboundAddr(5, 7, 0, 11, 3)
	if err != nil {
		t.Fatal(err)
	}

	inbound, outbound, err := containerNetAddresses(p, 5, 7, 11, 3)
	if err != nil {
		t.Fatal(err)
	}
	if inbound != inboundWant {
		t.Fatalf("inbound address = %v, want %v", inbound, inboundWant)
	}
	if outbound != outboundWant {
		t.Fatalf("outbound address = %v, want %v", outbound, outboundWant)
	}

	_, nextRun, err := containerNetAddresses(p, 5, 7, 11, 4)
	if err != nil {
		t.Fatal(err)
	}
	if nextRun == outbound {
		t.Fatal("successive runs received the same outbound address")
	}
}
