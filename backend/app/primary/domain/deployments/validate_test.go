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

func remoteDeploymentSpec(image string, networking apigen.NetworkingConfig) apigen.DeploymentSpec {
	return apigen.DeploymentSpec{
		Container1Spec: &apigen.ContainerSpec{Source: apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: image}}},
		Networking:     networking,
	}
}

func TestValidateDeploymentSpecNixDockerBuild(t *testing.T) {
	spec, err := ValidateSpecWithAssets(&apigen.DeploymentSpec{
		Container1Spec: &apigen.ContainerSpec{
			Source: apigen.ContainerBundleSource{NixDockerBuild: &apigen.NixDockerBuild{
				Repo:   "github.com/acme/web",
				Flake:  "nix/web/flake.nix",
				Target: ".#webImage",
			}},
			Runtime: apigen.ContainerRuntime{User: "1000"},
		},
		Networking: hostNetworking(),
	}, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithAssets failed: %v", err)
	}
	if spec.Container1Spec.Source.NixDockerBuild == nil {
		t.Fatal("nixDockerBuild is nil")
	}
	if spec.Container1Spec.Source.NixDockerBuild.Repo != "github.com/acme/web" {
		t.Fatalf("repo = %q", spec.Container1Spec.Source.NixDockerBuild.Repo)
	}
	if spec.Container1Spec.Source.NixDockerBuild.Flake != "nix/web/flake.nix" {
		t.Fatalf("flake = %q", spec.Container1Spec.Source.NixDockerBuild.Flake)
	}
	if spec.Container1Spec.Source.NixDockerBuild.Target != ".#webImage" {
		t.Fatalf("target = %q", spec.Container1Spec.Source.NixDockerBuild.Target)
	}
	if spec.Container1Spec.Runtime.User != "1000" {
		t.Fatalf("container user = %q", spec.Container1Spec.Runtime.User)
	}
}

func TestValidateDeploymentSpecCanonicalizesSafeFlakePath(t *testing.T) {
	spec, err := ValidateSpecWithAssets(&apigen.DeploymentSpec{
		Container1Spec: &apigen.ContainerSpec{Source: apigen.ContainerBundleSource{
			NixDockerBuild: &apigen.NixDockerBuild{Repo: "github.com/acme/web", Flake: "./nix/../flake.nix"},
		}},
		Networking: hostNetworking(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.Container1Spec.Source.NixDockerBuild.Flake; got != "flake.nix" {
		t.Fatalf("flake = %q, want flake.nix", got)
	}

	for _, flake := range []string{"/flake.nix", "../flake.nix", "nix/default.nix"} {
		t.Run(flake, func(t *testing.T) {
			_, err := ValidateSpecWithAssets(&apigen.DeploymentSpec{
				Container1Spec: &apigen.ContainerSpec{Source: apigen.ContainerBundleSource{
					NixDockerBuild: &apigen.NixDockerBuild{Repo: "github.com/acme/web", Flake: flake},
				}},
				Networking: hostNetworking(),
			}, nil)
			if err == nil {
				t.Fatalf("flake path %q was accepted", flake)
			}
		})
	}
}

func nixCreateRequest(nodeID int32, name string, running bool) *apigen.DeploymentCreateRequest {
	return &apigen.DeploymentCreateRequest{
		SpaceID: 1, Name: name,
		NodeID: nodeID,
		Spec:   nixDeploymentSpecWithState("github.com/acme/app", "flake.nix", testNixCommit, running),
	}
}

func nixDeploymentSpec(repo, flake string) apigen.DeploymentSpec {
	return nixDeploymentSpecWithState(repo, flake, testNixCommit, true)
}

func nixDeploymentSpecWithState(repo, flake, version string, running bool) apigen.DeploymentSpec {
	return apigen.DeploymentSpec{
		Container1Spec: &apigen.ContainerSpec{
			Source:  apigen.ContainerBundleSource{NixDockerBuild: &apigen.NixDockerBuild{Repo: repo, Flake: flake}},
			Version: version,
			Running: running,
		},
		Networking: hostNetworking(),
	}
}

func TestValidateDeploymentSpecRejectsNonLocalNixTarget(t *testing.T) {
	_, err := ValidateSpecWithAssets(&apigen.DeploymentSpec{
		Container1Spec: &apigen.ContainerSpec{Source: apigen.ContainerBundleSource{
			NixDockerBuild: &apigen.NixDockerBuild{Repo: "github.com/acme/web", Flake: "flake.nix", Target: "github:acme/web#image"},
		}},
		Networking: hostNetworking(),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "local flake selector") {
		t.Fatalf("err = %v, want local target rejection", err)
	}
}

type fakeAssetResolver map[string]assets.AssetVersionRef

func (r fakeAssetResolver) GetAssetVersionRef(assetVersionID int32) (assets.AssetVersionRef, bool) {
	for _, asset := range r {
		if asset.VersionID == assetVersionID {
			return asset, true
		}
	}
	return assets.AssetVersionRef{}, false
}

type fakeSecretResolver map[int32]string

func (r fakeSecretResolver) MetaByID(id int32) (secrets.Meta, bool) {
	_, ok := r[id]
	return secrets.Meta{ID: id}, ok
}

type fakeConfigResolver map[int32]string

func (r fakeConfigResolver) ResolveConfig(id int32) (string, bool) {
	v, ok := r[id]
	return v, ok
}

func TestValidateDeploymentSpecResolvesAssetMounts(t *testing.T) {
	assets := fakeAssetResolver{
		"nginx.conf": {
			VersionID: 42,
			AssetID:   1,
			Key:       "nginx.conf",
		},
	}
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container1Spec.Runtime.AssetMounts = []*apigen.AssetMount{{
		AssetVersionID: 42, ContainerPath: "/etc/nginx/nginx.conf", Permission: apigen.FilePermission_READ_EXECUTE,
	}}
	spec, err := ValidateSpecWithAssets(&input, assets)
	if err != nil {
		t.Fatalf("ValidateSpecWithAssets failed: %v", err)
	}
	mounts := spec.Container1Spec.Runtime.AssetMounts
	if len(mounts) != 1 {
		t.Fatalf("asset mounts len = %d", len(mounts))
	}
	if mounts[0].AssetVersionID != 42 || mounts[0].ContainerPath != "/etc/nginx/nginx.conf" || mounts[0].Permission != apigen.FilePermission_READ_EXECUTE {
		t.Fatalf("asset mount not resolved: %+v", mounts[0])
	}
}

func TestValidateDeploymentSpecResolvesEnvAssetRefs(t *testing.T) {
	assets := fakeAssetResolver{
		"app.conf": {
			VersionID: 51,
			AssetID:   2,
			Key:       "app.conf",
		},
	}
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{"APP_CONFIG": {AssetVersionID: 51}}
	spec, err := ValidateSpecWithAssets(&input, assets)
	if err != nil {
		t.Fatalf("ValidateSpecWithAssets failed: %v", err)
	}
	value := spec.Container1Spec.Runtime.EnvVars["APP_CONFIG"]
	if value.Asset != "app.conf" || value.AssetVersionID != 51 {
		t.Fatalf("env asset ref not resolved: %+v", value)
	}
}

func TestValidateDeploymentSpecRejectsUnknownEnvAssetRef(t *testing.T) {
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{"APP_CONFIG": {AssetVersionID: 999}}
	_, err := ValidateSpecWithAssets(&input, fakeAssetResolver{})
	if err == nil || !strings.Contains(err.Error(), `asset version id 999 not found`) {
		t.Fatalf("err = %v, want unknown asset", err)
	}
}

func TestValidateDeploymentSpecAcceptsHostMounts(t *testing.T) {
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container1Spec.Runtime.Mounts = []*apigen.CustomHostMount{{
		HostPath: " /home/ubuntu/coflip-server/data ", ContainerPath: " /data ", Permission: apigen.FilePermission_READ_WRITE,
	}}
	spec, err := ValidateSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithAssets failed: %v", err)
	}
	mount := spec.Container1Spec.Runtime.Mounts[0]
	if mount.HostPath != "/home/ubuntu/coflip-server/data" || mount.ContainerPath != "/data" || mount.Permission != apigen.FilePermission_READ_WRITE {
		t.Fatalf("mount not normalized: %+v", mount)
	}
}

func TestValidateDeploymentSpecValidatesMounts(t *testing.T) {
	t.Run("default volume path", func(t *testing.T) {
		input := remoteDeploymentSpec("nginx", hostNetworking())
		input.Container1Spec.Runtime.DefaultVolume.ContainerPath = "data"
		if _, err := ValidateSpecWithAssets(&input, nil); err == nil || !strings.Contains(err.Error(), "defaultVolume.containerPath") {
			t.Fatalf("err = %v, want invalid default volume path", err)
		}
	})

	t.Run("custom mount permission", func(t *testing.T) {
		input := remoteDeploymentSpec("nginx", hostNetworking())
		input.Container1Spec.Runtime.Mounts = []*apigen.CustomHostMount{{HostPath: "/srv/data", ContainerPath: "/data"}}
		if _, err := ValidateSpecWithAssets(&input, nil); err == nil || !strings.Contains(err.Error(), "permission") {
			t.Fatalf("err = %v, want custom mount permission rejection", err)
		}
	})

	t.Run("cross-deployment mount permission", func(t *testing.T) {
		input := remoteDeploymentSpec("nginx", hostNetworking())
		input.Container1Spec.Runtime.CrossDeploymentMounts = []*apigen.CrossDeploymentMount{{DeploymentID: 2, ContainerPath: "/data"}}
		if _, err := ValidateSpecWithAssets(&input, nil); err == nil || !strings.Contains(err.Error(), "permission") {
			t.Fatalf("err = %v, want cross-deployment mount permission rejection", err)
		}
	})

	t.Run("asset mount permission", func(t *testing.T) {
		input := remoteDeploymentSpec("nginx", hostNetworking())
		input.Container1Spec.Runtime.AssetMounts = []*apigen.AssetMount{{AssetVersionID: 1, ContainerPath: "/etc/app.conf", Permission: apigen.FilePermission_READ_WRITE}}
		assets := fakeAssetResolver{"app.conf": {VersionID: 1, AssetID: 3, Key: "app.conf"}}
		if _, err := ValidateSpecWithAssets(&input, assets); err == nil || !strings.Contains(err.Error(), "READ_ONLY or READ_EXECUTE") {
			t.Fatalf("err = %v, want asset mount permission rejection", err)
		}
	})
}

func TestValidateDeploymentSpecNormalizesContainerCommand(t *testing.T) {
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container1Spec.Runtime.OverrideCommand = []string{" /app/server ", "", " --listen ", " :8080 ", "   "}
	spec, err := ValidateSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithAssets failed: %v", err)
	}
	want := []string{"/app/server", "--listen", ":8080"}
	if got := spec.Container1Spec.Runtime.OverrideCommand; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
}

func TestValidateDeploymentSpecAcceptsDevShmSizeKb(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container1Spec.Runtime.DevShmSizeKb = 65536
	spec, err := ValidateSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithAssets failed: %v", err)
	}
	if spec.Container1Spec.Runtime.DevShmSizeKb != 65536 {
		t.Fatalf("devShmSizeKb = %d, want 65536", spec.Container1Spec.Runtime.DevShmSizeKb)
	}
}

func TestValidateDeploymentSpecRejectsInvalidDevShmSizeKb(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container1Spec.Runtime.DevShmSizeKb = -1
	_, err := ValidateSpecWithAssets(&input, nil)
	if err == nil || !strings.Contains(err.Error(), "devShmSizeKb") {
		t.Fatalf("err = %v, want invalid devShmSizeKb", err)
	}
}

func TestValidateDeploymentSpecAcceptsFileDescriptorLimit(t *testing.T) {
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container1Spec.Runtime.FileDescriptorLimit = 4096
	spec, err := ValidateSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithAssets failed: %v", err)
	}
	if spec.Container1Spec.Runtime.FileDescriptorLimit != 4096 {
		t.Fatalf("fileDescriptorLimit = %d, want 4096", spec.Container1Spec.Runtime.FileDescriptorLimit)
	}
}

func TestValidateDeploymentSpecRejectsInvalidFileDescriptorLimit(t *testing.T) {
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container1Spec.Runtime.FileDescriptorLimit = -1
	_, err := ValidateSpecWithAssets(&input, nil)
	if err == nil || !strings.Contains(err.Error(), "fileDescriptorLimit") {
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
			input.Container1Spec.Runtime.Mounts = []*apigen.CustomHostMount{{
				HostPath: tc.host, ContainerPath: tc.container, Permission: apigen.FilePermission_READ_WRITE,
			}}
			_, err := ValidateSpecWithAssets(&input, nil)
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
		input.Container1Spec.Runtime.Mounts = []*apigen.CustomHostMount{{HostPath: host, ContainerPath: "/data", Permission: apigen.FilePermission_READ_ONLY}}
		if _, err := ValidateSpecWithAssets(&input, nil); err != nil {
			t.Errorf("unrelated path %q was denied: %v", host, err)
		}
	}
}

func TestValidateDeploymentSpecRejectsOpendeploySpec(t *testing.T) {
	_, err := ValidateSpecWithAssets(&apigen.DeploymentSpec{
		OpendeploySpec: &apigen.OpendeploySpec{},
		Networking:     hostNetworking(),
	}, nil)
	if err == nil {
		t.Fatal("expected ValidateSpecWithAssets to reject an opendeploy spec")
	}
}

func TestValidateDeploymentSpecAcceptsKnownEnvRefs(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{
		"PGUSER": {SecretVersionID: ptrInt32(6)}, "PGDATABASE": {ConfigVersionID: ptrInt32(18)},
	}
	_, err := ValidateSpecWithResolvers(&input, nil, fakeSecretResolver{6: "postgres"}, fakeConfigResolver{18: "postgres"})
	if err != nil {
		t.Fatalf("ValidateSpecWithResolvers failed: %v", err)
	}
}

func TestValidateDeploymentSpecDefaultsToVirtualNetworking(t *testing.T) {
	input := remoteDeploymentSpec("nginx", apigen.NetworkingConfig{})
	spec, err := ValidateSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithAssets failed: %v", err)
	}
	if spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
		t.Fatalf("networking mode = %v, want virtual", spec.Networking.Mode)
	}

	forwarded := remoteDeploymentSpec("nginx", apigen.NetworkingConfig{
		PortForwarding: []*apigen.PortForward{{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080}},
	})
	spec, err = ValidateSpecWithAssets(&forwarded, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithAssets failed: %v", err)
	}
	if spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL || len(spec.Networking.PortForwarding) != 1 {
		t.Fatalf("networking = %+v, want virtual mode with one port forward", spec.Networking)
	}
}

func TestValidateDeploymentSpecAcceptsExplicitHostNetworking(t *testing.T) {
	input := remoteDeploymentSpec("nginx", hostNetworking())
	spec, err := ValidateSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithAssets failed: %v", err)
	}
	if spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_HOST {
		t.Fatalf("networking mode = %v, want host", spec.Networking.Mode)
	}
}

func TestValidateDeploymentSpecAcceptsHostNetworkingRollover(t *testing.T) {
	input := remoteDeploymentSpec("nginx", hostNetworking())
	input.Container1Spec.UpgradeStrategy = apigen.ContainerUpgradeStrategy_ROLLOVER
	spec, err := ValidateSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithAssets failed: %v", err)
	}
	if spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_HOST {
		t.Fatalf("networking mode = %v, want host", spec.Networking.Mode)
	}
}

func TestValidateDeploymentSpecAcceptsVirtualPortForwarding(t *testing.T) {
	input := remoteDeploymentSpec("nginx", apigen.NetworkingConfig{
		Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
		PortForwarding: []*apigen.PortForward{
			{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080},
			{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UDP, HostPort: 18080, ContainerPort: 8080},
		},
	})
	spec, err := ValidateSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithAssets failed: %v", err)
	}
	if got := len(spec.Networking.PortForwarding); got != 2 {
		t.Fatalf("portForwarding count = %d, want 2", got)
	}
}

func TestValidateDeploymentSpecNormalizesPortForwardIpFilter(t *testing.T) {
	input := remoteDeploymentSpec("nginx", apigen.NetworkingConfig{
		Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
		PortForwarding: []*apigen.PortForward{{
			Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080,
			IpFilter: &apigen.IpFilter{Allow: []string{" 203.0.113.7 ", "198.51.100.0/24", "2001:DB8::/32"}},
		}},
	})
	spec, err := ValidateSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithAssets failed: %v", err)
	}
	got := spec.Networking.PortForwarding[0].IpFilter.Allow
	want := []string{"203.0.113.7", "198.51.100.0/24", "2001:db8::/32"}
	if len(got) != len(want) {
		t.Fatalf("allow = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("allow = %v, want %v", got, want)
		}
	}
}

func TestValidateDeploymentSpecAcceptsTlsPassthroughIngress(t *testing.T) {
	input := remoteDeploymentSpec("nginx", apigen.NetworkingConfig{
		Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
		Ingress: []*apigen.Ingress{{
			Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
			Hostname: "db.example.com",
			TlsPassthroughConfig: &apigen.TlsPassthroughConfig{
				ContainerPort: 5432,
			},
		}},
	})
	spec, err := ValidateSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("ValidateSpecWithAssets failed: %v", err)
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
				PortForwarding: []*apigen.PortForward{{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080}},
			},
			want: "requires virtual mode",
		},
		{
			name: "invalid protocol",
			networking: apigen.NetworkingConfig{
				Mode:           apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []*apigen.PortForward{{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UNSPECIFIED, HostPort: 18080, ContainerPort: 8080}},
			},
			want: "protocol",
		},
		{
			name: "invalid host port",
			networking: apigen.NetworkingConfig{
				Mode:           apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []*apigen.PortForward{{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 0, ContainerPort: 8080}},
			},
			want: "hostPort",
		},
		{
			name: "invalid container port",
			networking: apigen.NetworkingConfig{
				Mode:           apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []*apigen.PortForward{{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 0}},
			},
			want: "containerPort",
		},
		{
			name: "duplicate same protocol host port",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []*apigen.PortForward{
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
				Ingress: []*apigen.Ingress{{
					Kind:                 apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
					Hostname:             "db.example.com",
					TlsPassthroughConfig: &apigen.TlsPassthroughConfig{ContainerPort: 5432},
				}},
			},
			want: "requires virtual mode",
		},
		{
			name: "ip filter deny not supported",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []*apigen.PortForward{{
					Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080,
					IpFilter: &apigen.IpFilter{Deny: []string{"203.0.113.7"}},
				}},
			},
			want: "ipFilter.deny is not supported yet",
		},
		{
			name: "ip filter invalid allow entry",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []*apigen.PortForward{{
					Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080,
					IpFilter: &apigen.IpFilter{Allow: []string{"office"}},
				}},
			},
			want: "not a valid IP address or CIDR prefix",
		},
		{
			name: "ip filter allow host bits set",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []*apigen.PortForward{{
					Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080,
					IpFilter: &apigen.IpFilter{Allow: []string{"10.0.0.1/8"}},
				}},
			},
			want: "host bits set",
		},
		{
			name: "ip filter duplicate allow entry",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []*apigen.PortForward{{
					Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080,
					IpFilter: &apigen.IpFilter{Allow: []string{"203.0.113.7/32", "203.0.113.7"}},
				}},
			},
			want: "duplicate entry",
		},
		{
			name: "tls passthrough without config",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				Ingress: []*apigen.Ingress{{
					Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
					Hostname: "db.example.com",
				}},
			},
			want: "tlsPassthroughConfig",
		},
		{
			name: "invalid ingress hostname",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				Ingress: []*apigen.Ingress{{
					Kind:                 apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
					Hostname:             "not a hostname",
					TlsPassthroughConfig: &apigen.TlsPassthroughConfig{ContainerPort: 5432},
				}},
			},
			want: "hostname",
		},
		{
			name: "tls passthrough on netproxy DNS port",
			networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				Ingress: []*apigen.Ingress{{
					Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
					Hostname: "dns.example.com",
					TlsPassthroughConfig: &apigen.TlsPassthroughConfig{
						HostPort: 53, ContainerPort: 443,
					},
				}},
			},
			want: "hostPort 53 is reserved for opendeploy-net DNS",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := remoteDeploymentSpec("nginx", tt.networking)
			_, err := ValidateSpecWithAssets(&spec, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateDeploymentSpecRejectsNetproxyImage(t *testing.T) {
	input := remoteDeploymentSpec(internaldeploy.NetproxyImage, hostNetworking())
	_, err := ValidateSpecWithAssets(&input, nil)
	if err == nil || !strings.Contains(err.Error(), "internal-only") {
		t.Fatalf("err = %v, want netproxy image internal-only rejection", err)
	}
}

func TestValidateDeploymentSpecRejectsUnknownSecretRef(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{"PGPASSWORD": {SecretVersionID: ptrInt32(99)}}
	_, err := ValidateSpecWithResolvers(&input, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err == nil || !strings.Contains(err.Error(), "unknown secret id 99") {
		t.Fatalf("err = %v, want unknown secret", err)
	}
}

func TestValidateDeploymentSpecRejectsUnknownConfigRef(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{"PGDATABASE": {ConfigVersionID: ptrInt32(99)}}
	_, err := ValidateSpecWithResolvers(&input, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err == nil || !strings.Contains(err.Error(), "unknown config id 99") {
		t.Fatalf("err = %v, want unknown config", err)
	}
}

func TestValidateDeploymentSpecAcceptsLiteralEnvValues(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{
		"LITERAL": {Value: ptrString("${s:not.real} and ${c:not.real}")},
	}
	_, err := ValidateSpecWithResolvers(&input, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err != nil {
		t.Fatalf("ValidateSpecWithResolvers failed: %v", err)
	}
}

func TestValidateDeploymentSpecRejectsIncompleteAddressRef(t *testing.T) {
	deploymentID := int32(7)
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{"UPSTREAM": {AddressDeploymentID: &deploymentID}}
	_, err := ValidateSpecWithAssets(&input, nil)
	if err == nil || !strings.Contains(err.Error(), "required together") {
		t.Fatalf("err = %v, want incomplete address rejection", err)
	}
}

func ptrInt32(v int32) *int32    { return &v }
func ptrString(v string) *string { return &v }
