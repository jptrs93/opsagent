package runner

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage"
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
		"SECRET": {SecretRefID: &secretID},
		"CONFIG": {ConfigRefID: &configID},
		"ASSET":  {Asset: "bundle", AssetVersionID: 3},
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
				{AssetVersionID: 8, ContainerPath: "/etc/app.conf", Permission: apigen.FilePermission_READ_ONLY},
				{AssetVersionID: 9, ContainerPath: "/docker-entrypoint-initdb.d/init.sh", Permission: apigen.FilePermission_READ_EXECUTE},
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
	r := buildContainerRunner(ctx, cancel, &fakeOperatorStore{}, nil, systemdTestInstanceID, &apigen.DeploymentConfig{
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

// TestOnlyServingPlacementClaimsInboundAddress pins the gate that keeps two
// nodes from holding a host route for one instance address. During a cross-node
// rollover the standby runs a full runner on the other node; if it claimed the
// address on startup, traffic originating on each node would reach a different
// container until the cluster map caught up.
//
// The assertion leans on Activate being unavailable off Linux: a runner that
// declines to claim returns nil without reaching it, while one that claims
// surfaces the platform error. Reaching Activate at all is the behaviour under
// test.
func TestOnlyServingPlacementClaimsInboundAddress(t *testing.T) {
	previous := network.Default
	network.SetDefault(network.New(network.GeneratePrefix(), 7))
	t.Cleanup(func() { network.SetDefault(previous) })

	newRunner := func() *containerRunner {
		return &containerRunner{
			deploymentID:  42,
			spaceID:       1,
			configVersion: 3,
			latestVersion: 3,
			networking:    apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL},
			net:           &network.ContainerNet{ContainerID: "opendeploy-42-v3", DeploymentID: 42},
		}
	}

	standby := newRunner()
	if err := standby.claimInboundAddress(standby.getContainerNet()); err != nil {
		t.Fatalf("a placement that is not serving must not claim the address: %v", err)
	}

	serving := newRunner()
	if err := serving.Serve(); err == nil {
		t.Fatal("a serving placement must claim the address (expected the non-linux Activate error)")
	}

	// Host-network deployments have no instance address to claim at all.
	host := newRunner()
	host.networking.Mode = apigen.NetworkingMode_NETWORKING_MODE_HOST
	if err := host.Serve(); err != nil {
		t.Fatalf("host networking must not claim an address: %v", err)
	}

	// A run superseded on this node must not take the address back from its
	// replacement, even though both belong to the same serving placement.
	superseded := newRunner()
	superseded.latestVersion = 4
	if err := superseded.Serve(); err != nil {
		t.Fatalf("a superseded run must not reclaim the address: %v", err)
	}
}

func rolloverTestDeployment() *apigen.DeploymentConfig {
	return &apigen.DeploymentConfig{
		ID:      7,
		Version: 3,
		Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{
			UpgradeStrategy: apigen.ContainerUpgradeStrategy_ROLLOVER,
			Runtime:         apigen.ContainerRuntime{DefaultVolume: apigen.DefaultVolumeMount{Disabled: true}},
		}},
	}
}

func newTestCandidate(t *testing.T, store storage.OperatorStore) *containerRunner {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dep := rolloverTestDeployment()
	r := buildContainerRunner(ctx, cancel, store, nil, systemdTestInstanceID, dep, 3)
	r.initFreshRun(dep, apigen.PreparerStatus{DeploymentConfigVersion: 3, Artifact: "example/app:v3"}, true)
	return r
}

// TestRolloverCandidatePublishesStatus is the regression guard for a rollover
// candidate that dies during startup. Candidates used to suppress every status
// write to avoid clobbering an incumbent runner, but an instance's config is
// pinned to its version, so a candidate is only ever created when nothing else
// is running for that instance. The suppression bought nothing and cost
// everything: a crash before the readiness signal wrote no status, so no
// notification was published, so the operator never woke to retry and the
// rollout stalled with no trace of why.
func TestRolloverCandidatePublishesStatus(t *testing.T) {
	store := &fakeOperatorStore{}
	r := newTestCandidate(t, store)

	statuses := store.runnerStatuses()
	if len(statuses) != 1 {
		t.Fatalf("status writes after construction = %d, want 1", len(statuses))
	}
	if statuses[0].Status != apigen.RunningStatus_STARTING {
		t.Fatalf("initial candidate status = %v, want STARTING", statuses[0].Status)
	}
	if !r.readinessPending.Load() {
		t.Fatal("a candidate must start with the readiness gate closed")
	}

	// A crash before the readiness signal must reach the store, which is both
	// what the FE renders and what wakes the operator.
	r.updateStatus(apigen.RunningStatus_CRASHED, 0)
	statuses = store.runnerStatuses()
	if len(statuses) != 2 || statuses[1].Status != apigen.RunningStatus_CRASHED {
		t.Fatalf("statuses = %+v, want a published CRASHED", statuses)
	}
}

// TestCandidateReadinessOutcomes pins which failures respawn and which give up.
// A candidate that reports nothing keeps the operator blocked on WaitReady,
// which is deliberate: the operator must not build a second candidate while
// this one is still retrying.
func TestCandidateReadinessOutcomes(t *testing.T) {
	closed := func() {}

	t.Run("container exits before signalling", func(t *testing.T) {
		r := newTestCandidate(t, &fakeOperatorStore{})
		exitDone := make(chan struct{})
		close(exitDone)

		outcome, taskExited, err := r.waitForReadiness(make(chan error), closed, exitDone)
		if outcome != readinessRetry {
			t.Fatalf("outcome = %v, want readinessRetry", outcome)
		}
		if !taskExited || err == nil {
			t.Fatalf("taskExited = %v, err = %v, want true and an error", taskExited, err)
		}
		select {
		case <-r.readyCh:
			t.Fatal("a retryable attempt must not release the operator")
		default:
		}
	})

	t.Run("deadline passes", func(t *testing.T) {
		r := newTestCandidate(t, &fakeOperatorStore{})
		r.readinessDeadline = time.Now().Add(-time.Second)

		outcome, _, err := r.waitForReadiness(make(chan error), closed, make(chan struct{}))
		if outcome != readinessGiveUp || err == nil {
			t.Fatalf("outcome = %v, err = %v, want readinessGiveUp and an error", outcome, err)
		}
	})

	t.Run("signal received", func(t *testing.T) {
		r := newTestCandidate(t, &fakeOperatorStore{})
		ready := make(chan error, 1)
		ready <- nil

		outcome, _, err := r.waitForReadiness(ready, closed, make(chan struct{}))
		if outcome != readinessSignalled || err != nil {
			t.Fatalf("outcome = %v, err = %v, want readinessSignalled", outcome, err)
		}
	})
}

// TestFailReadinessReleasesTheOperator covers giving up: the operator is blocked
// on WaitReady, so abandoning a candidate without notifying it would wedge the
// rollover for good.
func TestFailReadinessReleasesTheOperator(t *testing.T) {
	store := &fakeOperatorStore{}
	r := newTestCandidate(t, store)

	r.failReadiness(errors.New("never became ready"))

	if r.readinessPending.Load() {
		t.Fatal("giving up must open the readiness gate so no attempt waits on it again")
	}
	if err := r.WaitReady(); err == nil {
		t.Fatal("WaitReady() = nil, want the readiness failure")
	}
	statuses := store.runnerStatuses()
	if got := statuses[len(statuses)-1].Status; got != apigen.RunningStatus_CRASHED {
		t.Fatalf("final status = %v, want CRASHED", got)
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

	// Two scheduled instances of one deployment must not share an outbound
	// address. Keying the slot on config version failed here, because moving an
	// instance to another node creates a second placement at the same version.
	_, otherPlacement, err := containerNetAddresses(p, 5, 7, 12, 3)
	if err != nil {
		t.Fatal(err)
	}
	if otherPlacement == outbound {
		t.Fatal("two scheduled instances received the same outbound address")
	}
}
