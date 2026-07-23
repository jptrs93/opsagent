package apigen

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDeploymentConfigV2ContainerRoundTrip(t *testing.T) {
	secretID := int32(7)
	value := "literal"
	in := &DeploymentConfig{
		ID:        42,
		NodeID:    3,
		Identity:  DeploymentIdentity{SpaceID: 2, Name: "web"},
		Version:   8,
		UpdatedAt: time.Unix(20, 30),
		UpdatedBy: 4,
		Spec: DeploymentSpec{
			Prepare: PrepareConfig{NixDockerBuild: &NixDockerBuildConfig{Repo: "github.com/acme/web", Flake: "flake.nix", Target: ".#image"}},
			Runner: RunnerConfig{Container: ContainerRunnerConfig{
				User:                "1000:1000",
				EnvVars:             map[string]*EnvVarValue{"SECRET": {SecretID: &secretID}, "VALUE": {Value: &value}},
				Command:             []string{"/app", "serve"},
				WorkingDir:          "/srv",
				DataMountPath:       "/data",
				Mounts:              []*ContainerMount{{Host: "/host/config", Container: "/config", Readonly: true}},
				AssetMounts:         []*ContainerAssetMount{{AssetID: 12, Path: "/assets/tool", Executable: true}},
				UpgradeStrategy:     ContainerUpgradeStrategy_ROLLOVER,
				ReadinessSignal:     &ContainerReadinessSignal{TimeoutSeconds: 15},
				DevShmSizeKb:        1024,
				FileDescriptorLimit: 4096,
				DisableDataVolume:   false,
			}},
			Networking: NetworkingConfig{
				Mode:           NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []*PortForward{{Protocol: PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 8443, ContainerPort: 443}},
				Ingress: []*Ingress{{
					Kind:                 IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
					Hostname:             "web.example.com",
					TlsPassthroughConfig: &TlsPassthroughConfig{HostPort: 443, ContainerPort: 8443},
				}},
			},
		},
		DesiredState: DesiredState{Version: "abc123", Running: true},
		Deleted:      false,
		CreatedAt:    time.Unix(10, 20),
	}

	v2, err := DeploymentConfigToV2(in)
	if err != nil {
		t.Fatalf("DeploymentConfigToV2: %v", err)
	}
	if v2.Spec.Container1Spec == nil {
		t.Fatalf("converted config has no primary container: %+v", v2.Spec)
	}
	if v2.Spec.Container1Spec.Version != "abc123" || !v2.Spec.Container1Spec.Running {
		t.Fatalf("desired state was not folded into container spec: %+v", v2.Spec.Container1Spec)
	}

	got, err := DeploymentConfigFromV2(v2)
	if err != nil {
		t.Fatalf("DeploymentConfigFromV2: %v", err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip mismatch\ngot:  %#v\nwant: %#v", got, in)
	}
}

func TestDeploymentConfigV2SystemdRoundTrip(t *testing.T) {
	in := &DeploymentConfig{
		ID:       1,
		NodeID:   1,
		Identity: DeploymentIdentity{SpaceID: 1, Name: "OPENDEPLOY"},
		Spec: DeploymentSpec{
			Prepare:    PrepareConfig{GithubRelease: &GithubReleaseConfig{Repo: "github.com/acme/opendeploy", Asset: "opendeploy-linux-amd64"}},
			Runner:     RunnerConfig{Systemd: SystemdRunnerConfig{Name: "opendeploy", BinPath: "/usr/local/bin/opendeploy"}},
			Networking: NetworkingConfig{Mode: NetworkingMode_NETWORKING_MODE_HOST},
		},
		DesiredState: DesiredState{Version: "v1.2.3", Running: true},
	}

	v2, err := DeploymentConfigToV2(in)
	if err != nil {
		t.Fatalf("DeploymentConfigToV2: %v", err)
	}
	if v2.Spec.SystemdSpec == nil || v2.Spec.Container1Spec != nil {
		t.Fatalf("converted systemd spec has wrong variant: %+v", v2.Spec)
	}
	got, err := DeploymentConfigFromV2(v2)
	if err != nil {
		t.Fatalf("DeploymentConfigFromV2: %v", err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip mismatch\ngot:  %#v\nwant: %#v", got, in)
	}
}

func TestDeploymentConfigV2ToV1RoundTrip(t *testing.T) {
	in := &DeploymentConfig2{
		ID:       9,
		NodeID:   4,
		Identity: DeploymentIdentity{SpaceID: 3, Name: "api"},
		Spec: DeploymentSpec2{
			Networking: NetworkingConfig{Mode: NetworkingMode_NETWORKING_MODE_HOST},
			Container1Spec: &ContainerSpec{
				Source: ContainerBundleSource{RemoteImage: &RemoteDockerImage{Image: "ghcr.io/acme/api"}},
				Runtime: ContainerRuntime{
					DefaultVolume: DefaultVolumeMount{Disabled: true},
					Mounts:        []*CustomHostMount{{HostPath: "/host/data", ContainerPath: "/data", Permission: FilePermission_READ_WRITE}},
					AssetMounts:   []*AssetMount2{{AssetID: 22, ContainerPath: "/etc/api.conf", Permission: FilePermission_READ_ONLY}},
				},
				Version: "v4",
				Running: true,
			},
		},
	}

	v1, err := DeploymentConfigFromV2(in)
	if err != nil {
		t.Fatalf("DeploymentConfigFromV2: %v", err)
	}
	got, err := DeploymentConfigToV2(v1)
	if err != nil {
		t.Fatalf("DeploymentConfigToV2: %v", err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip mismatch\ngot:  %#v\nwant: %#v", got, in)
	}
}

func TestDeploymentSpecToV2DropsAssetMetadata(t *testing.T) {
	spec := &DeploymentSpec{
		Prepare: PrepareConfig{ContainerImage: &ContainerImageConfig{Image: "example/app"}},
		Runner: RunnerConfig{Container: ContainerRunnerConfig{AssetMounts: []*ContainerAssetMount{{
			AssetID: 1,
			Asset:   "config",
			Path:    "/config",
			Format:  "yaml",
		}}}},
	}
	got, err := DeploymentSpecToV2(spec, DesiredState{})
	if err != nil {
		t.Fatalf("DeploymentSpecToV2: %v", err)
	}
	if got.Container1Spec == nil || len(got.Container1Spec.Runtime.AssetMounts) != 1 {
		t.Fatalf("converted asset mounts = %+v", got.Container1Spec)
	}
}

func TestDeploymentSpecToV2DropsGithubReleaseTag(t *testing.T) {
	spec := &DeploymentSpec{
		Prepare: PrepareConfig{GithubRelease: &GithubReleaseConfig{
			Repo:  "github.com/acme/opendeploy",
			Asset: "opendeploy-linux-amd64",
			Tag:   "ignored-tag",
		}},
		Runner: RunnerConfig{Systemd: SystemdRunnerConfig{Name: "opendeploy"}},
	}
	desired := DesiredState{Version: "v1.2.3", Running: true}

	got, err := DeploymentSpecToV2(spec, desired)
	if err != nil {
		t.Fatalf("DeploymentSpecToV2: %v", err)
	}
	if got.SystemdSpec == nil || got.SystemdSpec.Version != desired.Version {
		t.Fatalf("converted systemd spec = %+v, want version %q", got.SystemdSpec, desired.Version)
	}
}

func TestDeploymentSpecCrossDeploymentMountRoundTrip(t *testing.T) {
	in := &DeploymentSpec{
		Prepare: PrepareConfig{ContainerImage: &ContainerImageConfig{Image: "example/app"}},
		Runner: RunnerConfig{Container: ContainerRunnerConfig{Mounts: []*ContainerMount{{
			Host:      "/var/lib/opendeploy-volumes/42/default",
			Container: "/data/shared",
			Readonly:  true,
		}}}},
	}
	v2, err := DeploymentSpecToV2(in, DesiredState{})
	if err != nil {
		t.Fatalf("DeploymentSpecToV2: %v", err)
	}
	runtime := v2.Container1Spec.Runtime
	if len(runtime.CrossDeploymentMounts) != 1 || len(runtime.Mounts) != 0 {
		t.Fatalf("converted mounts = cross:%+v custom:%+v", runtime.CrossDeploymentMounts, runtime.Mounts)
	}
	if mount := runtime.CrossDeploymentMounts[0]; mount.DeploymentID != 42 || mount.ContainerPath != "/data/shared" || mount.Permission != FilePermission_READ_ONLY {
		t.Fatalf("converted cross-deployment mount = %+v", mount)
	}
	got, _, err := DeploymentSpecFromV2(v2)
	if err != nil {
		t.Fatalf("DeploymentSpecFromV2: %v", err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip mismatch\ngot:  %#v\nwant: %#v", got, in)
	}
}

func TestDeploymentSpecFromV2RejectsUnsupportedVariants(t *testing.T) {
	tests := []struct {
		name string
		spec *DeploymentSpec2
		want string
	}{
		{
			name: "second container",
			spec: &DeploymentSpec2{Container2Spec: &ContainerSpec{Source: ContainerBundleSource{RemoteImage: &RemoteDockerImage{Image: "example/app"}}}},
			want: "multiple containers",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := DeploymentSpecFromV2(tt.spec)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateDeploymentSpec2(t *testing.T) {
	validContainer := func() *ContainerSpec {
		return &ContainerSpec{Source: ContainerBundleSource{RemoteImage: &RemoteDockerImage{Image: "example/app"}}}
	}
	tests := []struct {
		name string
		spec *DeploymentSpec2
		want string
	}{
		{name: "no workload", spec: &DeploymentSpec2{}, want: "exactly one"},
		{
			name: "mixed workloads",
			spec: &DeploymentSpec2{Container1Spec: validContainer(), SystemdSpec: &SystemdSpec{}},
			want: "exactly one",
		},
		{
			name: "multiple sources",
			spec: &DeploymentSpec2{Container1Spec: &ContainerSpec{Source: ContainerBundleSource{
				NixDockerBuild: &NixDockerBuild2{},
				RemoteImage:    &RemoteDockerImage{},
			}}},
			want: "source has 2 fields",
		},
		{
			name: "writable asset",
			spec: &DeploymentSpec2{Container1Spec: &ContainerSpec{
				Source:  ContainerBundleSource{RemoteImage: &RemoteDockerImage{}},
				Runtime: ContainerRuntime{AssetMounts: []*AssetMount2{{Permission: FilePermission_READ_WRITE}}},
			}},
			want: "READ_ONLY or READ_EXECUTE",
		},
		{
			name: "executable host mount",
			spec: &DeploymentSpec2{Container1Spec: &ContainerSpec{
				Source:  ContainerBundleSource{RemoteImage: &RemoteDockerImage{}},
				Runtime: ContainerRuntime{Mounts: []*CustomHostMount{{Permission: FilePermission_READ_EXECUTE}}},
			}},
			want: "permission is invalid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDeploymentSpec2(tt.spec)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}

	if err := ValidateDeploymentSpec2(&DeploymentSpec2{Container1Spec: validContainer()}); err != nil {
		t.Fatalf("valid deployment spec rejected: %v", err)
	}
}
