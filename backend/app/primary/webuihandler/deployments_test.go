package webuihandler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/clusterhandler"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/engine/versionprovider"
	"github.com/jptrs93/opsagent/backend/lib/network"
	githubrepo "github.com/jptrs93/opsagent/backend/lib/repo/github"
	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

const testNixCommit = "0123456789abcdef0123456789abcdef01234567"
const testNixCommit2 = "89abcdef0123456789abcdef0123456789abcdef"

func findSystemDeployment(t *testing.T, store *state.Service, nodeID int32) *apigen.DeploymentConfig {
	t.Helper()
	for _, cfg := range store.ListActiveDeploymentConfigs() {
		if internaldeploy.IsSelfConfig(cfg) && cfg.NodeID == nodeID {
			return cfg
		}
	}
	t.Fatalf("system deployment for node %d not found", nodeID)
	return nil
}

func seedInstanceRunnerStatus(store *state.Service, deploymentID, version, nodeID int32, status apigen.RunningStatus) {
	inst := store.CreateScheduledInstance(deploymentID, version, nodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	store.MustWriteScheduledInstanceStatus(inst.ID, func(s *apigen.ScheduledInstanceStatus) bool {
		s.BumpUpdatedAt()
		s.Runner.Status = status
		return true
	})
}

func seedDeploymentRunnerStatus(store *state.Service, cfg *apigen.DeploymentConfig, status apigen.RunningStatus) {
	seedInstanceRunnerStatus(store, cfg.ID, cfg.Version, cfg.NodeID, status)
}

func createTestDeployment(store *state.Service, nodeIdentifier string, identity apigen.DeploymentIdentity, spec *apigen.DeploymentSpec) *apigen.DeploymentConfig {
	node := store.EnsurePrimaryNode(nodeIdentifier, nodeIdentifier)
	return store.MustCreateDeploymentForNode(apigen.Context{}, &identity, node.ID, spec)
}

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
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
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
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
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
	spec, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
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
			_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
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

func TestDeploymentCreateEnforcesRunningNixSource(t *testing.T) {
	t.Run("running verifies before persistence", func(t *testing.T) {
		store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
		node := store.EnsurePrimaryNode("primary", "primary")
		provider := &fakeGitSourceProvider{sourceCommitValid: true}
		h := &Handler{ConfigService: &config.Service{}, Store: store, GitVersions: provider}

		cfg, err := h.PostV1DeploymentsCreate(apigen.Context{Ctx: context.Background()}, nixCreateRequest(node.ID, "web", true))
		if err != nil {
			t.Fatal(err)
		}
		if cfg == nil || len(provider.validateCalls) != 1 {
			t.Fatalf("config/provider calls = %v/%v", cfg, provider.validateCalls)
		}
	})

	t.Run("verification failure persists nothing", func(t *testing.T) {
		store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
		node := store.EnsurePrimaryNode("primary", "primary")
		provider := &fakeGitSourceProvider{sourceErr: errors.New("remote unavailable")}
		h := &Handler{ConfigService: &config.Service{}, Store: store, GitVersions: provider}

		_, err := h.PostV1DeploymentsCreate(apigen.Context{Ctx: context.Background()}, nixCreateRequest(node.ID, "web", true))
		if err == nil {
			t.Fatal("expected source verification failure")
		}
		if got := len(store.ListActiveDeploymentConfigs()); got != 0 {
			t.Fatalf("persisted deployments = %d, want 0", got)
		}
	})

	t.Run("stopped skips provider", func(t *testing.T) {
		store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
		node := store.EnsurePrimaryNode("primary", "primary")
		provider := &fakeGitSourceProvider{sourceErr: errors.New("must not be called")}
		h := &Handler{ConfigService: &config.Service{}, Store: store, GitVersions: provider}

		cfg, err := h.PostV1DeploymentsCreate(apigen.Context{Ctx: context.Background()}, nixCreateRequest(node.ID, "web", false))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.WorkloadRunning() || len(provider.validateCalls) != 0 {
			t.Fatalf("config/provider calls = %+v/%v", cfg.Spec.Container1Spec, provider.validateCalls)
		}
	})

	t.Run("stopped still requires immutable version syntax", func(t *testing.T) {
		store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
		node := store.EnsurePrimaryNode("primary", "primary")
		provider := &fakeGitSourceProvider{}
		h := &Handler{ConfigService: &config.Service{}, Store: store, GitVersions: provider}
		req := nixCreateRequest(node.ID, "web", false)
		req.Spec.Container1Spec.Version = "main"

		if _, err := h.PostV1DeploymentsCreate(apigen.Context{Ctx: context.Background()}, req); err == nil {
			t.Fatal("expected mutable version rejection")
		}
		if len(provider.validateCalls) != 0 || len(store.ListActiveDeploymentConfigs()) != 0 {
			t.Fatalf("provider calls/deployments = %v/%d", provider.validateCalls, len(store.ListActiveDeploymentConfigs()))
		}
	})

	t.Run("stopped permits an empty desired version", func(t *testing.T) {
		store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
		node := store.EnsurePrimaryNode("primary", "primary")
		provider := &fakeGitSourceProvider{}
		h := &Handler{ConfigService: &config.Service{}, Store: store, GitVersions: provider}
		req := nixCreateRequest(node.ID, "web", false)
		req.Spec.Container1Spec.Version = ""

		cfg, err := h.PostV1DeploymentsCreate(apigen.Context{Ctx: context.Background()}, req)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.WorkloadVersion() != "" || cfg.WorkloadRunning() {
			t.Fatalf("workload state = %+v, want stopped with no version", cfg.Spec.Container1Spec)
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
		if _, err := h.PostV1DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, req); err == nil {
			t.Fatal("expected source verification failure")
		}
		unchanged := h.findConfigByID(cfg.ID)
		if unchanged.Version != cfg.Version || unchanged.WorkloadRunning() {
			t.Fatalf("deployment changed after failed verification: %+v", unchanged)
		}
		provider.sourceErr = nil
		provider.sourceCommitValid = true
		if _, err := h.PostV1DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, req); err != nil {
			t.Fatal(err)
		}
		if len(provider.validateCalls) != 2 {
			t.Fatalf("source calls = %v", provider.validateCalls)
		}
	})

	t.Run("target version change verifies", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, true)
		provider.validateCalls = nil
		_, err := h.PostV1DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.ID, Version: cfg.Version + 1, TargetVersion: testNixCommit2,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(provider.validateCalls) != 1 || provider.validateCalls[0].commit != testNixCommit2 {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
	})

	t.Run("stop preserves persisted version instead of request spec version", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, true)
		provider.validateCalls = nil
		spec := nixDeploymentSpecWithState("github.com/acme/app", "flake.nix", "main", true)
		if _, err := h.PostV1DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.ID, Version: cfg.Version + 1, Stop: true, Spec: spec,
		}); err != nil {
			t.Fatal(err)
		}
		updated := h.findConfigByID(cfg.ID)
		if updated.WorkloadVersion() != testNixCommit || updated.WorkloadRunning() {
			t.Fatalf("workload state = %q/%v, want %q/false", updated.WorkloadVersion(), updated.WorkloadRunning(), testNixCommit)
		}
		if len(provider.validateCalls) != 0 {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
	})

	t.Run("Nix spec change while running verifies", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, true)
		provider.validateCalls = nil
		spec := nixDeploymentSpec("github.com/acme/other", "nix/app/flake.nix")
		_, err := h.PostV1DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
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
		if _, err := h.PostV1DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.ID, Version: cfg.Version + 1, Stop: true, Spec: spec,
		}); err != nil {
			t.Fatal(err)
		}
		stopped := h.findConfigByID(cfg.ID)
		spec = nixDeploymentSpecWithState("github.com/acme/still-inaccessible", "flake.nix", "", false)
		if _, err := h.PostV1DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.ID, Version: stopped.Version + 1, Spec: spec,
		}); err != nil {
			t.Fatal(err)
		}
		if len(provider.validateCalls) != 0 {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
		if updated := h.findConfigByID(cfg.ID); updated.WorkloadVersion() != "" || updated.WorkloadRunning() {
			t.Fatalf("workload state after stopped source change = %+v, want empty stopped version", updated.Spec.Container1Spec)
		}
	})

	t.Run("unrelated and source no-op updates skip provider", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, true)
		provider.validateCalls = nil
		sameSpace := cfg.Identity.SpaceID
		if _, err := h.PostV1DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.ID, Version: cfg.Version + 1, SpaceID: &sameSpace,
		}); err != nil {
			t.Fatal(err)
		}
		updated := h.findConfigByID(cfg.ID)
		if _, err := h.PostV1DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: updated.ID, Version: updated.Version + 1, TargetVersion: testNixCommit,
		}); err != nil {
			t.Fatal(err)
		}
		if len(provider.validateCalls) != 0 {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
	})

	t.Run("stopping while changing source kind clears incompatible version", func(t *testing.T) {
		store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
		node := store.EnsurePrimaryNode("primary", "primary")
		provider := &fakeGitSourceProvider{sourceErr: errors.New("must not be called")}
		h := &Handler{ConfigService: &config.Service{}, Store: store, GitVersions: provider}
		cfg, err := h.PostV1DeploymentsCreate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentCreateRequest{
			Identity: apigen.DeploymentIdentity{SpaceID: 1, Name: "web"},
			NodeID:   node.ID,
			Spec: apigen.DeploymentSpec{
				Container1Spec: &apigen.ContainerSpec{
					Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "nginx"}},
					Version: "latest",
					Running: true,
				},
				Networking: hostNetworking(),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.PostV1DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.ID,
			Version:      cfg.Version + 1,
			Stop:         true,
			Spec:         nixDeploymentSpec("github.com/acme/app", "flake.nix"),
		}); err != nil {
			t.Fatal(err)
		}
		updated := h.findConfigByID(cfg.ID)
		if updated.WorkloadVersion() != "" || updated.WorkloadRunning() {
			t.Fatalf("workload state = %+v, want stopped with empty version", updated.Spec.Container1Spec)
		}
		if len(provider.validateCalls) != 0 {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
	})
}

func TestDeploymentVersionsUsesCombinedDiscovery(t *testing.T) {
	h, cfg, provider := newNixDeploymentHandler(t, false)
	provider.branches = []string{"main", "trunk"}
	provider.commits = []*apigen.Version{{ID: testNixCommit}}

	versions, err := h.PostV1DeploymentsVersions(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentVersionsRequest{
		DeploymentID: cfg.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if versions.NixDockerBuild.SelectedBranch != "main" {
		t.Fatalf("selected branch = %q, want main", versions.NixDockerBuild.SelectedBranch)
	}
	if provider.discoverVersionsCalls != 1 || provider.defaultCommitCalls != 0 || provider.listCommitsCalls != 0 {
		t.Fatalf("discover/default/commit calls = %d/%d/%d", provider.discoverVersionsCalls, provider.defaultCommitCalls, provider.listCommitsCalls)
	}
}

func TestDeploymentVersionsFallsBackToPreferredLocalBranch(t *testing.T) {
	h, cfg, provider := newNixDeploymentHandler(t, false)
	provider.branches = []string{"release", "prod"}
	provider.commits = []*apigen.Version{{ID: testNixCommit}}

	versions, err := h.PostV1DeploymentsVersions(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentVersionsRequest{
		DeploymentID: cfg.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if versions.NixDockerBuild.SelectedBranch != "prod" {
		t.Fatalf("selected branch = %q, want prod fallback", versions.NixDockerBuild.SelectedBranch)
	}
}

func TestDeploymentVersionsHandlesRepositoryWithoutBranches(t *testing.T) {
	h, cfg, provider := newNixDeploymentHandler(t, false)
	provider.defaultErr = errors.New("remote HEAD is unavailable")

	versions, err := h.PostV1DeploymentsVersions(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentVersionsRequest{
		DeploymentID: cfg.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if versions.NixDockerBuild.SelectedBranch != "" || len(versions.NixDockerBuild.Commits) != 0 {
		t.Fatalf("versions = %+v, want no selected branch or commits", versions.NixDockerBuild)
	}
	if provider.listCommitsCalls != 0 {
		t.Fatalf("list commits calls = %d, want 0", provider.listCommitsCalls)
	}
	if provider.discoverVersionsCalls != 1 {
		t.Fatalf("discover calls = %d, want 1", provider.discoverVersionsCalls)
	}
}

func TestDeploymentVersionsGithubReleaseFailuresAreDisplayable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "github upstream failure marker", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := githubrepo.NewClient(githubcredentials.SecretProvider{}, githubrepo.WithAPIBaseURL(server.URL))
	provider := versionprovider.NewGithubReleaseVersionProviderWithClient(client)
	tests := []struct {
		name             string
		createDeployment func(*testing.T, *state.Service) *apigen.DeploymentConfig
		provider         *versionprovider.GithubReleaseVersionProvider
		wantInternal     string
	}{
		{
			name: "opendeploy-net special branch",
			createDeployment: func(_ *testing.T, store *state.Service) *apigen.DeploymentConfig {
				node := store.EnsurePrimaryNode("primary", "primary")
				return store.EnsureNetproxyDeployment(node.ID, "v1.2.3")
			},
			provider:     provider,
			wantInternal: "listing releases: github api 503: github upstream failure marker",
		},
		{
			name: "GitHub release config branch",
			createDeployment: func(t *testing.T, store *state.Service) *apigen.DeploymentConfig {
				node := store.EnsurePrimaryNode("primary", "primary")
				store.EnsureSystemDeployment(node.ID, "v1.2.3")
				return findSystemDeployment(t, store, node.ID)
			},
			provider:     provider,
			wantInternal: "listing releases: github api 503: github upstream failure marker",
		},
		{
			name: "unconfigured provider",
			createDeployment: func(t *testing.T, store *state.Service) *apigen.DeploymentConfig {
				node := store.EnsurePrimaryNode("primary", "primary")
				store.EnsureSystemDeployment(node.ID, "v1.2.3")
				return findSystemDeployment(t, store, node.ID)
			},
			wantInternal: "github release version loading is not configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
			store.EnsurePrimaryNode("primary", "primary")
			cfg := tt.createDeployment(t, store)
			h := &Handler{ConfigService: &config.Service{}, Store: store, GithubReleaseVersions: tt.provider}

			_, err := h.PostV1DeploymentsVersions(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentVersionsRequest{DeploymentID: cfg.ID})
			var apiErr apigen.ApiErr
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %#v, want apigen.ApiErr", err)
			}
			if apiErr.Code != http.StatusBadGateway {
				t.Errorf("status = %d, want %d", apiErr.Code, http.StatusBadGateway)
			}
			if apiErr.DisplayErr != githubReleaseVersionsDisplayErr {
				t.Errorf("display error = %q, want %q", apiErr.DisplayErr, githubReleaseVersionsDisplayErr)
			}
			if !strings.Contains(apiErr.InternalErr, tt.wantInternal) {
				t.Errorf("internal error = %q, want containing %q", apiErr.InternalErr, tt.wantInternal)
			}
		})
	}
}

func nixCreateRequest(nodeID int32, name string, running bool) *apigen.DeploymentCreateRequest {
	return &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: 1, Name: name},
		NodeID:   nodeID,
		Spec:     nixDeploymentSpecWithState("github.com/acme/app", "flake.nix", testNixCommit, running),
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

func newNixDeploymentHandler(t *testing.T, running bool) (*Handler, *apigen.DeploymentConfig, *fakeGitSourceProvider) {
	t.Helper()
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := store.EnsurePrimaryNode("primary", "primary")
	provider := &fakeGitSourceProvider{sourceCommitValid: true}
	h := &Handler{ConfigService: &config.Service{}, Store: store, GitVersions: provider}
	cfg, err := h.PostV1DeploymentsCreate(apigen.Context{Ctx: context.Background()}, nixCreateRequest(node.ID, "web", running))
	if err != nil {
		t.Fatal(err)
	}
	return h, cfg, provider
}

func TestValidateDeploymentSpecRejectsNonLocalNixTarget(t *testing.T) {
	_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		Container1Spec: &apigen.ContainerSpec{Source: apigen.ContainerBundleSource{
			NixDockerBuild: &apigen.NixDockerBuild{Repo: "github.com/acme/web", Flake: "flake.nix", Target: "github:acme/web#image"},
		}},
		Networking: hostNetworking(),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "local flake selector") {
		t.Fatalf("err = %v, want local target rejection", err)
	}
}

type fakeAssetResolver map[string]*apigen.AssetVersion

func (r fakeAssetResolver) GetAssetVersionByID(assetVersionID int32) (*apigen.AssetVersion, bool) {
	for _, asset := range r {
		if asset != nil && asset.ID == assetVersionID {
			return asset, true
		}
	}
	return nil, false
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
			ID:        42,
			Key:       "nginx.conf",
			CreatedAt: time.UnixMilli(1000),
			Version:   3,
		},
	}
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container1Spec.Runtime.AssetMounts = []*apigen.AssetMount{{
		AssetVersionID: 42, ContainerPath: "/etc/nginx/nginx.conf", Permission: apigen.FilePermission_READ_EXECUTE,
	}}
	spec, err := validateDeploymentSpecWithAssets(&input, assets)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
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
			ID:      51,
			Key:     "app.conf",
			Version: 7,
		},
	}
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{"APP_CONFIG": {AssetVersionID: 51}}
	spec, err := validateDeploymentSpecWithAssets(&input, assets)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	value := spec.Container1Spec.Runtime.EnvVars["APP_CONFIG"]
	if value.Asset != "app.conf" || value.AssetVersionID != 51 {
		t.Fatalf("env asset ref not resolved: %+v", value)
	}
}

func TestValidateDeploymentSpecRejectsUnknownEnvAssetRef(t *testing.T) {
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{"APP_CONFIG": {AssetVersionID: 999}}
	_, err := validateDeploymentSpecWithAssets(&input, fakeAssetResolver{})
	if err == nil || !strings.Contains(err.Error(), `asset version id 999 not found`) {
		t.Fatalf("err = %v, want unknown asset", err)
	}
}

func TestValidateDeploymentSpecAcceptsHostMounts(t *testing.T) {
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container1Spec.Runtime.Mounts = []*apigen.CustomHostMount{{
		HostPath: " /home/ubuntu/coflip-server/data ", ContainerPath: " /data ", Permission: apigen.FilePermission_READ_WRITE,
	}}
	spec, err := validateDeploymentSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	mount := spec.Container1Spec.Runtime.Mounts[0]
	if mount.HostPath != "/home/ubuntu/coflip-server/data" || mount.ContainerPath != "/data" || mount.Permission != apigen.FilePermission_READ_WRITE {
		t.Fatalf("mount not normalized: %+v", mount)
	}
}

func TestValidateDeploymentSpecValidatesV2Mounts(t *testing.T) {
	t.Run("default volume path", func(t *testing.T) {
		input := remoteDeploymentSpec("nginx", hostNetworking())
		input.Container1Spec.Runtime.DefaultVolume.ContainerPath = "data"
		if _, err := validateDeploymentSpecWithAssets(&input, nil); err == nil || !strings.Contains(err.Error(), "defaultVolume.containerPath") {
			t.Fatalf("err = %v, want invalid default volume path", err)
		}
	})

	t.Run("custom mount permission", func(t *testing.T) {
		input := remoteDeploymentSpec("nginx", hostNetworking())
		input.Container1Spec.Runtime.Mounts = []*apigen.CustomHostMount{{HostPath: "/srv/data", ContainerPath: "/data"}}
		if _, err := validateDeploymentSpecWithAssets(&input, nil); err == nil || !strings.Contains(err.Error(), "permission") {
			t.Fatalf("err = %v, want custom mount permission rejection", err)
		}
	})

	t.Run("cross-deployment mount permission", func(t *testing.T) {
		input := remoteDeploymentSpec("nginx", hostNetworking())
		input.Container1Spec.Runtime.CrossDeploymentMounts = []*apigen.CrossDeploymentMount{{DeploymentID: 2, ContainerPath: "/data"}}
		if _, err := validateDeploymentSpecWithAssets(&input, nil); err == nil || !strings.Contains(err.Error(), "permission") {
			t.Fatalf("err = %v, want cross-deployment mount permission rejection", err)
		}
	})

	t.Run("asset mount permission", func(t *testing.T) {
		input := remoteDeploymentSpec("nginx", hostNetworking())
		input.Container1Spec.Runtime.AssetMounts = []*apigen.AssetMount{{AssetVersionID: 1, ContainerPath: "/etc/app.conf", Permission: apigen.FilePermission_READ_WRITE}}
		assets := fakeAssetResolver{"app.conf": {ID: 1, Key: "app.conf"}}
		if _, err := validateDeploymentSpecWithAssets(&input, assets); err == nil || !strings.Contains(err.Error(), "READ_ONLY or READ_EXECUTE") {
			t.Fatalf("err = %v, want asset mount permission rejection", err)
		}
	})
}

func TestValidateDeploymentSpecNormalizesContainerCommand(t *testing.T) {
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container1Spec.Runtime.OverrideCommand = []string{" /app/server ", "", " --listen ", " :8080 ", "   "}
	spec, err := validateDeploymentSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	want := []string{"/app/server", "--listen", ":8080"}
	if got := spec.Container1Spec.Runtime.OverrideCommand; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
}

func TestValidateDeploymentSpecAcceptsDevShmSizeKb(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container1Spec.Runtime.DevShmSizeKb = 65536
	spec, err := validateDeploymentSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	if spec.Container1Spec.Runtime.DevShmSizeKb != 65536 {
		t.Fatalf("devShmSizeKb = %d, want 65536", spec.Container1Spec.Runtime.DevShmSizeKb)
	}
}

func TestValidateDeploymentSpecRejectsInvalidDevShmSizeKb(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container1Spec.Runtime.DevShmSizeKb = -1
	_, err := validateDeploymentSpecWithAssets(&input, nil)
	if err == nil || !strings.Contains(err.Error(), "devShmSizeKb") {
		t.Fatalf("err = %v, want invalid devShmSizeKb", err)
	}
}

func TestValidateDeploymentSpecAcceptsFileDescriptorLimit(t *testing.T) {
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container1Spec.Runtime.FileDescriptorLimit = 4096
	spec, err := validateDeploymentSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	if spec.Container1Spec.Runtime.FileDescriptorLimit != 4096 {
		t.Fatalf("fileDescriptorLimit = %d, want 4096", spec.Container1Spec.Runtime.FileDescriptorLimit)
	}
}

func TestValidateDeploymentSpecRejectsInvalidFileDescriptorLimit(t *testing.T) {
	input := remoteDeploymentSpec("nginx:latest", hostNetworking())
	input.Container1Spec.Runtime.FileDescriptorLimit = -1
	_, err := validateDeploymentSpecWithAssets(&input, nil)
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
			input := remoteDeploymentSpec("nginx:latest", hostNetworking())
			input.Container1Spec.Runtime.Mounts = []*apigen.CustomHostMount{{
				HostPath: tc.host, ContainerPath: tc.container, Permission: apigen.FilePermission_READ_WRITE,
			}}
			_, err := validateDeploymentSpecWithAssets(&input, nil)
			if err == nil {
				t.Fatal("expected invalid host mount")
			}
		})
	}
}

func TestValidateDeploymentSpecRejectsSystemdRunner(t *testing.T) {
	_, err := validateDeploymentSpecWithAssets(&apigen.DeploymentSpec{
		SystemdSpec: &apigen.SystemdSpec{
			Source:  &apigen.GithubRelease{Repo: "github.com/acme/web"},
			Runtime: &apigen.SystemdRuntime{Name: "opendeploy", BinPath: "/var/lib/opendeploy/bin/opendeploy"},
		},
		Networking: hostNetworking(),
	}, nil)
	if err == nil {
		t.Fatal("expected validateDeploymentSpecWithAssets to reject systemd runner")
	}
}

func TestDeploymentUpdateRejectsSystemDeploymentSpecUpdate(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := store.EnsurePrimaryNode("primary", "primary")
	store.EnsureSystemDeployment(node.ID, "v0.0.194")
	var system *apigen.DeploymentConfig
	for _, cfg := range store.ListActiveDeploymentConfigs() {
		if internaldeploy.IsSelfConfig(cfg) {
			system = cfg
			break
		}
	}
	if system == nil {
		t.Fatal("system deployment not found")
	}

	h := &Handler{ConfigService: &config.Service{}, Store: store}
	_, err := h.PostV1DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: system.ID,
		Version:      system.Version + 1,
		Spec:         remoteDeploymentSpec("nginx", hostNetworking()),
	})
	if err == nil || !strings.Contains(err.Error(), "internal-only") {
		t.Fatalf("err = %v, want internal-only rejection", err)
	}
}

func TestValidateDeploymentSpecAcceptsKnownEnvRefs(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{
		"PGUSER": {SecretVersionID: ptrInt32(6)}, "PGDATABASE": {ConfigRefID: ptrInt32(18)},
	}
	_, err := validateDeploymentSpecWithResolvers(&input, nil, fakeSecretResolver{6: "postgres"}, fakeConfigResolver{18: "postgres"})
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithResolvers failed: %v", err)
	}
}

func TestValidateDeploymentSpecRejectsMissingNetworking(t *testing.T) {
	input := remoteDeploymentSpec("nginx", apigen.NetworkingConfig{})
	_, err := validateDeploymentSpecWithAssets(&input, nil)
	if err == nil || !strings.Contains(err.Error(), "networking is required") {
		t.Fatalf("err = %v, want networking required", err)
	}
}

func TestValidateDeploymentSpecAcceptsExplicitHostNetworking(t *testing.T) {
	input := remoteDeploymentSpec("nginx", hostNetworking())
	spec, err := validateDeploymentSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	if spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_HOST {
		t.Fatalf("networking mode = %v, want host", spec.Networking.Mode)
	}
}

func TestValidateDeploymentSpecAcceptsHostNetworkingRollover(t *testing.T) {
	input := remoteDeploymentSpec("nginx", hostNetworking())
	input.Container1Spec.UpgradeStrategy = apigen.ContainerUpgradeStrategy_ROLLOVER
	spec, err := validateDeploymentSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
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
	spec, err := validateDeploymentSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
	}
	if got := len(spec.Networking.PortForwarding); got != 2 {
		t.Fatalf("portForwarding count = %d, want 2", got)
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
	spec, err := validateDeploymentSpecWithAssets(&input, nil)
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithAssets failed: %v", err)
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
			name:       "unspecified mode with port forwarding",
			networking: apigen.NetworkingConfig{PortForwarding: []*apigen.PortForward{{Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 18080, ContainerPort: 8080}}},
			want:       "networking.mode is required",
		},
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
			_, err := validateDeploymentSpecWithAssets(&spec, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateDeploymentSpecRejectsNetproxyImage(t *testing.T) {
	input := remoteDeploymentSpec(internaldeploy.NetproxyImage, hostNetworking())
	_, err := validateDeploymentSpecWithAssets(&input, nil)
	if err == nil || !strings.Contains(err.Error(), "internal-only") {
		t.Fatalf("err = %v, want netproxy image internal-only rejection", err)
	}
}

func TestValidateDeploymentSpecRejectsUnknownSecretRef(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{"PGPASSWORD": {SecretVersionID: ptrInt32(99)}}
	_, err := validateDeploymentSpecWithResolvers(&input, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err == nil || !strings.Contains(err.Error(), "unknown secret id 99") {
		t.Fatalf("err = %v, want unknown secret", err)
	}
}

func TestValidateDeploymentSpecRejectsUnknownConfigRef(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{"PGDATABASE": {ConfigRefID: ptrInt32(99)}}
	_, err := validateDeploymentSpecWithResolvers(&input, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err == nil || !strings.Contains(err.Error(), "unknown config id 99") {
		t.Fatalf("err = %v, want unknown config", err)
	}
}

func TestValidateDeploymentSpecAcceptsLiteralEnvValues(t *testing.T) {
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{
		"LITERAL": {Value: ptrString("${s:not.real} and ${c:not.real}")},
	}
	_, err := validateDeploymentSpecWithResolvers(&input, nil, fakeSecretResolver{}, fakeConfigResolver{})
	if err != nil {
		t.Fatalf("validateDeploymentSpecWithResolvers failed: %v", err)
	}
}

func TestValidateDeploymentSpecRejectsIncompleteAddressRef(t *testing.T) {
	deploymentID := int32(7)
	input := remoteDeploymentSpec("postgres:16", hostNetworking())
	input.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{"UPSTREAM": {AddressDeploymentID: &deploymentID}}
	_, err := validateDeploymentSpecWithAssets(&input, nil)
	if err == nil || !strings.Contains(err.Error(), "required together") {
		t.Fatalf("err = %v, want incomplete address rejection", err)
	}
}

func TestDeploymentAddressEnvRefsValidateAndBlockTargetChanges(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	worker := store.EnsurePrimaryNode("worker", "worker")
	secretsManager, err := secrets.Initialize(t.TempDir(), store)
	if err != nil {
		t.Fatalf("secrets.Initialize: %v", err)
	}
	h := &Handler{ConfigService: &config.Service{}, Store: store, Secrets: secretsManager}

	create := func(name string, nodeID int32, networking apigen.NetworkingConfig, env map[string]*apigen.EnvVarValue) *apigen.DeploymentConfig {
		t.Helper()
		spec := remoteDeploymentSpec("nginx", networking)
		spec.Container1Spec.Runtime.EnvVars = env
		cfg, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			Identity: apigen.DeploymentIdentity{SpaceID: 1, Name: name},
			NodeID:   nodeID,
			Spec:     spec,
		})
		if err != nil {
			t.Fatalf("PostV1DeploymentsCreate %s: %v", name, err)
		}
		return cfg
	}

	target := create("database", primary.ID, virtualNetworking(), nil)
	addressDeploymentID := target.ID
	addressSpaceID := int32(1)
	consumer := create("web", primary.ID, hostNetworking(), map[string]*apigen.EnvVarValue{
		"DATABASE_ADDR": {AddressDeploymentID: &addressDeploymentID, AddressSpaceID: &addressSpaceID},
	})
	if got := consumer.Spec.Container1Spec.Runtime.EnvVars["DATABASE_ADDR"]; got.AddressDeploymentID == nil || got.AddressSpaceID == nil {
		t.Fatalf("address ref was not stored: %+v", got)
	}

	wrongSpace := int32(2)
	_, err = h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: 1, Name: "wrong-space"},
		NodeID:   primary.ID,
		Spec: func() apigen.DeploymentSpec {
			spec := remoteDeploymentSpec("nginx", hostNetworking())
			spec.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{
				"DATABASE_ADDR": {AddressDeploymentID: &addressDeploymentID, AddressSpaceID: &wrongSpace},
			}
			return spec
		}(),
	})
	if err == nil || !strings.Contains(err.Error(), "address space does not match") {
		t.Fatalf("err = %v, want address-space rejection", err)
	}

	remote := create("remote", worker.ID, virtualNetworking(), nil)
	remoteID := remote.ID
	crossNode, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: 1, Name: "cross-node"},
		NodeID:   primary.ID,
		Spec: func() apigen.DeploymentSpec {
			spec := remoteDeploymentSpec("nginx", hostNetworking())
			spec.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{
				"REMOTE_ADDR": {AddressDeploymentID: &remoteID, AddressSpaceID: &addressSpaceID},
			}
			return spec
		}(),
	})
	if err != nil {
		t.Fatalf("cross-node address reference: %v", err)
	}
	if got := crossNode.Spec.Container1Spec.Runtime.EnvVars["REMOTE_ADDR"]; got.AddressDeploymentID == nil || *got.AddressDeploymentID != remoteID {
		t.Fatalf("cross-node address ref = %+v", got)
	}

	nextSpace, err := store.CreateSpace("other")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	nextSpaceID := nextSpace.ID
	_, err = h.PostV1DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: target.ID,
		Version:      target.Version + 1,
		SpaceID:      &nextSpaceID,
	})
	if err == nil || !strings.Contains(err.Error(), "address references exist") {
		t.Fatalf("err = %v, want address-dependent space move rejection", err)
	}

	_, err = h.PostV1DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: target.ID,
		Version:      target.Version + 1,
		Spec:         remoteDeploymentSpec("nginx", hostNetworking()),
	})
	if err == nil || !strings.Contains(err.Error(), "cannot leave virtual mode") {
		t.Fatalf("err = %v, want virtual-networking removal rejection", err)
	}

	seedDeploymentRunnerStatus(store, target, apigen.RunningStatus_STOPPED)
	err = h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: target.ID, Version: target.Version + 1})
	if err == nil || !strings.Contains(err.Error(), "reference_in_use") {
		t.Fatalf("err = %v, want referenced deployment deletion rejection", err)
	}
}

func TestDeploymentCreatePersistsInitialStoppedWorkloadState(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	h := &Handler{ConfigService: &config.Service{}, Store: store}

	cfg, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: 1, Name: "web"},
		NodeID:   primary.ID,
		Spec: func() apigen.DeploymentSpec {
			spec := remoteDeploymentSpec("nginx", hostNetworking())
			spec.Container1Spec.Version = "1.25"
			return spec
		}(),
	})
	if err != nil {
		t.Fatalf("PostV1DeploymentsCreate failed: %v", err)
	}
	if cfg.Version != 1 {
		t.Fatalf("version = %d, want initial config version 1", cfg.Version)
	}
	if cfg.WorkloadVersion() != "1.25" || cfg.WorkloadRunning() {
		t.Fatalf("workload state = %+v, want stopped 1.25", cfg.Spec.Container1Spec)
	}
	if history := store.MustFetchDeploymentHistory(cfg.ID); len(history) != 1 {
		t.Fatalf("history len = %d, want create only", len(history))
	} else if history[0].WorkloadVersion() != "1.25" || history[0].WorkloadRunning() {
		t.Fatalf("history workload state = %+v, want stopped 1.25", history[0].Spec.Container1Spec)
	}
}

func TestDeploymentCreateRejectsIngressClaimsAlreadyUsedOnNode(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	h := &Handler{ConfigService: &config.Service{}, Store: store, NodeID: primary.ID}
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
	_, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: 1, Name: "database"},
		NodeID:   primary.ID,
		Spec:     remoteDeploymentSpec("postgres", ingress("db.example.com")),
	})
	if err != nil {
		t.Fatalf("creating first ingress deployment: %v", err)
	}
	_, err = h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: 1, Name: "database-copy"},
		NodeID:   primary.ID,
		Spec:     remoteDeploymentSpec("postgres", ingress("DB.EXAMPLE.COM")),
	})
	if err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("err = %v, want duplicate ingress claim rejection", err)
	}
	_, err = h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: 1, Name: "direct"},
		NodeID:   primary.ID,
		Spec: remoteDeploymentSpec("nginx", apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
			PortForwarding: []*apigen.PortForward{{
				Protocol:      apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP,
				HostPort:      8443,
				ContainerPort: 443,
			}},
		}),
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts with ingress") {
		t.Fatalf("err = %v, want raw port conflict rejection", err)
	}
}

func TestDeploymentCreateRejectsPrimaryIngressOnPort443(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	worker := store.EnsurePrimaryNode("worker", "worker")
	h := &Handler{ConfigService: &config.Service{}, Store: store, NodeID: primary.ID}
	spec := func() apigen.DeploymentSpec {
		return remoteDeploymentSpec("postgres", apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
			Ingress: []*apigen.Ingress{{
				Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
				Hostname: "db.example.com",
				TlsPassthroughConfig: &apigen.TlsPassthroughConfig{
					ContainerPort: 5432,
				},
			}},
		})
	}
	_, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: 1, Name: "primary-database"},
		NodeID:   primary.ID,
		Spec:     spec(),
	})
	if err == nil || !strings.Contains(err.Error(), "reserved for the primary Web UI") {
		t.Fatalf("err = %v, want primary :443 reservation", err)
	}
	_, err = h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: 1, Name: "worker-database"},
		NodeID:   worker.ID,
		Spec:     spec(),
	})
	if err != nil {
		t.Fatalf("worker :443 ingress was rejected: %v", err)
	}
}

func TestDeploymentCreateRejectsInternalIdentity(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	h := &Handler{ConfigService: &config.Service{}, Store: store}

	_, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: state.OpendeploySpaceID, Name: "opendeploy-net"},
		NodeID:   primary.ID,
		Spec:     remoteDeploymentSpec("nginx", hostNetworking()),
	})
	if err == nil || !strings.Contains(err.Error(), "internal-only") {
		t.Fatalf("err = %v, want internal identity rejection", err)
	}
}

func TestDeploymentIdentityIsScopedByNodeID(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	nodeA := store.EnsurePrimaryNode("node-a", "node-a-id")
	nodeB := store.EnsurePrimaryNode("node-b", "node-b-id")
	h := &Handler{ConfigService: &config.Service{}, Store: store}
	spec := remoteDeploymentSpec("nginx", hostNetworking())
	create := func(nodeID, spaceID int32) (*apigen.DeploymentConfig, error) {
		return h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			Identity: apigen.DeploymentIdentity{SpaceID: spaceID, Name: "web"},
			NodeID:   nodeID,
			Spec:     spec,
		})
	}

	_, err := create(nodeA.ID, 1)
	if err != nil {
		t.Fatalf("create first deployment: %v", err)
	}
	if _, err := create(nodeA.ID, 1); err != DuplicateDeploymentErr {
		t.Fatalf("same-node duplicate err = %v, want %v", err, DuplicateDeploymentErr)
	}
	if _, err := create(nodeB.ID, 1); err != nil {
		t.Fatalf("same identity on another node: %v", err)
	}
	extraSpace, err := store.CreateSpace("other")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	otherSpace, err := create(nodeA.ID, extraSpace.ID)
	if err != nil {
		t.Fatalf("create deployment in another space: %v", err)
	}
	spaceID := int32(1)
	if _, err := h.PostV1DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: otherSpace.ID,
		Version:      otherSpace.Version + 1,
		SpaceID:      &spaceID,
	}); err != DuplicateDeploymentErr {
		t.Fatalf("space move duplicate err = %v, want %v", err, DuplicateDeploymentErr)
	}
}

func TestDeploymentUpdatePreservesLegacyHostNetworking(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	initial := remoteDeploymentSpec("nginx", hostNetworking())
	created := createTestDeployment(store, "primary", apigen.DeploymentIdentity{SpaceID: 1, Name: "web"}, &initial)
	h := &Handler{ConfigService: &config.Service{}, Store: store}

	_, err := h.PostV1DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: created.ID,
		Version:      created.Version + 1,
		Spec:         remoteDeploymentSpec("nginx:1.26", hostNetworking()),
	})
	if err != nil {
		t.Fatalf("PostV1DeploymentsUpdate failed: %v", err)
	}
	updated := h.findConfigByID(created.ID)
	if updated.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_HOST {
		t.Fatalf("networking mode = %v, want preserved host", updated.Spec.Networking.Mode)
	}
}

func TestDeploymentUpdatePreservesExistingVirtualNetworking(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	initial := remoteDeploymentSpec("nginx", virtualNetworking())
	created := createTestDeployment(store, "primary", apigen.DeploymentIdentity{SpaceID: 1, Name: "web"}, &initial)
	h := &Handler{ConfigService: &config.Service{}, Store: store}

	_, err := h.PostV1DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: created.ID,
		Version:      created.Version + 1,
		Spec:         remoteDeploymentSpec("nginx:1.26", virtualNetworking()),
	})
	if err != nil {
		t.Fatalf("PostV1DeploymentsUpdate failed: %v", err)
	}
	updated := h.findConfigByID(created.ID)
	if updated.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
		t.Fatalf("networking mode = %v, want preserved virtual", updated.Spec.Networking.Mode)
	}
}

func TestDeploymentUpdateAcceptsCrossDeploymentMount(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	secretsManager, err := secrets.Initialize(t.TempDir(), store)
	if err != nil {
		t.Fatalf("secrets.Initialize failed: %v", err)
	}
	sourceSpec := remoteDeploymentSpec("postgres", hostNetworking())
	source := createTestDeployment(store, "primary", apigen.DeploymentIdentity{SpaceID: 1, Name: "database"}, &sourceSpec)
	targetSpec := remoteDeploymentSpec("nginx", hostNetworking())
	targetSpec.Container1Spec.Runtime.CrossDeploymentMounts = []*apigen.CrossDeploymentMount{{
		DeploymentID: source.ID, ContainerPath: "/var/lib/postgresql/data", Permission: apigen.FilePermission_READ_WRITE,
	}}
	target := createTestDeployment(store, "primary", apigen.DeploymentIdentity{SpaceID: 1, Name: "web"}, &targetSpec)
	h := &Handler{ConfigService: &config.Service{}, Store: store, Secrets: secretsManager}

	spec := target.Spec
	spec.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{
		"LOG_LEVEL": {Value: ptrString("debug")},
	}
	_, err = h.PostV1DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: target.ID,
		Version:      target.Version + 1,
		Spec:         spec,
	})
	if err != nil {
		t.Fatalf("PostV1DeploymentsUpdate failed: %v", err)
	}
	updated := h.findConfigByID(target.ID)
	if got := updated.Spec.Container1Spec.Runtime.EnvVars["LOG_LEVEL"].Value; got == nil || *got != "debug" {
		t.Fatalf("LOG_LEVEL = %+v, want debug", updated.Spec.Container1Spec.Runtime.EnvVars["LOG_LEVEL"])
	}
}

func TestDeploymentDeleteRequiresStoppedDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	initial := remoteDeploymentSpec("nginx", hostNetworking())
	initial.Container1Spec.Version = "1.25"
	created := createTestDeployment(store, "primary", apigen.DeploymentIdentity{SpaceID: 1, Name: "web"}, &initial)
	seedDeploymentRunnerStatus(store, created, apigen.RunningStatus_RUNNING)
	h := &Handler{ConfigService: &config.Service{}, Store: store, NodeID: created.NodeID}

	_, err := h.PostV1DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID:  created.ID,
		Version:       created.Version + 1,
		TargetVersion: "1.26",
	})
	if err != nil {
		t.Fatalf("PostV1DeploymentsUpdate setup failed: %v", err)
	}
	cfg := h.findConfigByID(created.ID)
	if cfg == nil {
		t.Fatal("deployment not found")
	}
	err = h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: created.ID, Version: cfg.Version + 1})
	if err == nil || !strings.Contains(err.Error(), "must be stopped") {
		t.Fatalf("err = %v, want stopped-only rejection", err)
	}
}

func TestDeploymentDeleteAllowsNeverScheduledStoppedDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := store.EnsurePrimaryNode("primary", "primary")
	initial := remoteDeploymentSpec("nginx", hostNetworking())
	initial.Container1Spec.Version = "1.25"
	initial.Container1Spec.Running = false
	created := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{SpaceID: 1, Name: "web"}, node.ID, &initial)
	h := &Handler{ConfigService: &config.Service{}, Store: store, NodeID: node.ID}

	if err := h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: created.ID, Version: created.Version + 1}); err != nil {
		t.Fatalf("PostV1DeploymentsDelete failed: %v", err)
	}
	if cfg := h.findConfigByID(created.ID); cfg != nil {
		t.Fatalf("deleted stopped deployment still active: %+v", cfg)
	}
}

func TestDeploymentDeleteAllowsRunningDisconnectedNodeDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	worker := store.EnsurePrimaryNode("worker", "worker-a")
	initial := remoteDeploymentSpec("nginx", hostNetworking())
	initial.Container1Spec.Version = "1.25"
	initial.Container1Spec.Running = true
	created := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{SpaceID: 1, Name: "web"}, worker.ID, &initial)
	seedDeploymentRunnerStatus(store, created, apigen.RunningStatus_RUNNING)
	h := &Handler{ConfigService: &config.Service{}, Store: store, NodeID: primary.ID}

	err := h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: created.ID, Version: created.Version + 1})
	if err != nil {
		t.Fatalf("PostV1DeploymentsDelete failed: %v", err)
	}
	if cfg := h.findConfigByID(created.ID); cfg != nil {
		t.Fatalf("deleted deployment still active: %+v", cfg)
	}
}

func TestDeploymentDeleteAllowsStaleDisconnectedSystemDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	worker := store.EnsurePrimaryNode("worker", "worker-a")
	store.EnsureSystemDeployment(worker.ID, "v0.0.194")
	system := findSystemDeployment(t, store, worker.ID)
	seedDeploymentRunnerStatus(store, system, apigen.RunningStatus_CRASHED)
	h := &Handler{ConfigService: &config.Service{},
		Store:   store,
		NodeID:  primary.ID,
		Cluster: clusterhandler.New(store, nil, nil, nil, network.Prefix{}, nil, nil, nil),
	}

	err := h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: system.ID, Version: system.Version + 1})
	if err != nil {
		t.Fatalf("PostV1DeploymentsDelete failed: %v", err)
	}
	if cfg := h.findConfigByID(system.ID); cfg != nil {
		t.Fatalf("deleted system deployment still active: %+v", cfg)
	}
}

func TestDeploymentDeleteRejectsPrimarySystemDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	store.EnsureSystemDeployment(primary.ID, "v0.0.194")
	system := findSystemDeployment(t, store, primary.ID)
	seedDeploymentRunnerStatus(store, system, apigen.RunningStatus_STOPPED)
	h := &Handler{ConfigService: &config.Service{},
		Store:   store,
		NodeID:  primary.ID,
		Cluster: clusterhandler.New(store, nil, nil, nil, network.Prefix{}, nil, nil, nil),
	}

	err := h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: system.ID, Version: system.Version + 1})
	if err == nil || !strings.Contains(err.Error(), "internal-only") {
		t.Fatalf("err = %v, want internal-only rejection", err)
	}
}

// Mid-rollover the newest instance can report STOPPED while an older one is
// still serving. Deletion must consider every live assignment, not just the
// newest.
func TestDeploymentDeleteRejectedWhileOlderRolloverInstanceRuns(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	initial := remoteDeploymentSpec("nginx", hostNetworking())
	initial.Container1Spec.Version = "1.25"
	created := createTestDeployment(store, "primary", apigen.DeploymentIdentity{SpaceID: 1, Name: "web"}, &initial)
	seedInstanceRunnerStatus(store, created.ID, created.Version, created.NodeID, apigen.RunningStatus_RUNNING)

	next := remoteDeploymentSpec("nginx", hostNetworking())
	next.Container1Spec.Version = "1.27"
	updated, _, versionOK := store.UpdateDeploymentConfig(apigen.Context{}, created.ID, state.DeploymentConfigUpdate{
		ExpectedVersion: created.Version + 1,
		SpaceID:         created.Identity.SpaceID,
		Spec:            &next,
	})
	if !versionOK {
		t.Fatal("expected config update to succeed")
	}
	seedInstanceRunnerStatus(store, updated.ID, updated.Version, updated.NodeID, apigen.RunningStatus_STOPPED)

	h := &Handler{ConfigService: &config.Service{}, Store: store, NodeID: created.NodeID}
	err := h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{
		DeploymentID: created.ID,
		Version:      updated.Version + 1,
	})
	if err == nil || !strings.Contains(err.Error(), "must be stopped") {
		t.Fatalf("err = %v, want deletion rejected while an older instance is running", err)
	}

	// Once the older instance stops too, deletion is allowed again.
	for _, state := range store.FetchScheduledSnapshot(nil) {
		if state.Instance.DeploymentVersion != created.Version {
			continue
		}
		store.MustWriteScheduledInstanceStatus(state.Instance.ID, func(s *apigen.ScheduledInstanceStatus) bool {
			s.BumpUpdatedAt()
			s.Runner.Status = apigen.RunningStatus_STOPPED
			return true
		})
	}
	if err := h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{
		DeploymentID: created.ID,
		Version:      updated.Version + 1,
	}); err != nil {
		t.Fatalf("PostV1DeploymentsDelete = %v, want success once all instances are stopped", err)
	}
}

func TestDeploymentDeleteSoftDeletesStoppedDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	initial := remoteDeploymentSpec("nginx", hostNetworking())
	initial.Container1Spec.Version = "1.25"
	created := createTestDeployment(store, "primary", apigen.DeploymentIdentity{SpaceID: 1, Name: "web"}, &initial)
	seedDeploymentRunnerStatus(store, created, apigen.RunningStatus_STOPPED)
	h := &Handler{ConfigService: &config.Service{}, Store: store}

	err := h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: created.ID, Version: created.Version + 1})
	if err != nil {
		t.Fatalf("PostV1DeploymentsDelete failed: %v", err)
	}
	if cfg := h.findConfigByID(created.ID); cfg != nil {
		t.Fatalf("deleted deployment still active: %+v", cfg)
	}
	history := store.MustFetchDeploymentHistory(created.ID)
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}
	deleted := history[len(history)-1]
	if !deleted.Deleted || deleted.WorkloadRunning() || deleted.WorkloadVersion() != "1.25" {
		t.Fatalf("deleted history entry = %+v", deleted)
	}
}

func TestDeploymentCreateWithDeletedIdentityCreatesIndependentDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := store.EnsurePrimaryNode("primary", "primary")
	h := &Handler{ConfigService: &config.Service{}, Store: store}
	create := func(version string) *apigen.DeploymentConfig {
		t.Helper()
		spec := remoteDeploymentSpec("nginx", hostNetworking())
		spec.Container1Spec.Version = version
		cfg, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			Identity: apigen.DeploymentIdentity{SpaceID: 1, Name: "web"},
			NodeID:   primary.ID,
			Spec:     spec,
		})
		if err != nil {
			t.Fatalf("PostV1DeploymentsCreate: %v", err)
		}
		return cfg
	}

	first := create("1.25")
	seedDeploymentRunnerStatus(store, first, apigen.RunningStatus_STOPPED)
	if err := h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{
		DeploymentID: first.ID,
		Version:      first.Version + 1,
	}); err != nil {
		t.Fatalf("PostV1DeploymentsDelete: %v", err)
	}

	second := create("1.26")
	if second.ID == first.ID {
		t.Fatalf("new deployment reused deleted deployment ID %d", first.ID)
	}
	if second.Version != 1 {
		t.Fatalf("new deployment version = %d, want 1", second.Version)
	}
	if second.WorkloadVersion() != "1.26" {
		t.Fatalf("new deployment workload state = %+v", second.Spec.Container1Spec)
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

func TestDeploymentUpdateCombinesSpaceAndWorkloadStateInSingleConfigVersion(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	initial := remoteDeploymentSpec("nginx", hostNetworking())
	created := createTestDeployment(store, "primary", apigen.DeploymentIdentity{SpaceID: 1, Name: "web"}, &initial)
	h := &Handler{ConfigService: &config.Service{}, Store: store}

	target, err := store.CreateSpace("other")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	spaceID := target.ID
	_, err = h.PostV1DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID:  created.ID,
		Version:       created.Version + 1,
		SpaceID:       &spaceID,
		TargetVersion: "1.25",
	})
	if err != nil {
		t.Fatalf("PostV1DeploymentsUpdate failed: %v", err)
	}

	cfg := h.findConfigByID(created.ID)
	if cfg == nil {
		t.Fatal("updated deployment not found")
	}
	if cfg.Version != created.Version+1 {
		t.Fatalf("version = %d, want %d", cfg.Version, created.Version+1)
	}
	if cfg.Identity.SpaceID != spaceID {
		t.Fatalf("space = %d, want %d", cfg.Identity.SpaceID, spaceID)
	}
	if cfg.WorkloadVersion() != "1.25" || !cfg.WorkloadRunning() {
		t.Fatalf("workload state = %+v, want running 1.25", cfg.Spec.Container1Spec)
	}
	if history := store.MustFetchDeploymentHistory(created.ID); len(history) != 2 {
		t.Fatalf("history len = %d, want create + one combined update", len(history))
	}
}

func TestDeploymentUpdateRejectsStaleExpectedVersion(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	initial := remoteDeploymentSpec("nginx", hostNetworking())
	created := createTestDeployment(store, "primary", apigen.DeploymentIdentity{SpaceID: 1, Name: "web"}, &initial)
	h := &Handler{ConfigService: &config.Service{}, Store: store}

	_, err := h.PostV1DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
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
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	initial := remoteDeploymentSpec("nginx", hostNetworking())
	created := createTestDeployment(store, "primary", apigen.DeploymentIdentity{SpaceID: 1, Name: "web"}, &initial)
	h := &Handler{ConfigService: &config.Service{}, Store: store}

	_, err := h.PostV1DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
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
