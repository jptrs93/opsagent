package handler

import (
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	secretspkg "github.com/jptrs93/opsagent/backend/secrets"
)

func TestValidateDeploymentSpecNixDockerBuild(t *testing.T) {
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			NixDockerBuild: apigen.NixDockerBuildConfig{
				Repo:  "github.com/acme/web",
				Flake: "nix/web/flake.nix",
			},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{User: "1000"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	if spec.Prepare.NixDockerBuild.Repo != "github.com/acme/web" {
		t.Fatalf("repo = %q", spec.Prepare.NixDockerBuild.Repo)
	}
	if spec.Prepare.NixDockerBuild.Flake != "nix/web/flake.nix" {
		t.Fatalf("flake = %q", spec.Prepare.NixDockerBuild.Flake)
	}
	if spec.Runner.Container.User != "1000" {
		t.Fatalf("container user = %q", spec.Runner.Container.User)
	}
}

type fakeAssetResolver map[string]*apigen.Asset

func (r fakeAssetResolver) GetAsset(key string, version int32) (*apigen.Asset, bool) {
	asset, ok := r[key]
	if !ok || (version > 0 && asset.Version != version) {
		return nil, false
	}
	return asset, true
}

type fakeSecretResolver map[string]string

func (r fakeSecretResolver) List() []secretspkg.Meta {
	out := make([]secretspkg.Meta, 0, len(r))
	for name := range r {
		out = append(out, secretspkg.Meta{Name: name})
	}
	return out
}

type fakeConfigResolver map[string]string

func (r fakeConfigResolver) ResolveConfig(name string) (string, bool) {
	v, ok := r[name]
	return v, ok
}

func TestValidateDeploymentSpecResolvesAssetMounts(t *testing.T) {
	assets := fakeAssetResolver{
		"nginx.conf": {
			ID:        42,
			Key:       "nginx.conf",
			CreatedAt: time.UnixMilli(1000),
			Version:   3,
			Format:    "nginx",
		},
	}
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: apigen.ContainerImageConfig{Image: "nginx:latest"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				AssetMounts: []*apigen.ContainerAssetMount{{
					Asset: "nginx.conf",
					Path:  "/etc/nginx/nginx.conf",
				}},
			},
		},
	}, assets)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	mounts := spec.Runner.Container.AssetMounts
	if len(mounts) != 1 {
		t.Fatalf("asset mounts len = %d", len(mounts))
	}
	if mounts[0].AssetID != 42 || mounts[0].Asset != "nginx.conf" || mounts[0].Version != 3 || mounts[0].Path != "/etc/nginx/nginx.conf" || mounts[0].Format != "nginx" {
		t.Fatalf("asset mount not resolved: %+v", mounts[0])
	}
}

func TestValidateDeploymentSpecAcceptsHostMounts(t *testing.T) {
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: apigen.ContainerImageConfig{Image: "nginx:latest"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				Mounts: []*apigen.ContainerMount{{
					Host:      " /home/ubuntu/coflip-server/data ",
					Container: " /data ",
					Readonly:  false,
				}},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	mount := spec.Runner.Container.Mounts[0]
	if mount.Host != "/home/ubuntu/coflip-server/data" || mount.Container != "/data" || mount.Readonly {
		t.Fatalf("mount not normalized: %+v", mount)
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
				Prepare: apigen.PrepareConfig{
					ContainerImage: apigen.ContainerImageConfig{Image: "nginx:latest"},
				},
				Runner: apigen.RunnerConfig{
					Container: apigen.ContainerRunnerConfig{
						Mounts: []*apigen.ContainerMount{{Host: tc.host, Container: tc.container}},
					},
				},
			}, nil)
			if err == nil {
				t.Fatal("expected invalid host mount")
			}
		})
	}
}

func TestValidateDeploymentSpecRejectsSystemdRunner(t *testing.T) {
	_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			NixDockerBuild: apigen.NixDockerBuildConfig{
				Repo:  "github.com/acme/web",
				Flake: "nix/web/flake.nix",
			},
		},
		Runner: apigen.RunnerConfig{
			Systemd: apigen.SystemdRunnerConfig{Name: "opendeploy", BinPath: "/var/lib/opendeploy/bin/opendeploy"},
		},
	}, nil)
	if err == nil {
		t.Fatal("expected validateDeploymentSpecWithAssets to reject systemd runner")
	}
}

func TestValidateDeploymentSpecAcceptsKnownEnvRefs(t *testing.T) {
	_, err := validateDeploymentSpecWithResolvers(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: apigen.ContainerImageConfig{Image: "postgres:16"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{Env: []*apigen.EnvVar{
				{Key: "PGUSER", Value: "${s:postgres.user}"},
				{Key: "PGDATABASE", Value: "${c:postgres.db}"},
			}},
		},
	}, nil, fakeSecretResolver{"postgres.user": "postgres"}, fakeConfigResolver{"postgres.db": "postgres"})
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithResolvers failed: %v", err)
	}
}

func TestValidateDeploymentSpecRejectsUnknownSecretRef(t *testing.T) {
	_, err := validateDeploymentSpecWithResolvers(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: apigen.ContainerImageConfig{Image: "postgres:16"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{Env: []*apigen.EnvVar{
				{Key: "PGPASSWORD", Value: "${s:missing.password}"},
			}},
		},
	}, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err == nil || !strings.Contains(err.Error(), "unknown secret ${s:missing.password}") {
		t.Fatalf("err = %v, want unknown secret", err)
	}
}

func TestValidateDeploymentSpecRejectsUnknownConfigRef(t *testing.T) {
	_, err := validateDeploymentSpecWithResolvers(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: apigen.ContainerImageConfig{Image: "postgres:16"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{Env: []*apigen.EnvVar{
				{Key: "PGDATABASE", Value: "${c:missing.db}"},
			}},
		},
	}, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err == nil || !strings.Contains(err.Error(), "unknown config ${c:missing.db}") {
		t.Fatalf("err = %v, want unknown config", err)
	}
}

func TestValidateDeploymentSpecIgnoresEscapedRefs(t *testing.T) {
	_, err := validateDeploymentSpecWithResolvers(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: apigen.ContainerImageConfig{Image: "postgres:16"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{Env: []*apigen.EnvVar{
				{Key: "LITERAL", Value: "$${s:not.real} and $${c:not.real}"},
			}},
		},
	}, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithResolvers failed: %v", err)
	}
}
