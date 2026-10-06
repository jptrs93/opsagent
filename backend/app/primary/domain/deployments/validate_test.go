package deployments

import (
	"github.com/jptrs93/opsagent/backend/app/primary/domain/assets"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
)

const testNixCommit = "0123456789abcdef0123456789abcdef01234567"
const testNixCommit2 = "89abcdef0123456789abcdef0123456789abcdef"

func hostNetworking() apigen.NetworkingConfig {
	return apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST}
}

func virtualNetworking() apigen.NetworkingConfig {
	return apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL}
}

func containerWorkload(container *apigen.ContainerSpec) apigen.Workload {
	if container.UpgradeStrategy == apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_UNSPECIFIED {
		container.UpgradeStrategy = apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE
	}
	return apigen.Workload{Value: apigen.WorkloadValueOneof{Container: container}}
}

func remoteSource(image string) apigen.ContainerSource {
	return apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{RemoteImage: &apigen.RemoteImage{Image: image}}}
}

func nixSourceOf(repo, flake, target string) apigen.ContainerSource {
	return apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{NixImageBuild: &apigen.NixImageBuild{Repo: repo, Flake: flake, Target: target}}}
}

func mustPrefix(s string) apigen.IpPrefix {
	p, err := apigen.ParsePrefix(s)
	if err != nil {
		panic(err)
	}
	return p
}

func remoteDeploymentSpec(image string, networking apigen.NetworkingConfig) apigen.DeploymentSpec {
	return apigen.DeploymentSpec{
		Workload:   containerWorkload(&apigen.ContainerSpec{Source: remoteSource(image)}),
		Networking: networking,
	}
}

func TestValidateDeploymentSpecNixDockerBuild(t *testing.T) {
	spec, err := ValidateSpecWithResolvers(&apigen.DeploymentSpec{
		Workload: containerWorkload(&apigen.ContainerSpec{
			Source:  nixSourceOf("github.com/acme/web", "nix/web/flake.nix", ".#webImage"),
			Runtime: apigen.ContainerRuntime{User: "1000"},
		}),
		Networking: hostNetworking(),
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithResolvers failed: %v", err)
	}
	nix := spec.Container().Source.Value.NixImageBuild
	if nix == nil {
		t.Fatal("nixImageBuild is nil")
	}
	if nix.Repo != "github.com/acme/web" {
		t.Fatalf("repo = %q", nix.Repo)
	}
	if nix.Flake != "nix/web/flake.nix" {
		t.Fatalf("flake = %q", nix.Flake)
	}
	if nix.Target != ".#webImage" {
		t.Fatalf("target = %q", nix.Target)
	}
	if spec.Container().Runtime.User != "1000" {
		t.Fatalf("container user = %q", spec.Container().Runtime.User)
	}
}

func TestValidateDeploymentSpecCanonicalizesSafeFlakePath(t *testing.T) {
	spec, err := ValidateSpecWithResolvers(&apigen.DeploymentSpec{
		Workload:   containerWorkload(&apigen.ContainerSpec{Source: nixSourceOf("github.com/acme/web", "./nix/../flake.nix", "")}),
		Networking: hostNetworking(),
	}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.Container().Source.Value.NixImageBuild.Flake; got != "flake.nix" {
		t.Fatalf("flake = %q, want flake.nix", got)
	}

	for _, flake := range []string{"/flake.nix", "../flake.nix", "nix/default.nix"} {
		t.Run(flake, func(t *testing.T) {
			_, err := ValidateSpecWithResolvers(&apigen.DeploymentSpec{
				Workload:   containerWorkload(&apigen.ContainerSpec{Source: nixSourceOf("github.com/acme/web", flake, "")}),
				Networking: hostNetworking(),
			}, nil, nil, nil)
			if err == nil {
				t.Fatalf("flake path %q was accepted", flake)
			}
		})
	}
}

func nixCreateRequest(nodeID uint64, name string, running bool) *apigen.DeploymentCreateRequest {
	return &apigen.DeploymentCreateRequest{
		SpaceID: 1, Name: name,
		Scheduling: apigen.DedicatedScheduling(running, nodeID),
		Spec:       nixDeploymentSpecWithVersion("github.com/acme/app", "flake.nix", testNixCommit),
	}
}

func nixDeploymentSpec(repo, flake string) apigen.DeploymentSpec {
	return nixDeploymentSpecWithVersion(repo, flake, testNixCommit)
}

func nixDeploymentSpecWithVersion(repo, flake, version string) apigen.DeploymentSpec {
	return apigen.DeploymentSpec{
		Workload:   containerWorkload(&apigen.ContainerSpec{Source: nixSourceOf(repo, flake, ""), Version: version}),
		Networking: hostNetworking(),
	}
}

func TestValidateDeploymentSpecRejectsNonLocalNixTarget(t *testing.T) {
	_, err := ValidateSpecWithResolvers(&apigen.DeploymentSpec{
		Workload:   containerWorkload(&apigen.ContainerSpec{Source: nixSourceOf("github.com/acme/web", "flake.nix", "github:acme/web#image")}),
		Networking: hostNetworking(),
	}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "local flake selector") {
		t.Fatalf("err = %v, want local target rejection", err)
	}
}

type fakeAssetResolver map[string]assets.AssetVersionRef

func (r fakeAssetResolver) GetAssetVersionRef(ref apigen.ValueRef) (assets.AssetVersionRef, bool) {
	for _, asset := range r {
		if asset.Ref == ref {
			return asset, true
		}
	}
	return assets.AssetVersionRef{}, false
}

type fakeSecretResolver map[apigen.ValueRef]string

func (r fakeSecretResolver) MetaByRef(ref apigen.ValueRef) (secrets.Meta, bool) {
	_, ok := r[ref]
	return secrets.Meta{SecretID: ref.ID, Version: ref.Version}, ok
}

type fakeConfigResolver map[apigen.ValueRef]string

func (r fakeConfigResolver) ResolveConfig(ref apigen.ValueRef) (string, bool) {
	v, ok := r[ref]
	return v, ok
}

func TestValidateDeploymentSpecResolvesAssetMounts(t *testing.T) {
	assets := fakeAssetResolver{
		"nginx.conf": {
			Ref: apigen.ValueRef{ID: 1, Version: 42},
			Key: "nginx.conf",
		},
	}
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container().Runtime.AssetMounts = []apigen.AssetMount{{
		Asset: apigen.AssetRef{AssetID: 1, Version: 42}, ContainerPath: "/etc/nginx/nginx.conf", Permission: apigen.FilePermission_FILE_PERMISSION_READ_EXECUTE,
	}}
	spec, err := ValidateSpecWithResolvers(&input, assets, nil, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithResolvers failed: %v", err)
	}
	mounts := spec.Container().Runtime.AssetMounts
	if len(mounts) != 1 {
		t.Fatalf("asset mounts len = %d", len(mounts))
	}
	if mounts[0].Asset.Ref() != (apigen.ValueRef{ID: 1, Version: 42}) || mounts[0].ContainerPath != "/etc/nginx/nginx.conf" || mounts[0].Permission != apigen.FilePermission_FILE_PERMISSION_READ_EXECUTE {
		t.Fatalf("asset mount not resolved: %+v", mounts[0])
	}
}

func TestValidateDeploymentSpecResolvesEnvAssetRefs(t *testing.T) {
	assets := fakeAssetResolver{
		"app.conf": {
			Ref: apigen.ValueRef{ID: 2, Version: 51},
			Key: "app.conf",
		},
	}
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container().Runtime.EnvVars = map[string]apigen.EnvVar{"APP_CONFIG": {Value: apigen.EnvVarValueOneof{Asset: &apigen.AssetEnv{Asset: apigen.AssetRef{AssetID: 2, Version: 51}}}}}
	spec, err := ValidateSpecWithResolvers(&input, assets, nil, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithResolvers failed: %v", err)
	}
	value := spec.Container().Runtime.EnvVars["APP_CONFIG"].Value.Asset
	if value == nil || value.Key != "app.conf" || value.Asset.Ref() != (apigen.ValueRef{ID: 2, Version: 51}) {
		t.Fatalf("env asset ref not resolved: %+v", value)
	}
}

func TestValidateDeploymentSpecRejectsUnknownEnvAssetRef(t *testing.T) {
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container().Runtime.EnvVars = map[string]apigen.EnvVar{"APP_CONFIG": {Value: apigen.EnvVarValueOneof{Asset: &apigen.AssetEnv{Asset: apigen.AssetRef{AssetID: 2, Version: 999}}}}}
	_, err := ValidateSpecWithResolvers(&input, fakeAssetResolver{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), `asset 2@999 not found`) {
		t.Fatalf("err = %v, want unknown asset", err)
	}
}

func TestValidateDeploymentSpecAcceptsHostMounts(t *testing.T) {
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container().Runtime.Mounts = []apigen.HostMount{{
		HostPath: " /home/ubuntu/coflip-server/data ", ContainerPath: " /data ", Permission: apigen.FilePermission_FILE_PERMISSION_READ_WRITE,
	}}
	spec, err := ValidateSpecWithResolvers(&input, nil, nil, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithResolvers failed: %v", err)
	}
	mount := spec.Container().Runtime.Mounts[0]
	if mount.HostPath != "/home/ubuntu/coflip-server/data" || mount.ContainerPath != "/data" || mount.Permission != apigen.FilePermission_FILE_PERMISSION_READ_WRITE {
		t.Fatalf("mount not normalized: %+v", mount)
	}
}

func TestValidateDeploymentSpecValidatesMounts(t *testing.T) {
	t.Run("default volume path", func(t *testing.T) {
		input := remoteDeploymentSpec("nginx", hostNetworking())
		input.Container().Runtime.DefaultVolume.ContainerPath = "data"
		if _, err := ValidateSpecWithResolvers(&input, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "defaultVolume.containerPath") {
			t.Fatalf("err = %v, want invalid default volume path", err)
		}
	})

	t.Run("custom mount permission", func(t *testing.T) {
		input := remoteDeploymentSpec("nginx", hostNetworking())
		input.Container().Runtime.Mounts = []apigen.HostMount{{HostPath: "/srv/data", ContainerPath: "/data"}}
		if _, err := ValidateSpecWithResolvers(&input, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "permission") {
			t.Fatalf("err = %v, want custom mount permission rejection", err)
		}
	})

	t.Run("cross-deployment mount permission", func(t *testing.T) {
		input := remoteDeploymentSpec("nginx", hostNetworking())
		input.Container().Runtime.CrossDeploymentMounts = []apigen.CrossDeploymentMount{{DeploymentID: 2, ContainerPath: "/data"}}
		if _, err := ValidateSpecWithResolvers(&input, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "permission") {
			t.Fatalf("err = %v, want cross-deployment mount permission rejection", err)
		}
	})

	t.Run("asset mount permission", func(t *testing.T) {
		input := remoteDeploymentSpec("nginx", hostNetworking())
		input.Container().Runtime.AssetMounts = []apigen.AssetMount{{Asset: apigen.AssetRef{AssetID: 3, Version: 1}, ContainerPath: "/etc/app.conf", Permission: apigen.FilePermission_FILE_PERMISSION_READ_WRITE}}
		assets := fakeAssetResolver{"app.conf": {Ref: apigen.ValueRef{ID: 3, Version: 1}, Key: "app.conf"}}
		if _, err := ValidateSpecWithResolvers(&input, assets, nil, nil); err == nil || !strings.Contains(err.Error(), "READ_ONLY or READ_EXECUTE") {
			t.Fatalf("err = %v, want asset mount permission rejection", err)
		}
	})
}

func TestValidateDeploymentSpecNormalizesContainerCommand(t *testing.T) {
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container().Runtime.OverrideCommand = []string{" /app/server ", "", " --listen ", " :8080 ", "   "}
	spec, err := ValidateSpecWithResolvers(&input, nil, nil, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithResolvers failed: %v", err)
	}
	want := []string{"/app/server", "--listen", ":8080"}
	if got := spec.Container().Runtime.OverrideCommand; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
}

func TestValidateDeploymentSpecAcceptsDevShmSizeKb(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container().Runtime.DevShmSizeKb = apigen.Some[uint32](65536)
	spec, err := ValidateSpecWithResolvers(&input, nil, nil, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithResolvers failed: %v", err)
	}
	if got := spec.Container().Runtime.DevShmSizeKb; !got.Present || got.Value != 65536 {
		t.Fatalf("devShmSizeKb = %v, want 65536", got)
	}
}

func TestValidateDeploymentSpecRejectsInvalidDevShmSizeKb(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container().Runtime.DevShmSizeKb = apigen.Some[uint32](0)
	_, err := ValidateSpecWithResolvers(&input, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "dev_shm_size_kb") {
		t.Fatalf("err = %v, want invalid devShmSizeKb", err)
	}
}

func TestValidateDeploymentSpecAcceptsFileDescriptorLimit(t *testing.T) {
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container().Runtime.FileDescriptorLimit = apigen.Some[uint32](4096)
	spec, err := ValidateSpecWithResolvers(&input, nil, nil, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithResolvers failed: %v", err)
	}
	if got := spec.Container().Runtime.FileDescriptorLimit; !got.Present || got.Value != 4096 {
		t.Fatalf("fileDescriptorLimit = %v, want 4096", got)
	}
}

func TestValidateDeploymentSpecRejectsInvalidFileDescriptorLimit(t *testing.T) {
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container().Runtime.FileDescriptorLimit = apigen.Some[uint32](0)
	_, err := ValidateSpecWithResolvers(&input, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "file_descriptor_limit") {
		t.Fatalf("err = %v, want invalid fileDescriptorLimit", err)
	}
}

func TestValidateDeploymentSpecRejectsInvalidHostMounts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		host      string
		container string
	}{
		{name: "relative host", host: "data", container: "/data"},
		{name: "relative container", host: "/srv/data", container: "data"},
		{name: "root host", host: "/", container: "/data"},
		{name: "root container", host: "/srv/data", container: "/"},
		{name: "unclean host", host: "/srv/../data", container: "/data"},
		{name: "unclean container", host: "/srv/data", container: "/var/../data"},
		{name: "opendeploy data", host: "/var/lib/opendeploy", container: "/data"},
		{name: "var ancestor", host: "/var", container: "/data"},
		{name: "var lib ancestor", host: "/var/lib", container: "/data"},
		{name: "trimmed ancestor", host: " /var/lib ", container: "/data"},
		{name: "ancestor with dot segments", host: "/srv/../var/lib", container: "/data"},
		{name: "ancestor with repeated separators", host: "/var//lib", container: "/data"},
		{name: "opendeploy tls", host: "/var/lib/opendeploy/tls", container: "/data"},
		{name: "opendeploy volumes root", host: "/var/lib/opendeploy-volumes", container: "/data"},
		{name: "opendeploy volume directory", host: "/var/lib/opendeploy-volumes/24", container: "/data"},
		{name: "opendeploy volume descendant", host: "/var/lib/opendeploy-volumes/24/default/keys", container: "/data"},
		{name: "opendeploy runtime socket", host: "/run/opendeploy/containerd.sock", container: "/data"},
		{name: "containerd state", host: "/var/lib/containerd", container: "/data"},
		{name: "system config", host: "/etc/opendeploy", container: "/data"},
		{name: "systemd unit dir", host: "/etc/systemd/system", container: "/data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := remoteDeploymentSpec("nginx:latest", hostNetworking())
			input.Container().Runtime.Mounts = []apigen.HostMount{{
				HostPath: tc.host, ContainerPath: tc.container, Permission: apigen.FilePermission_FILE_PERMISSION_READ_WRITE,
			}}
			_, err := ValidateSpecWithResolvers(&input, nil, nil, nil)
			if err == nil {
				t.Fatal("expected invalid host mount")
			}
		})
	}
}

func TestContainerHostMountDenylistPathBoundaries(t *testing.T) {
	for _, host := range []string{"/", "/var", "/var/lib", "/var/lib/opendeploy", "/var/lib/opendeploy/machine.key", "/var/lib/../lib", "/var//lib"} {
		if !containerHostMountDenied(host) {
			t.Errorf("protected path or ancestor %q was allowed", host)
		}
	}
	for _, host := range []string{"/srv/data", "/home/ubuntu/app", "/var/lib/my-app", "/var/log/my-app", "/var/lib/opendeploy-backups", "/etc-backup", "/runner", "/var/libexec"} {
		input := remoteDeploymentSpec("nginx", virtualNetworking())
		input.Container().Runtime.Mounts = []apigen.HostMount{{HostPath: host, ContainerPath: "/data", Permission: apigen.FilePermission_FILE_PERMISSION_READ_ONLY}}
		if _, err := ValidateSpecWithResolvers(&input, nil, nil, nil); err != nil {
			t.Errorf("unrelated path %q was denied: %v", host, err)
		}
	}
}

func TestValidateDeploymentSpecRequiresContainerWorkload(t *testing.T) {
	_, err := ValidateSpecWithResolvers(&apigen.DeploymentSpec{Networking: hostNetworking()}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "workload") {
		t.Fatalf("err = %v, want missing container workload rejection", err)
	}
}

func TestValidateDeploymentSpecAcceptsKnownEnvRefs(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container().Runtime.EnvVars = map[string]apigen.EnvVar{
		"PGUSER":     {Value: apigen.EnvVarValueOneof{Secret: &apigen.SecretEnv{Secret: apigen.SecretRef{SecretID: 6, Version: 2}}}},
		"PGDATABASE": {Value: apigen.EnvVarValueOneof{Config: &apigen.ConfigEnv{Config: apigen.ConfigRef{ConfigID: 18, Version: 1}}}},
	}
	_, err := ValidateSpecWithResolvers(&input, nil, fakeSecretResolver{{ID: 6, Version: 2}: "postgres"}, fakeConfigResolver{{ID: 18, Version: 1}: "postgres"})
	if err != nil {
		t.Fatalf("ValidateSpecWithResolvers failed: %v", err)
	}
}

func TestValidateDeploymentSpecRejectsUnspecifiedNetworkingMode(t *testing.T) {
	input := remoteDeploymentSpec("nginx", apigen.NetworkingConfig{})
	_, err := ValidateSpecWithResolvers(&input, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "networking.mode") {
		t.Fatalf("err = %v, want unspecified networking mode rejection", err)
	}

}

func TestValidateDeploymentSpecAcceptsExplicitHostNetworking(t *testing.T) {
	input := remoteDeploymentSpec("nginx", hostNetworking())
	spec, err := ValidateSpecWithResolvers(&input, nil, nil, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithResolvers failed: %v", err)
	}
	if spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_HOST {
		t.Fatalf("networking mode = %v, want host", spec.Networking.Mode)
	}
}

func TestValidateDeploymentSpecAcceptsHostNetworkingRollover(t *testing.T) {
	input := remoteDeploymentSpec("nginx", hostNetworking())
	input.Container().UpgradeStrategy = apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_ROLLOVER
	spec, err := ValidateSpecWithResolvers(&input, nil, nil, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithResolvers failed: %v", err)
	}
	if spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_HOST {
		t.Fatalf("networking mode = %v, want host", spec.Networking.Mode)
	}
}

func TestValidateDeploymentSpecAcceptsVirtualPortForwarding(t *testing.T) {
	input := remoteDeploymentSpec("nginx", apigen.NetworkingConfig{
		Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
		PortForwarding: []apigen.PortForward{
			{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080},
			{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UDP, HostPort: 18080, ContainerPort: 8080},
		},
	})
	spec, err := ValidateSpecWithResolvers(&input, nil, nil, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithResolvers failed: %v", err)
	}
	if got := len(spec.Networking.PortForwarding); got != 2 {
		t.Fatalf("portForwarding count = %d, want 2", got)
	}
}

func TestValidateDeploymentSpecNormalizesPortForwardIpFilter(t *testing.T) {
	input := remoteDeploymentSpec("nginx", apigen.NetworkingConfig{
		Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
		PortForwarding: []apigen.PortForward{{
			Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080,
			IpFilter: []apigen.IpFilter{
				{Mode: apigen.IpFilterMode_IP_FILTER_MODE_ALLOW, Prefix: mustPrefix("203.0.113.7")},
				{Mode: apigen.IpFilterMode_IP_FILTER_MODE_ALLOW, Prefix: mustPrefix("198.51.100.0/24")},
				{Mode: apigen.IpFilterMode_IP_FILTER_MODE_ALLOW, Prefix: mustPrefix("2001:DB8::/32")},
			},
		}},
	})
	spec, err := ValidateSpecWithResolvers(&input, nil, nil, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithResolvers failed: %v", err)
	}
	got := spec.Networking.PortForwarding[0].IpFilter
	want := []string{"203.0.113.7/32", "198.51.100.0/24", "2001:db8::/32"}
	if len(got) != len(want) {
		t.Fatalf("allow = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].Prefix.String() != want[i] {
			t.Fatalf("allow[%d] = %v, want %v", i, got[i].Prefix.String(), want[i])
		}
	}
}

func TestValidateDeploymentSpecAcceptsTlsPassthroughIngress(t *testing.T) {
	input := remoteDeploymentSpec("nginx", apigen.NetworkingConfig{
		Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
		Ingress: []apigen.Ingress{{
			Hostname: "db.example.com",
			Config:   tlsPassthrough(apigen.Maybe[uint32]{}, 5432),
		}},
	})
	spec, err := ValidateSpecWithResolvers(&input, nil, nil, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithResolvers failed: %v", err)
	}
	if got := len(spec.Networking.Ingress); got != 1 {
		t.Fatalf("ingress count = %d, want 1", got)
	}
}

func TestValidateDeploymentSpecRejectsInvalidNetworking(t *testing.T) {
	tests := []struct {
		name       string
		networking apigen.NetworkingConfig
		want       string
	}{
		{
			name: "host mode with port forwarding",
			networking: apigen.NetworkingConfig{
				Mode:           apigen.NetworkingMode_NETWORKING_MODE_HOST,
				PortForwarding: []apigen.PortForward{{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080}},
			},
			want: "requires virtual mode",
		},
		{
			name: "invalid protocol",
			networking: apigen.NetworkingConfig{
				Mode:           apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []apigen.PortForward{{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UNSPECIFIED, HostPort: 18080, ContainerPort: 8080}},
			},
			want: "protocol",
		},
		{
			name: "invalid host port",
			networking: apigen.NetworkingConfig{
				Mode:           apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []apigen.PortForward{{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 0, ContainerPort: 8080}},
			},
			want: "host_port",
		},
		{
			name: "invalid container port",
			networking: apigen.NetworkingConfig{
				Mode:           apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []apigen.PortForward{{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 0}},
			},
			want: "container_port",
		},
		{
			name: "duplicate same protocol host port",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []apigen.PortForward{
					{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080},
					{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8081},
				},
			},
			want: "duplicate TCP host port 18080",
		},
		{
			name: "host mode with ingress",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST,
				Ingress: []apigen.Ingress{{
					Hostname: "db.example.com",
					Config:   tlsPassthrough(apigen.Maybe[uint32]{}, 5432),
				}},
			},
			want: "requires virtual mode",
		},
		{
			name: "ip filter deny not supported",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []apigen.PortForward{{
					Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080,
					IpFilter: []apigen.IpFilter{{Mode: apigen.IpFilterMode_IP_FILTER_MODE_DENY, Prefix: mustPrefix("203.0.113.7")}},
				}},
			},
			want: "ipFilter.deny is not supported yet",
		},
		{
			name: "ip filter invalid allow entry",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []apigen.PortForward{{
					Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080,
					IpFilter: []apigen.IpFilter{{Mode: apigen.IpFilterMode_IP_FILTER_MODE_ALLOW}},
				}},
			},
			want: "prefix",
		},
		{
			name: "ip filter allow host bits set",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []apigen.PortForward{{
					Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080,
					IpFilter: []apigen.IpFilter{{Mode: apigen.IpFilterMode_IP_FILTER_MODE_ALLOW, Prefix: apigen.IpPrefix{Value: apigen.IpPrefixValueOneof{
						Ipv4: &apigen.IPv4Prefix{Address: apigen.IPv4Address{Octets: []byte{10, 0, 0, 1}}, PrefixLength: 8},
					}}}},
				}},
			},
			want: "host bits set",
		},
		{
			name: "ip filter duplicate allow entry",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []apigen.PortForward{{
					Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080,
					IpFilter: []apigen.IpFilter{
						{Mode: apigen.IpFilterMode_IP_FILTER_MODE_ALLOW, Prefix: mustPrefix("203.0.113.7/32")},
						{Mode: apigen.IpFilterMode_IP_FILTER_MODE_ALLOW, Prefix: mustPrefix("203.0.113.7")},
					},
				}},
			},
			want: "duplicate entry",
		},
		{
			name: "tls passthrough without config",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				Ingress: []apigen.Ingress{{
					Hostname: "db.example.com",
				}},
			},
			want: "config.value",
		},
		{
			name: "invalid ingress hostname",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				Ingress: []apigen.Ingress{{
					Hostname: "not a hostname",
					Config:   tlsPassthrough(apigen.Maybe[uint32]{}, 5432),
				}},
			},
			want: "hostname",
		},
		{
			name: "tls passthrough on netproxy DNS port",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				Ingress: []apigen.Ingress{{
					Hostname: "dns.example.com",
					Config:   tlsPassthrough(apigen.Some[uint32](53), 443),
				}},
			},
			want: "hostPort 53 is reserved for opendeploy-net DNS",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := remoteDeploymentSpec("nginx", tt.networking)
			_, err := ValidateSpecWithResolvers(&spec, nil, nil, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateDeploymentSpecRejectsNetproxyImage(t *testing.T) {
	input := remoteDeploymentSpec(internaldeploy.NetproxyImage, hostNetworking())
	_, err := ValidateSpecWithResolvers(&input, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "internal-only") {
		t.Fatalf("err = %v, want netproxy image internal-only rejection", err)
	}
}

func TestValidateDeploymentSpecRejectsUnknownSecretRef(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container().Runtime.EnvVars = map[string]apigen.EnvVar{"PGPASSWORD": {Value: apigen.EnvVarValueOneof{Secret: &apigen.SecretEnv{Secret: apigen.SecretRef{SecretID: 6, Version: 99}}}}}
	_, err := ValidateSpecWithResolvers(&input, nil, fakeSecretResolver{{ID: 6, Version: 2}: "postgres"}, fakeConfigResolver{})
	if err == nil || !strings.Contains(err.Error(), "unknown secret 6@99") {
		t.Fatalf("err = %v, want unknown secret", err)
	}
}

func TestValidateDeploymentSpecRejectsUnknownConfigRef(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container().Runtime.EnvVars = map[string]apigen.EnvVar{"PGDATABASE": {Value: apigen.EnvVarValueOneof{Config: &apigen.ConfigEnv{Config: apigen.ConfigRef{ConfigID: 99, Version: 1}}}}}
	_, err := ValidateSpecWithResolvers(&input, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err == nil || !strings.Contains(err.Error(), "unknown config 99@1") {
		t.Fatalf("err = %v, want unknown config", err)
	}
}

func TestValidateDeploymentSpecAcceptsLiteralEnvValues(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container().Runtime.EnvVars = map[string]apigen.EnvVar{
		"LITERAL": {Value: apigen.EnvVarValueOneof{Literal: &apigen.LiteralEnv{Value: "${s:not.real} and ${c:not.real}"}}},
	}
	_, err := ValidateSpecWithResolvers(&input, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err != nil {
		t.Fatalf("ValidateSpecWithResolvers failed: %v", err)
	}
}

func TestValidateDeploymentSpecRejectsIncompleteAddressRef(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container().Runtime.EnvVars = map[string]apigen.EnvVar{"UPSTREAM": {Value: apigen.EnvVarValueOneof{Address: &apigen.AddressEnv{SpaceID: 1}}}}
	_, err := ValidateSpecWithResolvers(&input, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "addressDeploymentId must be positive") {
		t.Fatalf("err = %v, want incomplete address rejection", err)
	}
	empty := remoteDeploymentSpec("postgres:16", hostNetworking())
	empty.Container().Runtime.EnvVars = map[string]apigen.EnvVar{"UPSTREAM": {}}
	_, err = ValidateSpecWithResolvers(&empty, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("err = %v, want empty env value rejection", err)
	}
}

func tlsPassthrough(hostPort apigen.Maybe[uint32], containerPort uint32) apigen.IngressConfig {
	return apigen.IngressConfig{Value: apigen.IngressConfigValueOneof{TlsPassthrough: &apigen.TlsPassthroughConfig{HostPort: hostPort, ContainerPort: containerPort}}}
}

func TestValidateIssuedTLSNamesLimitsInternalNamesToOwnSpace(t *testing.T) {
	spec := remoteDeploymentSpec("nginx", virtualNetworking())
	spec.Container().Runtime.IssuedTlsMount = apigen.Some(apigen.IssuedTLSMount{ContainerPath: "/tls", ExtraNames: []string{"example.com", "api.space-3.internal"}})
	if err := validateIssuedTLSNames(&spec, 7, 3); err != nil {
		t.Fatalf("own-space names: %v", err)
	}
	spec.Container().Runtime.IssuedTlsMount.Value.ExtraNames = []string{"example.com", "api.space-1.internal"}
	err := validateIssuedTLSNames(&spec, 7, 3)
	if err == nil || !strings.Contains(err.Error(), "extraNames[1]") || !strings.Contains(err.Error(), "space-3.internal") {
		t.Fatalf("err = %v, want rejection of api.space-1.internal", err)
	}
}
