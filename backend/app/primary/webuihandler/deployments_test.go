package webuihandler

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/clusterhandler"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

const testNixCommit = "0123456789abcdef0123456789abcdef01234567"
const testNixCommit2 = "89abcdef0123456789abcdef0123456789abcdef"

func findSystemDeployment(t *testing.T, store *sqlite.PrimaryStorage, machine string) *apigen.DeploymentConfig {
	t.Helper()
	for _, cfg := range store.ListActiveDeploymentConfigs() {
		if sqlite.IsSystemDeploymentConfig(cfg) && cfg.ConfigID.Machine == machine {
			return cfg
		}
	}
	t.Fatalf("system deployment for %s not found", machine)
	return nil
}

func hostNetworking() apigen.NetworkingConfig {
	return apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST}
}

func virtualNetworking() apigen.NetworkingConfig {
	return apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL}
}

func TestValidateDeploymentSpecNixDockerBuild(t *testing.T) {
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			NixDockerBuild: &apigen.NixDockerBuildConfig{
				Repo:   "github.com/acme/web",
				Flake:  "nix/web/flake.nix",
				Target: ".#webImage",
			},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{User: "1000"},
		},
		Networking: hostNetworking(),
	}, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	if spec.Prepare.NixDockerBuild == nil {
		t.Fatal("nixDockerBuild is nil")
	}
	if spec.Prepare.NixDockerBuild.Repo != "github.com/acme/web" {
		t.Fatalf("repo = %q", spec.Prepare.NixDockerBuild.Repo)
	}
	if spec.Prepare.NixDockerBuild.Flake != "nix/web/flake.nix" {
		t.Fatalf("flake = %q", spec.Prepare.NixDockerBuild.Flake)
	}
	if spec.Prepare.NixDockerBuild.Target != ".#webImage" {
		t.Fatalf("target = %q", spec.Prepare.NixDockerBuild.Target)
	}
	if spec.Runner.Container.User != "1000" {
		t.Fatalf("container user = %q", spec.Runner.Container.User)
	}
}

func TestValidateDeploymentSpecCanonicalizesSafeFlakePath(t *testing.T) {
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare:    apigen.PrepareConfig{NixDockerBuild: &apigen.NixDockerBuildConfig{Repo: "github.com/acme/web", Flake: "./nix/../flake.nix"}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: hostNetworking(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.Prepare.NixDockerBuild.Flake; got != "flake.nix" {
		t.Fatalf("flake = %q, want flake.nix", got)
	}

	for _, flake := range []string{"/flake.nix", "../flake.nix", "nix/default.nix"} {
		t.Run(flake, func(t *testing.T) {
			_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
				Prepare:    apigen.PrepareConfig{NixDockerBuild: &apigen.NixDockerBuildConfig{Repo: "github.com/acme/web", Flake: flake}},
				Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
				Networking: hostNetworking(),
			}, nil)
			if err == nil {
				t.Fatalf("flake path %q was accepted", flake)
			}
		})
	}
}

func TestDeploymentCreateEnforcesRunningNixSource(t *testing.T) {
	t.Run("running verifies before persistence", func(t *testing.T) {
		store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
		node := store.EnsurePrimaryNode("primary", "primary")
		provider := &fakeGitSourceProvider{sourceCommitValid: true}
		h := &Handler{Store: store, GitVersions: provider}

		cfg, err := h.PostV1DeploymentCreate(apigen.Context{Ctx: context.Background()}, nixCreateRequest(node.ID, "web", true))
		if err != nil {
			t.Fatal(err)
		}
		if cfg == nil || len(provider.validateCalls) != 1 {
			t.Fatalf("config/provider calls = %v/%v", cfg, provider.validateCalls)
		}
	})

	t.Run("verification failure persists nothing", func(t *testing.T) {
		store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
		node := store.EnsurePrimaryNode("primary", "primary")
		provider := &fakeGitSourceProvider{sourceErr: errors.New("remote unavailable")}
		h := &Handler{Store: store, GitVersions: provider}

		_, err := h.PostV1DeploymentCreate(apigen.Context{Ctx: context.Background()}, nixCreateRequest(node.ID, "web", true))
		if err == nil {
			t.Fatal("expected source verification failure")
		}
		if got := len(store.ListActiveDeploymentConfigs()); got != 0 {
			t.Fatalf("persisted deployments = %d, want 0", got)
		}
	})

	t.Run("stopped skips provider", func(t *testing.T) {
		store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
		node := store.EnsurePrimaryNode("primary", "primary")
		provider := &fakeGitSourceProvider{sourceErr: errors.New("must not be called")}
		h := &Handler{Store: store, GitVersions: provider}

		cfg, err := h.PostV1DeploymentCreate(apigen.Context{Ctx: context.Background()}, nixCreateRequest(node.ID, "web", false))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.DesiredState.Running || len(provider.validateCalls) != 0 {
			t.Fatalf("config/provider calls = %+v/%v", cfg.DesiredState, provider.validateCalls)
		}
	})

	t.Run("stopped still requires immutable version syntax", func(t *testing.T) {
		store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
		node := store.EnsurePrimaryNode("primary", "primary")
		provider := &fakeGitSourceProvider{}
		h := &Handler{Store: store, GitVersions: provider}
		req := nixCreateRequest(node.ID, "web", false)
		req.DesiredState.Version = "main"

		if _, err := h.PostV1DeploymentCreate(apigen.Context{Ctx: context.Background()}, req); err == nil {
			t.Fatal("expected mutable version rejection")
		}
		if len(provider.validateCalls) != 0 || len(store.ListActiveDeploymentConfigs()) != 0 {
			t.Fatalf("provider calls/deployments = %v/%d", provider.validateCalls, len(store.ListActiveDeploymentConfigs()))
		}
	})

	t.Run("stopped permits an empty desired version", func(t *testing.T) {
		store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
		node := store.EnsurePrimaryNode("primary", "primary")
		provider := &fakeGitSourceProvider{}
		h := &Handler{Store: store, GitVersions: provider}
		req := nixCreateRequest(node.ID, "web", false)
		req.DesiredState.Version = ""

		cfg, err := h.PostV1DeploymentCreate(apigen.Context{Ctx: context.Background()}, req)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.DesiredState.Version != "" || cfg.DesiredState.Running {
			t.Fatalf("desired state = %+v, want stopped with no version", cfg.DesiredState)
		}
		if len(provider.validateCalls) != 0 || len(store.ListActiveDeploymentConfigs()) != 1 {
			t.Fatalf("provider calls/deployments = %v/%d", provider.validateCalls, len(store.ListActiveDeploymentConfigs()))
		}
	})
}

func TestDeploymentUpdateEnforcesEffectiveRunningNixTransitions(t *testing.T) {
	t.Run("starting stopped verifies and failure does not update", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, false)
		provider.sourceErr = errors.New("remote unavailable")
		req := &apigen.DeploymentUpdateRequest{DeploymentID: cfg.ID, Version: cfg.Version + 1, TargetVersion: testNixCommit}
		if _, err := h.PostV1DeploymentUpdate(apigen.Context{Ctx: context.Background()}, req); err == nil {
			t.Fatal("expected source verification failure")
		}
		unchanged := h.findConfigByID(cfg.ID)
		if unchanged.Version != cfg.Version || unchanged.DesiredState.Running {
			t.Fatalf("deployment changed after failed verification: %+v", unchanged)
		}
		provider.sourceErr = nil
		provider.sourceCommitValid = true
		if _, err := h.PostV1DeploymentUpdate(apigen.Context{Ctx: context.Background()}, req); err != nil {
			t.Fatal(err)
		}
		if len(provider.validateCalls) != 2 {
			t.Fatalf("source calls = %v", provider.validateCalls)
		}
	})

	t.Run("target version change verifies", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, true)
		provider.validateCalls = nil
		_, err := h.PostV1DeploymentUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.ID, Version: cfg.Version + 1, TargetVersion: testNixCommit2,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(provider.validateCalls) != 1 || provider.validateCalls[0].commit != testNixCommit2 {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
	})

	t.Run("Nix spec change while running verifies", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, true)
		provider.validateCalls = nil
		spec := nixDeploymentSpec("github.com/acme/other", "nix/app/flake.nix")
		_, err := h.PostV1DeploymentUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.ID, Version: cfg.Version + 1, Spec: spec,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(provider.validateCalls) != 1 || provider.validateCalls[0].repo != "github.com/acme/other" || provider.validateCalls[0].commit != testNixCommit {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
	})

	t.Run("stop and stopped edits skip provider", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, true)
		provider.validateCalls = nil
		spec := nixDeploymentSpec("github.com/acme/inaccessible", "flake.nix")
		if _, err := h.PostV1DeploymentUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.ID, Version: cfg.Version + 1, Stop: true, Spec: spec,
		}); err != nil {
			t.Fatal(err)
		}
		stopped := h.findConfigByID(cfg.ID)
		spec = nixDeploymentSpec("github.com/acme/still-inaccessible", "flake.nix")
		if _, err := h.PostV1DeploymentUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.ID, Version: stopped.Version + 1, Spec: spec,
		}); err != nil {
			t.Fatal(err)
		}
		if len(provider.validateCalls) != 0 {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
		if updated := h.findConfigByID(cfg.ID); updated.DesiredState.Version != "" || updated.DesiredState.Running {
			t.Fatalf("desired state after stopped source change = %+v, want empty stopped version", updated.DesiredState)
		}
	})

	t.Run("unrelated and source no-op updates skip provider", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, true)
		provider.validateCalls = nil
		sameSpace := cfg.ConfigID.SpaceID
		if _, err := h.PostV1DeploymentUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.ID, Version: cfg.Version + 1, SpaceID: &sameSpace,
		}); err != nil {
			t.Fatal(err)
		}
		updated := h.findConfigByID(cfg.ID)
		if _, err := h.PostV1DeploymentUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: updated.ID, Version: updated.Version + 1, TargetVersion: testNixCommit,
		}); err != nil {
			t.Fatal(err)
		}
		if len(provider.validateCalls) != 0 {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
	})

	t.Run("stopping while changing source kind clears incompatible version", func(t *testing.T) {
		store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
		node := store.EnsurePrimaryNode("primary", "primary")
		provider := &fakeGitSourceProvider{sourceErr: errors.New("must not be called")}
		h := &Handler{Store: store, GitVersions: provider}
		cfg, err := h.PostV1DeploymentCreate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentCreateRequest{
			ConfigID: apigen.DeploymentIdentifier{SpaceID: 1, Name: "web"},
			NodeID:   node.ID,
			Spec: apigen.DeploymentSpec{
				Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
				Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
				Networking: hostNetworking(),
			},
			DesiredState: apigen.DesiredState{Version: "latest", Running: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.PostV1DeploymentUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.ID,
			Version:      cfg.Version + 1,
			Stop:         true,
			Spec:         nixDeploymentSpec("github.com/acme/app", "flake.nix"),
		}); err != nil {
			t.Fatal(err)
		}
		updated := h.findConfigByID(cfg.ID)
		if updated.DesiredState.Version != "" || updated.DesiredState.Running {
			t.Fatalf("desired state = %+v, want stopped with empty version", updated.DesiredState)
		}
		if len(provider.validateCalls) != 0 {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
	})
}

func TestDeploymentVersionsUsesRemoteDefaultBranch(t *testing.T) {
	h, cfg, provider := newNixDeploymentHandler(t, false)
	provider.branches = []string{"main", "trunk"}
	provider.defaultBranch = "trunk"
	provider.defaultCommit = testNixCommit
	provider.commits = []*apigen.Version{{ID: testNixCommit}}

	versions, err := h.PostV1DeploymentVersions(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentVersionsRequest{
		DeploymentID: cfg.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if versions.NixDockerBuild.SelectedBranch != "trunk" {
		t.Fatalf("selected branch = %q, want trunk", versions.NixDockerBuild.SelectedBranch)
	}
	if provider.defaultCommitCalls != 1 || provider.listCommitsCalls != 1 {
		t.Fatalf("default/commit calls = %d/%d", provider.defaultCommitCalls, provider.listCommitsCalls)
	}
}

func TestDeploymentVersionsFallsBackWhenRemoteHeadIsUnavailable(t *testing.T) {
	h, cfg, provider := newNixDeploymentHandler(t, false)
	provider.branches = []string{"release", "main"}
	provider.defaultErr = errors.New("remote HEAD is unavailable")
	provider.commits = []*apigen.Version{{ID: testNixCommit}}

	versions, err := h.PostV1DeploymentVersions(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentVersionsRequest{
		DeploymentID: cfg.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if versions.NixDockerBuild.SelectedBranch != "main" {
		t.Fatalf("selected branch = %q, want main fallback", versions.NixDockerBuild.SelectedBranch)
	}
}

func nixCreateRequest(nodeID int32, name string, running bool) *apigen.DeploymentCreateRequest {
	return &apigen.DeploymentCreateRequest{
		ConfigID:     apigen.DeploymentIdentifier{SpaceID: 1, Name: name},
		NodeID:       nodeID,
		Spec:         nixDeploymentSpec("github.com/acme/app", "flake.nix"),
		DesiredState: apigen.DesiredState{Version: testNixCommit, Running: running},
	}
}

func nixDeploymentSpec(repo, flake string) apigen.DeploymentSpec {
	return apigen.DeploymentSpec{
		Prepare:    apigen.PrepareConfig{NixDockerBuild: &apigen.NixDockerBuildConfig{Repo: repo, Flake: flake}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: hostNetworking(),
	}
}

func newNixDeploymentHandler(t *testing.T, running bool) (*Handler, *apigen.DeploymentConfig, *fakeGitSourceProvider) {
	t.Helper()
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	node := store.EnsurePrimaryNode("primary", "primary")
	provider := &fakeGitSourceProvider{sourceCommitValid: true}
	h := &Handler{Store: store, GitVersions: provider}
	cfg, err := h.PostV1DeploymentCreate(apigen.Context{Ctx: context.Background()}, nixCreateRequest(node.ID, "web", running))
	if err != nil {
		t.Fatal(err)
	}
	return h, cfg, provider
}

func TestValidateDeploymentSpecRejectsNonLocalNixTarget(t *testing.T) {
	_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{NixDockerBuild: &apigen.NixDockerBuildConfig{
			Repo: "github.com/acme/web", Flake: "flake.nix", Target: "github:acme/web#image",
		}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: hostNetworking(),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "local flake selector") {
		t.Fatalf("err = %v, want local target rejection", err)
	}
}

type fakeAssetResolver map[string]*apigen.Asset

func (r fakeAssetResolver) GetAssetByID(assetID int32) (*apigen.Asset, bool) {
	for _, asset := range r {
		if asset != nil && asset.ID == assetID {
			return asset, true
		}
	}
	return nil, false
}

type fakeSecretResolver map[int32]string

func (r fakeSecretResolver) List() []secrets.Meta {
	out := make([]secrets.Meta, 0, len(r))
	for id := range r {
		out = append(out, secrets.Meta{ID: id})
	}
	return out
}

type fakeConfigResolver map[int32]string

func (r fakeConfigResolver) ResolveConfig(id int32) (string, bool) {
	v, ok := r[id]
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
			ContainerImage: &apigen.ContainerImageConfig{Image: "nginx:latest"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				AssetMounts: []*apigen.ContainerAssetMount{{
					AssetID:    42,
					Path:       "/etc/nginx/nginx.conf",
					Executable: true,
				}},
			},
		},
		Networking: hostNetworking(),
	}, assets)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	mounts := spec.Runner.Container.AssetMounts
	if len(mounts) != 1 {
		t.Fatalf("asset mounts len = %d", len(mounts))
	}
	if mounts[0].AssetID != 42 || mounts[0].Asset != "nginx.conf" || mounts[0].Path != "/etc/nginx/nginx.conf" || mounts[0].Format != "nginx" || !mounts[0].Executable {
		t.Fatalf("asset mount not resolved: %+v", mounts[0])
	}
}

func TestValidateDeploymentSpecResolvesEnvAssetRefs(t *testing.T) {
	assets := fakeAssetResolver{
		"app.conf": {
			ID:      51,
			Key:     "app.conf",
			Version: 7,
		},
	}
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "nginx:latest"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				EnvVars: map[string]*apigen.EnvVarValue{
					"APP_CONFIG": {AssetID: 51},
				},
			},
		},
		Networking: hostNetworking(),
	}, assets)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	value := spec.Runner.Container.EnvVars["APP_CONFIG"]
	if value.Asset != "app.conf" || value.AssetID != 51 {
		t.Fatalf("env asset ref not resolved: %+v", value)
	}
}

func TestValidateDeploymentSpecRejectsUnknownEnvAssetRef(t *testing.T) {
	_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "nginx:latest"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				EnvVars: map[string]*apigen.EnvVarValue{
					"APP_CONFIG": {AssetID: 999},
				},
			},
		},
		Networking: hostNetworking(),
	}, fakeAssetResolver{})
	if err == nil || !strings.Contains(err.Error(), `asset id 999 not found`) {
		t.Fatalf("err = %v, want unknown asset", err)
	}
}

func TestValidateDeploymentSpecAcceptsHostMounts(t *testing.T) {
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "nginx:latest"},
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
		Networking: hostNetworking(),
	}, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	mount := spec.Runner.Container.Mounts[0]
	if mount.Host != "/home/ubuntu/coflip-server/data" || mount.Container != "/data" || mount.Readonly {
		t.Fatalf("mount not normalized: %+v", mount)
	}
}

func TestValidateDeploymentSpecNormalizesContainerCommand(t *testing.T) {
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "nginx:latest"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				Command: []string{" /app/server ", "", " --listen ", " :8080 ", "   "},
			},
		},
		Networking: hostNetworking(),
	}, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	want := []string{"/app/server", "--listen", ":8080"}
	if got := spec.Runner.Container.Command; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
}

func TestValidateDeploymentSpecAcceptsDevShmSizeKb(t *testing.T) {
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "postgres:16"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				DevShmSizeKb: 65536,
			},
		},
		Networking: hostNetworking(),
	}, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	if spec.Runner.Container.DevShmSizeKb != 65536 {
		t.Fatalf("devShmSizeKb = %d, want 65536", spec.Runner.Container.DevShmSizeKb)
	}
}

func TestValidateDeploymentSpecRejectsInvalidDevShmSizeKb(t *testing.T) {
	_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "postgres:16"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				DevShmSizeKb: -1,
			},
		},
		Networking: hostNetworking(),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "devShmSizeKb") {
		t.Fatalf("err = %v, want invalid devShmSizeKb", err)
	}
}

func TestValidateDeploymentSpecAcceptsFileDescriptorLimit(t *testing.T) {
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "nginx:latest"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				FileDescriptorLimit: 4096,
			},
		},
		Networking: hostNetworking(),
	}, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	if spec.Runner.Container.FileDescriptorLimit != 4096 {
		t.Fatalf("fileDescriptorLimit = %d, want 4096", spec.Runner.Container.FileDescriptorLimit)
	}
}

func TestValidateDeploymentSpecRejectsInvalidFileDescriptorLimit(t *testing.T) {
	_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "nginx:latest"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				FileDescriptorLimit: -1,
			},
		},
		Networking: hostNetworking(),
	}, nil)
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
			_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
				Prepare: apigen.PrepareConfig{
					ContainerImage: &apigen.ContainerImageConfig{Image: "nginx:latest"},
				},
				Runner: apigen.RunnerConfig{
					Container: apigen.ContainerRunnerConfig{
						Mounts: []*apigen.ContainerMount{{Host: tc.host, Container: tc.container}},
					},
				},
				Networking: hostNetworking(),
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
			NixDockerBuild: &apigen.NixDockerBuildConfig{
				Repo:  "github.com/acme/web",
				Flake: "nix/web/flake.nix",
			},
		},
		Runner: apigen.RunnerConfig{
			Systemd: apigen.SystemdRunnerConfig{Name: "opendeploy", BinPath: "/var/lib/opendeploy/bin/opendeploy"},
		},
		Networking: hostNetworking(),
	}, nil)
	if err == nil {
		t.Fatal("expected validateDeploymentSpecWithAssets to reject systemd runner")
	}
}

func TestDeploymentUpdateRejectsSystemDeploymentSpecUpdate(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	store.EnsureSystemDeployment("primary", "v0.0.194")
	var system *apigen.DeploymentConfig
	for _, cfg := range store.ListActiveDeploymentConfigs() {
		if sqlite.IsSystemDeploymentConfig(cfg) {
			system = cfg
			break
		}
	}
	if system == nil {
		t.Fatal("system deployment not found")
	}

	h := &Handler{Store: store}
	_, err := h.PostV1DeploymentUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: system.ID,
		Version:      system.Version + 1,
		Spec: apigen.DeploymentSpec{
			Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
			Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{UpgradeStrategy: apigen.ContainerUpgradeStrategy_RECREATE}},
			Networking: hostNetworking(),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "internal-only") {
		t.Fatalf("err = %v, want internal-only rejection", err)
	}
}

func TestValidateDeploymentSpecAcceptsKnownEnvRefs(t *testing.T) {
	_, err := validateDeploymentSpecWithResolvers(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "postgres:16"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{EnvVars: map[string]*apigen.EnvVarValue{
				"PGUSER":     {SecretID: ptrInt32(6)},
				"PGDATABASE": {ConfigID: ptrInt32(18)},
			}},
		},
		Networking: hostNetworking(),
	}, nil, fakeSecretResolver{6: "postgres"}, fakeConfigResolver{18: "postgres"})
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithResolvers failed: %v", err)
	}
}

func TestValidateDeploymentSpecRejectsMissingNetworking(t *testing.T) {
	_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "networking is required") {
		t.Fatalf("err = %v, want networking required", err)
	}
}

func TestValidateDeploymentSpecAcceptsExplicitHostNetworking(t *testing.T) {
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: hostNetworking(),
	}, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	if spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_HOST {
		t.Fatalf("networking mode = %v, want host", spec.Networking.Mode)
	}
}

func TestValidateDeploymentSpecAcceptsHostNetworkingRollover(t *testing.T) {
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner: apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{
			UpgradeStrategy: apigen.ContainerUpgradeStrategy_ROLLOVER,
		}},
		Networking: hostNetworking(),
	}, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	if spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_HOST {
		t.Fatalf("networking mode = %v, want host", spec.Networking.Mode)
	}
}

func TestValidateDeploymentSpecAcceptsVirtualPortForwarding(t *testing.T) {
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
			PortForwarding: []*apigen.PortForward{
				{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080},
				{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UDP, HostPort: 18080, ContainerPort: 8080},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	if got := len(spec.Networking.PortForwarding); got != 2 {
		t.Fatalf("portForwarding count = %d, want 2", got)
	}
}

func TestValidateDeploymentSpecAcceptsTlsPassthroughIngress(t *testing.T) {
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
			Ingress: []*apigen.Ingress{{
				Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
				Hostname: "db.example.com",
				TlsPassthroughConfig: &apigen.TlsPassthroughConfig{
					ContainerPort: 5432,
				},
			}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	if got := len(spec.Networking.Ingress); got != 1 {
		t.Fatalf("ingress count = %d, want 1", got)
	}
}

func TestValidateDeploymentSpecRejectsInvalidNetworking(t *testing.T) {
	tests := []struct {
		name string
		spec apigen.DeploymentSpec
		want string
	}{
		{
			name: "unspecified mode with port forwarding",
			spec: apigen.DeploymentSpec{
				Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
				Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
				Networking: apigen.NetworkingConfig{PortForwarding: []*apigen.PortForward{{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080}}},
			},
			want: "networking.mode is required",
		},
		{
			name: "host mode with port forwarding",
			spec: apigen.DeploymentSpec{
				Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
				Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
				Networking: apigen.NetworkingConfig{
					Mode:           apigen.NetworkingMode_NETWORKING_MODE_HOST,
					PortForwarding: []*apigen.PortForward{{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080}},
				},
			},
			want: "requires virtual mode",
		},
		{
			name: "invalid protocol",
			spec: apigen.DeploymentSpec{
				Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
				Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
				Networking: apigen.NetworkingConfig{
					Mode:           apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
					PortForwarding: []*apigen.PortForward{{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UNSPECIFIED, HostPort: 18080, ContainerPort: 8080}},
				},
			},
			want: "protocol",
		},
		{
			name: "invalid host port",
			spec: apigen.DeploymentSpec{
				Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
				Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
				Networking: apigen.NetworkingConfig{
					Mode:           apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
					PortForwarding: []*apigen.PortForward{{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 0, ContainerPort: 8080}},
				},
			},
			want: "hostPort",
		},
		{
			name: "invalid container port",
			spec: apigen.DeploymentSpec{
				Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
				Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
				Networking: apigen.NetworkingConfig{
					Mode:           apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
					PortForwarding: []*apigen.PortForward{{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 0}},
				},
			},
			want: "containerPort",
		},
		{
			name: "duplicate same protocol host port",
			spec: apigen.DeploymentSpec{
				Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
				Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
				Networking: apigen.NetworkingConfig{
					Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
					PortForwarding: []*apigen.PortForward{
						{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080},
						{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8081},
					},
				},
			},
			want: "duplicate TCP host port 18080",
		},
		{
			name: "host mode with ingress",
			spec: apigen.DeploymentSpec{
				Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
				Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
				Networking: apigen.NetworkingConfig{
					Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST,
					Ingress: []*apigen.Ingress{{
						Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
						Hostname: "db.example.com",
						TlsPassthroughConfig: &apigen.TlsPassthroughConfig{
							ContainerPort: 5432,
						},
					}},
				},
			},
			want: "requires virtual mode",
		},
		{
			name: "tls passthrough without config",
			spec: apigen.DeploymentSpec{
				Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
				Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
				Networking: apigen.NetworkingConfig{
					Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
					Ingress: []*apigen.Ingress{{
						Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
						Hostname: "db.example.com",
					}},
				},
			},
			want: "tlsPassthroughConfig",
		},
		{
			name: "invalid ingress hostname",
			spec: apigen.DeploymentSpec{
				Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
				Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
				Networking: apigen.NetworkingConfig{
					Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
					Ingress: []*apigen.Ingress{{
						Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
						Hostname: "not a hostname",
						TlsPassthroughConfig: &apigen.TlsPassthroughConfig{
							ContainerPort: 5432,
						},
					}},
				},
			},
			want: "hostname",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateDeploymentSpecWithAssets(&tt.spec, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateDeploymentSpecRejectsNetproxyImage(t *testing.T) {
	_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: internaldeploy.NetproxyImage}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: hostNetworking(),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "internal-only") {
		t.Fatalf("err = %v, want netproxy image internal-only rejection", err)
	}
}

func TestValidateDeploymentSpecRejectsUnknownSecretRef(t *testing.T) {
	_, err := validateDeploymentSpecWithResolvers(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "postgres:16"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{EnvVars: map[string]*apigen.EnvVarValue{
				"PGPASSWORD": {SecretID: ptrInt32(99)},
			}},
		},
		Networking: hostNetworking(),
	}, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err == nil || !strings.Contains(err.Error(), "unknown secret id 99") {
		t.Fatalf("err = %v, want unknown secret", err)
	}
}

func TestValidateDeploymentSpecRejectsUnknownConfigRef(t *testing.T) {
	_, err := validateDeploymentSpecWithResolvers(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "postgres:16"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{EnvVars: map[string]*apigen.EnvVarValue{
				"PGDATABASE": {ConfigID: ptrInt32(99)},
			}},
		},
		Networking: hostNetworking(),
	}, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err == nil || !strings.Contains(err.Error(), "unknown config id 99") {
		t.Fatalf("err = %v, want unknown config", err)
	}
}

func TestValidateDeploymentSpecAcceptsLiteralEnvValues(t *testing.T) {
	_, err := validateDeploymentSpecWithResolvers(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: "postgres:16"},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{EnvVars: map[string]*apigen.EnvVarValue{
				"LITERAL": {Value: ptrString("${s:not.real} and ${c:not.real}")},
			}},
		},
		Networking: hostNetworking(),
	}, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithResolvers failed: %v", err)
	}
}

func TestValidateDeploymentSpecRejectsIncompleteAddressRef(t *testing.T) {
	deploymentID := int32(7)
	_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "postgres:16"}},
		Runner: apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{EnvVars: map[string]*apigen.EnvVarValue{
			"UPSTREAM": {AddressDeploymentID: &deploymentID},
		}}},
		Networking: hostNetworking(),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "required together") {
		t.Fatalf("err = %v, want incomplete address rejection", err)
	}
}

func TestDeploymentAddressEnvRefsValidateAndBlockTargetChanges(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	worker := store.EnsurePrimaryNode("worker", "worker")
	secretsManager, err := secrets.Initialize(t.TempDir(), store)
	if err != nil {
		t.Fatalf("secrets.Initialize: %v", err)
	}
	h := &Handler{Store: store, Secrets: secretsManager}

	create := func(name string, nodeID int32, networking apigen.NetworkingConfig, env map[string]*apigen.EnvVarValue) *apigen.DeploymentConfig {
		t.Helper()
		cfg, err := h.PostV1DeploymentCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			ConfigID: apigen.DeploymentIdentifier{SpaceID: 1, Name: name},
			NodeID:   nodeID,
			Spec: apigen.DeploymentSpec{
				Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
				Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{EnvVars: env}},
				Networking: networking,
			},
		})
		if err != nil {
			t.Fatalf("PostV1DeploymentCreate %s: %v", name, err)
		}
		return cfg
	}

	target := create("database", primary.ID, virtualNetworking(), nil)
	addressDeploymentID := target.ID
	addressSpaceID := int32(1)
	consumer := create("web", primary.ID, hostNetworking(), map[string]*apigen.EnvVarValue{
		"DATABASE_ADDR": {AddressDeploymentID: &addressDeploymentID, AddressSpaceID: &addressSpaceID},
	})
	if got := consumer.Spec.Runner.Container.EnvVars["DATABASE_ADDR"]; got.AddressDeploymentID == nil || got.AddressSpaceID == nil {
		t.Fatalf("address ref was not stored: %+v", got)
	}

	wrongSpace := int32(2)
	_, err = h.PostV1DeploymentCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		ConfigID: apigen.DeploymentIdentifier{SpaceID: 1, Name: "wrong-space"},
		NodeID:   primary.ID,
		Spec: apigen.DeploymentSpec{
			Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
			Runner: apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{EnvVars: map[string]*apigen.EnvVarValue{
				"DATABASE_ADDR": {AddressDeploymentID: &addressDeploymentID, AddressSpaceID: &wrongSpace},
			}}},
			Networking: hostNetworking(),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "address space does not match") {
		t.Fatalf("err = %v, want address-space rejection", err)
	}

	remote := create("remote", worker.ID, virtualNetworking(), nil)
	remoteID := remote.ID
	_, err = h.PostV1DeploymentCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		ConfigID: apigen.DeploymentIdentifier{SpaceID: 1, Name: "cross-node"},
		NodeID:   primary.ID,
		Spec: apigen.DeploymentSpec{
			Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
			Runner: apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{EnvVars: map[string]*apigen.EnvVarValue{
				"REMOTE_ADDR": {AddressDeploymentID: &remoteID, AddressSpaceID: &addressSpaceID},
			}}},
			Networking: hostNetworking(),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "same node") {
		t.Fatalf("err = %v, want cross-node rejection", err)
	}

	nextSpaceID := int32(2)
	_, err = h.PostV1DeploymentUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: target.ID,
		Version:      target.Version + 1,
		SpaceID:      &nextSpaceID,
	})
	if err == nil || !strings.Contains(err.Error(), "address references exist") {
		t.Fatalf("err = %v, want address-dependent space move rejection", err)
	}

	_, err = h.PostV1DeploymentUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: target.ID,
		Version:      target.Version + 1,
		Spec: apigen.DeploymentSpec{
			Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
			Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
			Networking: hostNetworking(),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot leave virtual mode") {
		t.Fatalf("err = %v, want virtual-networking removal rejection", err)
	}

	store.MustWriteDeploymentStatus(target.ID, func(status *apigen.DeploymentStatus) bool {
		status.Runner.Status = apigen.RunningStatus_STOPPED
		return true
	})
	err = h.PostV1DeploymentDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: target.ID, Version: target.Version + 1})
	if err == nil || !strings.Contains(err.Error(), "reference_in_use") {
		t.Fatalf("err = %v, want referenced deployment deletion rejection", err)
	}
}

func TestDeploymentCreatePersistsInitialStoppedDesiredState(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	h := &Handler{Store: store}

	cfg, err := h.PostV1DeploymentCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		ConfigID: apigen.DeploymentIdentifier{SpaceID: 1, Name: "web"},
		NodeID:   primary.ID,
		Spec: apigen.DeploymentSpec{
			Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
			Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
			Networking: hostNetworking(),
		},
		DesiredState: apigen.DesiredState{Version: "1.25", Running: false},
	})
	if err != nil {
		t.Fatalf("PostV1DeploymentCreate failed: %v", err)
	}
	if cfg.Version != 1 {
		t.Fatalf("version = %d, want initial config version 1", cfg.Version)
	}
	if cfg.DesiredState.Version != "1.25" || cfg.DesiredState.Running {
		t.Fatalf("desired state = %+v, want stopped 1.25", cfg.DesiredState)
	}
	if history := store.MustFetchDeploymentHistory(cfg.ID); len(history) != 1 {
		t.Fatalf("history len = %d, want create only", len(history))
	} else if history[0].DesiredState.Version != "1.25" || history[0].DesiredState.Running {
		t.Fatalf("history desired state = %+v, want stopped 1.25", history[0].DesiredState)
	}
}

func TestDeploymentCreateRejectsIngressClaimsAlreadyUsedOnNode(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	h := &Handler{Store: store, NodeID: primary.ID}
	ingress := func(hostname string) apigen.NetworkingConfig {
		return apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
			Ingress: []*apigen.Ingress{{
				Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
				Hostname: hostname,
				TlsPassthroughConfig: &apigen.TlsPassthroughConfig{
					HostPort:      8443,
					ContainerPort: 5432,
				},
			}},
		}
	}
	_, err := h.PostV1DeploymentCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		ConfigID: apigen.DeploymentIdentifier{SpaceID: 1, Name: "database"},
		NodeID:   primary.ID,
		Spec: apigen.DeploymentSpec{
			Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "postgres"}},
			Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
			Networking: ingress("db.example.com"),
		},
	})
	if err != nil {
		t.Fatalf("creating first ingress deployment: %v", err)
	}
	_, err = h.PostV1DeploymentCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		ConfigID: apigen.DeploymentIdentifier{SpaceID: 1, Name: "database-copy"},
		NodeID:   primary.ID,
		Spec: apigen.DeploymentSpec{
			Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "postgres"}},
			Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
			Networking: ingress("DB.EXAMPLE.COM"),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("err = %v, want duplicate ingress claim rejection", err)
	}
	_, err = h.PostV1DeploymentCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		ConfigID: apigen.DeploymentIdentifier{SpaceID: 1, Name: "direct"},
		NodeID:   primary.ID,
		Spec: apigen.DeploymentSpec{
			Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
			Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
			Networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				PortForwarding: []*apigen.PortForward{{
					Protocol:      apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP,
					HostPort:      8443,
					ContainerPort: 443,
				}},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts with ingress") {
		t.Fatalf("err = %v, want raw port conflict rejection", err)
	}
}

func TestDeploymentCreateRejectsPrimaryIngressOnPort443(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	worker := store.EnsurePrimaryNode("worker", "worker")
	h := &Handler{Store: store, NodeID: primary.ID}
	spec := func() apigen.DeploymentSpec {
		return apigen.DeploymentSpec{
			Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "postgres"}},
			Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
			Networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				Ingress: []*apigen.Ingress{{
					Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
					Hostname: "db.example.com",
					TlsPassthroughConfig: &apigen.TlsPassthroughConfig{
						ContainerPort: 5432,
					},
				}},
			},
		}
	}
	_, err := h.PostV1DeploymentCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		ConfigID: apigen.DeploymentIdentifier{SpaceID: 1, Name: "primary-database"},
		NodeID:   primary.ID,
		Spec:     spec(),
	})
	if err == nil || !strings.Contains(err.Error(), "reserved for the primary Web UI") {
		t.Fatalf("err = %v, want primary :443 reservation", err)
	}
	_, err = h.PostV1DeploymentCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		ConfigID: apigen.DeploymentIdentifier{SpaceID: 1, Name: "worker-database"},
		NodeID:   worker.ID,
		Spec:     spec(),
	})
	if err != nil {
		t.Fatalf("worker :443 ingress was rejected: %v", err)
	}
}

func TestDeploymentCreateRejectsInternalIdentity(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	h := &Handler{Store: store}

	_, err := h.PostV1DeploymentCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		ConfigID: apigen.DeploymentIdentifier{SpaceID: sqlite.OpendeploySpaceID, Name: "opendeploy-net"},
		NodeID:   primary.ID,
		Spec: apigen.DeploymentSpec{
			Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
			Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
			Networking: hostNetworking(),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "internal-only") {
		t.Fatalf("err = %v, want internal identity rejection", err)
	}
}

func TestDeploymentUpdatePreservesLegacyHostNetworking(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	created := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "primary", Name: "web"}, &apigen.DeploymentSpec{
		Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: hostNetworking(),
	}, apigen.DesiredState{})
	h := &Handler{Store: store}

	_, err := h.PostV1DeploymentUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: created.ID,
		Version:      created.Version + 1,
		Spec: apigen.DeploymentSpec{
			Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx:1.26"}},
			Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
			Networking: hostNetworking(),
		},
	})
	if err != nil {
		t.Fatalf("PostV1DeploymentUpdate failed: %v", err)
	}
	updated := h.findConfigByID(created.ID)
	if updated.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_HOST {
		t.Fatalf("networking mode = %v, want preserved host", updated.Spec.Networking.Mode)
	}
}

func TestDeploymentUpdatePreservesExistingVirtualNetworking(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	created := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "primary", Name: "web"}, &apigen.DeploymentSpec{
		Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: virtualNetworking(),
	}, apigen.DesiredState{})
	h := &Handler{Store: store}

	_, err := h.PostV1DeploymentUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: created.ID,
		Version:      created.Version + 1,
		Spec: apigen.DeploymentSpec{
			Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx:1.26"}},
			Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
			Networking: virtualNetworking(),
		},
	})
	if err != nil {
		t.Fatalf("PostV1DeploymentUpdate failed: %v", err)
	}
	updated := h.findConfigByID(created.ID)
	if updated.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
		t.Fatalf("networking mode = %v, want preserved virtual", updated.Spec.Networking.Mode)
	}
}

func TestDeploymentUpdateAcceptsManagedDefaultVolumeMount(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	secretsManager, err := secrets.Initialize(t.TempDir(), store)
	if err != nil {
		t.Fatalf("secrets.Initialize failed: %v", err)
	}
	source := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "primary", Name: "database"}, &apigen.DeploymentSpec{
		Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "postgres"}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: hostNetworking(),
	}, apigen.DesiredState{})
	target := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "primary", Name: "web"}, &apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner: apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{
			Mounts: []*apigen.ContainerMount{{
				Host:      "/var/lib/opendeploy-volumes/" + strconv.Itoa(int(source.ID)) + "/default",
				Container: "/var/lib/postgresql/data",
			}},
		}},
		Networking: hostNetworking(),
	}, apigen.DesiredState{})
	h := &Handler{Store: store, Secrets: secretsManager}

	spec := target.Spec
	spec.Runner.Container.EnvVars = map[string]*apigen.EnvVarValue{
		"LOG_LEVEL": {Value: ptrString("debug")},
	}
	_, err = h.PostV1DeploymentUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: target.ID,
		Version:      target.Version + 1,
		Spec:         spec,
	})
	if err != nil {
		t.Fatalf("PostV1DeploymentUpdate failed: %v", err)
	}
	updated := h.findConfigByID(target.ID)
	if got := updated.Spec.Runner.Container.EnvVars["LOG_LEVEL"].Value; got == nil || *got != "debug" {
		t.Fatalf("LOG_LEVEL = %+v, want debug", updated.Spec.Runner.Container.EnvVars["LOG_LEVEL"])
	}
}

func TestDeploymentDeleteRequiresStoppedDeployment(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	created := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "primary", Name: "web"}, &apigen.DeploymentSpec{
		Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: hostNetworking(),
	}, apigen.DesiredState{Version: "1.25", Running: false})
	store.MustWriteDeploymentStatus(created.ID, func(s *apigen.DeploymentStatus) bool {
		s.Runner.Status = apigen.RunningStatus_RUNNING
		return true
	})
	h := &Handler{Store: store}

	_, err := h.PostV1DeploymentUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID:  created.ID,
		Version:       created.Version + 1,
		TargetVersion: "1.26",
	})
	if err != nil {
		t.Fatalf("PostV1DeploymentUpdate setup failed: %v", err)
	}
	cfg := h.findConfigByID(created.ID)
	if cfg == nil {
		t.Fatal("deployment not found")
	}
	err = h.PostV1DeploymentDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: created.ID, Version: cfg.Version + 1})
	if err == nil || !strings.Contains(err.Error(), "must be stopped") {
		t.Fatalf("err = %v, want stopped-only rejection", err)
	}
}

func TestDeploymentDeleteAllowsRunningMissingMachineDeployment(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	store.EnsurePrimaryNode("worker", "worker-a")
	created := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "worker-a", Name: "web"}, &apigen.DeploymentSpec{
		Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: hostNetworking(),
	}, apigen.DesiredState{Version: "1.25", Running: true})
	store.MustWriteDeploymentStatus(created.ID, func(s *apigen.DeploymentStatus) bool {
		s.Runner.Status = apigen.RunningStatus_RUNNING
		return true
	})
	h := &Handler{Store: store, NodeID: primary.ID}

	err := h.PostV1DeploymentDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: created.ID, Version: created.Version + 1})
	if err != nil {
		t.Fatalf("PostV1DeploymentDelete failed: %v", err)
	}
	if cfg := h.findConfigByID(created.ID); cfg != nil {
		t.Fatalf("deleted deployment still active: %+v", cfg)
	}
}

func TestDeploymentDeleteAllowsStaleDisconnectedSystemDeployment(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	store.EnsurePrimaryNode("worker", "worker-a")
	store.EnsureSystemDeployment("worker-a", "v0.0.194")
	system := findSystemDeployment(t, store, "worker-a")
	store.MustWriteDeploymentStatus(system.ID, func(s *apigen.DeploymentStatus) bool {
		// The node is disconnected, so this status is stale metadata and should not
		// block cleanup.
		s.Runner.Status = apigen.RunningStatus_CRASHED
		return true
	})
	h := &Handler{
		Store:   store,
		NodeID:  primary.ID,
		Cluster: clusterhandler.New(store, nil, nil, nil, network.Prefix{}),
	}

	err := h.PostV1DeploymentDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: system.ID, Version: system.Version + 1})
	if err != nil {
		t.Fatalf("PostV1DeploymentDelete failed: %v", err)
	}
	if cfg := h.findConfigByID(system.ID); cfg != nil {
		t.Fatalf("deleted system deployment still active: %+v", cfg)
	}
}

func TestDeploymentDeleteRejectsPrimarySystemDeployment(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	store.EnsureSystemDeployment("primary", "v0.0.194")
	system := findSystemDeployment(t, store, "primary")
	store.MustWriteDeploymentStatus(system.ID, func(s *apigen.DeploymentStatus) bool {
		s.Runner.Status = apigen.RunningStatus_STOPPED
		return true
	})
	h := &Handler{
		Store:   store,
		NodeID:  primary.ID,
		Cluster: clusterhandler.New(store, nil, nil, nil, network.Prefix{}),
	}

	err := h.PostV1DeploymentDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: system.ID, Version: system.Version + 1})
	if err == nil || !strings.Contains(err.Error(), "internal-only") {
		t.Fatalf("err = %v, want internal-only rejection", err)
	}
}

func TestDeploymentDeleteSoftDeletesStoppedDeployment(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	created := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "primary", Name: "web"}, &apigen.DeploymentSpec{
		Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: hostNetworking(),
	}, apigen.DesiredState{Version: "1.25", Running: false})
	store.MustWriteDeploymentStatus(created.ID, func(s *apigen.DeploymentStatus) bool {
		s.Runner.Status = apigen.RunningStatus_STOPPED
		return true
	})
	h := &Handler{Store: store}

	err := h.PostV1DeploymentDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: created.ID, Version: created.Version + 1})
	if err != nil {
		t.Fatalf("PostV1DeploymentDelete failed: %v", err)
	}
	if cfg := h.findConfigByID(created.ID); cfg != nil {
		t.Fatalf("deleted deployment still active: %+v", cfg)
	}
	history := store.MustFetchDeploymentHistory(created.ID)
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}
	deleted := history[len(history)-1]
	if !deleted.Deleted || deleted.DesiredState.Running || deleted.DesiredState.Version != "1.25" {
		t.Fatalf("deleted history entry = %+v", deleted)
	}
}

func TestDeploymentCreateWithDeletedIdentityCreatesIndependentDeployment(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	h := &Handler{Store: store}
	spec := apigen.DeploymentSpec{
		Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: hostNetworking(),
	}
	create := func(version string) *apigen.DeploymentConfig {
		t.Helper()
		cfg, err := h.PostV1DeploymentCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			ConfigID:     apigen.DeploymentIdentifier{SpaceID: 1, Name: "web"},
			NodeID:       primary.ID,
			Spec:         spec,
			DesiredState: apigen.DesiredState{Version: version, Running: false},
		})
		if err != nil {
			t.Fatalf("PostV1DeploymentCreate: %v", err)
		}
		return cfg
	}

	first := create("1.25")
	store.MustWriteDeploymentStatus(first.ID, func(s *apigen.DeploymentStatus) bool {
		s.Runner.Status = apigen.RunningStatus_STOPPED
		return true
	})
	if err := h.PostV1DeploymentDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{
		DeploymentID: first.ID,
		Version:      first.Version + 1,
	}); err != nil {
		t.Fatalf("PostV1DeploymentDelete: %v", err)
	}

	second := create("1.26")
	if second.ID == first.ID {
		t.Fatalf("new deployment reused deleted deployment ID %d", first.ID)
	}
	if second.Version != 1 {
		t.Fatalf("new deployment version = %d, want 1", second.Version)
	}
	if second.DesiredState.Version != "1.26" {
		t.Fatalf("new deployment desired state = %+v", second.DesiredState)
	}

	firstHistory := store.MustFetchDeploymentHistory(first.ID)
	if len(firstHistory) != 2 || !firstHistory[len(firstHistory)-1].Deleted {
		t.Fatalf("deleted deployment history = %+v, want independent two-entry history", firstHistory)
	}
	secondHistory := store.MustFetchDeploymentHistory(second.ID)
	if len(secondHistory) != 1 || secondHistory[0].Deleted || secondHistory[0].Version != 1 {
		t.Fatalf("new deployment history = %+v, want independent initial history", secondHistory)
	}
	active := store.ListActiveDeploymentConfigs()
	if len(active) != 1 || active[0].ID != second.ID {
		t.Fatalf("active deployments = %+v, want only new deployment %d", active, second.ID)
	}
}

func TestDeploymentUpdateCombinesSpaceAndDesiredStateInSingleConfigVersion(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	created := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "primary", Name: "web"}, &apigen.DeploymentSpec{
		Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: hostNetworking(),
	}, apigen.DesiredState{})
	h := &Handler{Store: store}

	spaceID := int32(2)
	_, err := h.PostV1DeploymentUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID:  created.ID,
		Version:       created.Version + 1,
		SpaceID:       &spaceID,
		TargetVersion: "1.25",
	})
	if err != nil {
		t.Fatalf("PostV1DeploymentUpdate failed: %v", err)
	}

	cfg := h.findConfigByID(created.ID)
	if cfg == nil {
		t.Fatal("updated deployment not found")
	}
	if cfg.Version != created.Version+1 {
		t.Fatalf("version = %d, want %d", cfg.Version, created.Version+1)
	}
	if cfg.ConfigID.SpaceID != spaceID {
		t.Fatalf("space = %d, want %d", cfg.ConfigID.SpaceID, spaceID)
	}
	if cfg.DesiredState.Version != "1.25" || !cfg.DesiredState.Running {
		t.Fatalf("desired state = %+v, want running 1.25", cfg.DesiredState)
	}
	if history := store.MustFetchDeploymentHistory(created.ID); len(history) != 2 {
		t.Fatalf("history len = %d, want create + one combined update", len(history))
	}
}

func TestDeploymentUpdateRejectsStaleExpectedVersion(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	created := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "primary", Name: "web"}, &apigen.DeploymentSpec{
		Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: hostNetworking(),
	}, apigen.DesiredState{})
	h := &Handler{Store: store}

	_, err := h.PostV1DeploymentUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID:  created.ID,
		Version:       created.Version,
		TargetVersion: "1.25",
	})
	apiErr, ok := err.(apigen.ApiErr)
	if !ok || apiErr.Code != http.StatusBadRequest || !strings.Contains(apiErr.Error(), "version mismatch") {
		t.Fatalf("err = %#v, want 400 version mismatch", err)
	}
}

func TestDeploymentUpdateRequiresExpectedVersion(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	created := store.MustCreateDeployment(apigen.Context{}, &apigen.DeploymentIdentifier{SpaceID: 1, Machine: "primary", Name: "web"}, &apigen.DeploymentSpec{
		Prepare:    apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "nginx"}},
		Runner:     apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
		Networking: hostNetworking(),
	}, apigen.DesiredState{})
	h := &Handler{Store: store}

	_, err := h.PostV1DeploymentUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID:  created.ID,
		TargetVersion: "1.25",
	})
	apiErr, ok := err.(apigen.ApiErr)
	if !ok || apiErr.Code != http.StatusBadRequest || !strings.Contains(apiErr.Error(), "version mismatch") {
		t.Fatalf("err = %#v, want 400 version mismatch", err)
	}
}

func ptrInt32(v int32) *int32    { return &v }
func ptrString(v string) *string { return &v }
