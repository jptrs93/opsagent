package handler

import "testing"

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
