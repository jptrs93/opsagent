package handler

import (
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestParseCreateDeploymentYamlNixDockerBuild(t *testing.T) {
	_, spec, err := parseCreateDeploymentYaml(`
name: web
environment: PROD
machine: primary
prepare:
  nixDockerBuild:
    repo: github.com/acme/web
    flake: nix/web/flake.nix
runner:
  container:
    user: "1000"
`)
	if err != nil {
		t.Fatalf("parseCreateDeploymentYaml failed: %v", err)
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

func TestParseCreateDeploymentYamlResolvesAssetMounts(t *testing.T) {
	assets := fakeAssetResolver{
		"nginx.conf": {
			ID:        42,
			Key:       "nginx.conf",
			CreatedAt: time.UnixMilli(1000),
			Version:   3,
			Format:    "nginx",
		},
	}
	_, spec, err := parseCreateDeploymentYamlWithAssets(`
name: web
machine: primary
prepare:
  containerImage:
    image: nginx:latest
runner:
  container:
    assetMounts:
      - asset: nginx.conf
        path: /etc/nginx/nginx.conf
`, assets)
	if err != nil {
		t.Fatalf("parseCreateDeploymentYamlWithAssets failed: %v", err)
	}
	mounts := spec.Runner.Container.AssetMounts
	if len(mounts) != 1 {
		t.Fatalf("asset mounts len = %d", len(mounts))
	}
	if mounts[0].AssetID != 42 || mounts[0].Asset != "nginx.conf" || mounts[0].Version != 3 || mounts[0].Path != "/etc/nginx/nginx.conf" || mounts[0].Format != "nginx" {
		t.Fatalf("asset mount not resolved: %+v", mounts[0])
	}
}

func TestParseCreateDeploymentYamlRejectsNixDockerWithProcessRunner(t *testing.T) {
	_, _, err := parseCreateDeploymentYaml(`
name: web
machine: primary
prepare:
  nixDockerBuild:
    repo: github.com/acme/web
    flake: nix/web/flake.nix
runner:
  osProcess:
    runAs: ubuntu
`)
	if err == nil {
		t.Fatal("expected parseCreateDeploymentYaml to reject process runner with nixDockerBuild")
	}
}
