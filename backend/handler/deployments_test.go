package handler

import (
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
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
