package webuihandler

import (
	"context"
	"errors"
	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/scheduledinstances"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/clusterhandler"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/engine/versionprovider"
	"github.com/jptrs93/opsagent/backend/lib/network"
	githubrepo "github.com/jptrs93/opsagent/backend/lib/repo/github"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

const testNixCommit = "0123456789abcdef0123456789abcdef01234567"
const testNixCommit2 = "89abcdef0123456789abcdef0123456789abcdef"

func findSystemDeployment(t *testing.T, store *state.Service, nodeID int32) *apigen.DeploymentEvent {
	t.Helper()
	for _, cfg := range erru.Must(store.Queries().ListActiveDeployments(context.Background())) {
		if internaldeploy.IsSelfConfig(cfg) && cfg.Value.NodeID == nodeID {
			return cfg
		}
	}
	t.Fatalf("system deployment for node %d not found", nodeID)
	return nil
}

func seedInstanceRunnerStatus(store *state.Service, deploymentID, version, nodeID int32, status apigen.RunningStatus) {
	inst := statetest.CreateScheduledInstance(store, deploymentID, version, nodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	scheduledinstances.WriteStatus(store, inst.ID, func(s *apigen.ScheduledInstanceStatus) bool {
		s.BumpUpdatedAt()
		s.Runner.Status = status
		return true
	})
}

func seedDeploymentRunnerStatus(store *state.Service, cfg *apigen.DeploymentEvent, status apigen.RunningStatus) {
	seedInstanceRunnerStatus(store, cfg.DeploymentID, cfg.SpecVersion, cfg.Value.NodeID, status)
}

func createTestDeployment(store *state.Service, nodeIdentifier string, spaceID int32, name string, spec *apigen.DeploymentSpec) *apigen.DeploymentEvent {
	node := nodes.EnsurePrimaryNode(store, nodeIdentifier, nodeIdentifier)
	return statetest.MustCreateDeploymentForNode(store, apigen.Context{}, spaceID, name, node.ID, spec)
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

func TestDeploymentCreateEnforcesRunningNixSource(t *testing.T) {
	t.Run("running verifies before persistence", func(t *testing.T) {
		store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
		node := nodes.EnsurePrimaryNode(store, "primary", "primary")
		provider := &fakeGitSourceProvider{sourceCommitValid: true}
		h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), GitVersions: provider}

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
		node := nodes.EnsurePrimaryNode(store, "primary", "primary")
		provider := &fakeGitSourceProvider{sourceErr: errors.New("remote unavailable")}
		h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), GitVersions: provider}

		_, err := h.PostV1DeploymentsCreate(apigen.Context{Ctx: context.Background()}, nixCreateRequest(node.ID, "web", true))
		if err == nil {
			t.Fatal("expected source verification failure")
		}
		if got := len(erru.Must(store.Queries().ListActiveDeployments(context.Background()))); got != 0 {
			t.Fatalf("persisted deployments = %d, want 0", got)
		}
	})

	t.Run("stopped skips provider", func(t *testing.T) {
		store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
		node := nodes.EnsurePrimaryNode(store, "primary", "primary")
		provider := &fakeGitSourceProvider{sourceErr: errors.New("must not be called")}
		h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), GitVersions: provider}

		cfg, err := h.PostV1DeploymentsCreate(apigen.Context{Ctx: context.Background()}, nixCreateRequest(node.ID, "web", false))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.WorkloadRunning() || len(provider.validateCalls) != 0 {
			t.Fatalf("config/provider calls = %+v/%v", cfg.Value.Spec.Container1Spec, provider.validateCalls)
		}
	})

	t.Run("stopped still requires immutable version syntax", func(t *testing.T) {
		store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
		node := nodes.EnsurePrimaryNode(store, "primary", "primary")
		provider := &fakeGitSourceProvider{}
		h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), GitVersions: provider}
		req := nixCreateRequest(node.ID, "web", false)
		req.Spec.Container1Spec.Version = "main"

		if _, err := h.PostV1DeploymentsCreate(apigen.Context{Ctx: context.Background()}, req); err == nil {
			t.Fatal("expected mutable version rejection")
		}
		if len(provider.validateCalls) != 0 || len(erru.Must(store.Queries().ListActiveDeployments(context.Background()))) != 0 {
			t.Fatalf("provider calls/deployments = %v/%d", provider.validateCalls, len(erru.Must(store.Queries().ListActiveDeployments(context.Background()))))
		}
	})

	t.Run("stopped permits an empty desired version", func(t *testing.T) {
		store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
		node := nodes.EnsurePrimaryNode(store, "primary", "primary")
		provider := &fakeGitSourceProvider{}
		h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), GitVersions: provider}
		req := nixCreateRequest(node.ID, "web", false)
		req.Spec.Container1Spec.Version = ""

		cfg, err := h.PostV1DeploymentsCreate(apigen.Context{Ctx: context.Background()}, req)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.WorkloadVersion() != "" || cfg.WorkloadRunning() {
			t.Fatalf("workload state = %+v, want stopped with no version", cfg.Value.Spec.Container1Spec)
		}
		if len(provider.validateCalls) != 0 || len(erru.Must(store.Queries().ListActiveDeployments(context.Background()))) != 1 {
			t.Fatalf("provider calls/deployments = %v/%d", provider.validateCalls, len(erru.Must(store.Queries().ListActiveDeployments(context.Background()))))
		}
	})
}

func TestDeploymentVersionsUsesCombinedDiscovery(t *testing.T) {
	h, cfg, provider := newNixDeploymentHandler(t, false)
	provider.branches = []string{"main", "trunk"}
	provider.commits = []*apigen.Version{{ID: testNixCommit}}

	versions, err := h.PostV1DeploymentsVersions(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentVersionsRequest{
		DeploymentID: cfg.DeploymentID,
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
		DeploymentID: cfg.DeploymentID,
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
		DeploymentID: cfg.DeploymentID,
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

	client := githubrepo.NewClient(githubrepo.WithAPIBaseURL(server.URL))
	provider := versionprovider.NewGithubReleaseVersionProvider(client)
	tests := []struct {
		name             string
		createDeployment func(*testing.T, *state.Service) *apigen.DeploymentEvent
		provider         *versionprovider.GithubReleaseVersionProvider
		wantInternal     string
	}{
		{
			name: "opendeploy-net special branch",
			createDeployment: func(_ *testing.T, store *state.Service) *apigen.DeploymentEvent {
				node := nodes.EnsurePrimaryNode(store, "primary", "primary")
				return deployments.EnsureNetproxy(store, node.ID, "v1.2.3")
			},
			provider:     provider,
			wantInternal: "listing releases: github api 503: github upstream failure marker",
		},
		{
			name: "GitHub release config branch",
			createDeployment: func(t *testing.T, store *state.Service) *apigen.DeploymentEvent {
				node := nodes.EnsurePrimaryNode(store, "primary", "primary")
				deployments.EnsureSystem(store, node.ID, "v1.2.3")
				return findSystemDeployment(t, store, node.ID)
			},
			provider:     provider,
			wantInternal: "listing releases: github api 503: github upstream failure marker",
		},
		{
			name: "unconfigured provider",
			createDeployment: func(t *testing.T, store *state.Service) *apigen.DeploymentEvent {
				node := nodes.EnsurePrimaryNode(store, "primary", "primary")
				deployments.EnsureSystem(store, node.ID, "v1.2.3")
				return findSystemDeployment(t, store, node.ID)
			},
			wantInternal: "github release version loading is not configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
			nodes.EnsurePrimaryNode(store, "primary", "primary")
			cfg := tt.createDeployment(t, store)
			h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), GithubReleaseVersions: tt.provider}

			_, err := h.PostV1DeploymentsVersions(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentVersionsRequest{DeploymentID: cfg.DeploymentID})
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

func newNixDeploymentHandler(t *testing.T, running bool) (*Handler, *apigen.DeploymentEvent, *fakeGitSourceProvider) {
	t.Helper()
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	provider := &fakeGitSourceProvider{sourceCommitValid: true}
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), GitVersions: provider}
	cfg, err := h.PostV1DeploymentsCreate(apigen.Context{Ctx: context.Background()}, nixCreateRequest(node.ID, "web", running))
	if err != nil {
		t.Fatal(err)
	}
	return h, cfg, provider
}

func TestDeploymentUpdateRejectsSystemDeploymentSpecUpdate(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	deployments.EnsureSystem(store, node.ID, "v0.0.194")
	var system *apigen.DeploymentEvent
	for _, cfg := range erru.Must(store.Queries().ListActiveDeployments(context.Background())) {
		if internaldeploy.IsSelfConfig(cfg) {
			system = cfg
			break
		}
	}
	if system == nil {
		t.Fatal("system deployment not found")
	}

	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries()}
	_, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:    system.DeploymentID,
		ExpectedVersion: system.Version + 1,
		SpecUpdate:      &apigen.SpecUpdate{Spec: remoteDeploymentSpec("nginx", hostNetworking())},
	})
	if err == nil || !strings.Contains(err.Error(), "internal-only") {
		t.Fatalf("err = %v, want internal-only rejection", err)
	}
}

func TestDeploymentAddressEnvRefsValidateAndBlockTargetChanges(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := nodes.EnsurePrimaryNode(store, "primary", "primary")
	secondary := nodes.EnsurePrimaryNode(store, "secondary", "secondary")
	secretsManager, err := secrets.Initialize(t.TempDir(), store)
	if err != nil {
		t.Fatalf("secrets.Initialize: %v", err)
	}
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), Secrets: secretsManager}

	create := func(name string, nodeID int32, networking apigen.NetworkingConfig, env map[string]*apigen.EnvVarValue) *apigen.DeploymentEvent {
		t.Helper()
		spec := remoteDeploymentSpec("nginx", networking)
		spec.Container1Spec.Runtime.EnvVars = env
		cfg, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			SpaceID: 1, Name: name,
			NodeID: nodeID,
			Spec:   spec,
		})
		if err != nil {
			t.Fatalf("PostV1DeploymentsCreate %s: %v", name, err)
		}
		return cfg
	}

	target := create("database", primary.ID, virtualNetworking(), nil)
	addressDeploymentID := target.DeploymentID
	addressSpaceID := int32(1)
	consumer := create("web", primary.ID, hostNetworking(), map[string]*apigen.EnvVarValue{
		"DATABASE_ADDR": {AddressDeploymentID: &addressDeploymentID, AddressSpaceID: &addressSpaceID},
	})
	if got := consumer.Value.Spec.Container1Spec.Runtime.EnvVars["DATABASE_ADDR"]; got.AddressDeploymentID == nil || got.AddressSpaceID == nil {
		t.Fatalf("address ref was not stored: %+v", got)
	}

	wrongSpace := int32(2)
	_, err = h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: 1, Name: "wrong-space",
		NodeID: primary.ID,
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

	remote := create("remote", secondary.ID, virtualNetworking(), nil)
	remoteID := remote.DeploymentID
	crossNode, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: 1, Name: "cross-node",
		NodeID: primary.ID,
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
	if got := crossNode.Value.Spec.Container1Spec.Runtime.EnvVars["REMOTE_ADDR"]; got.AddressDeploymentID == nil || *got.AddressDeploymentID != remoteID {
		t.Fatalf("cross-node address ref = %+v", got)
	}

	nextSpace, err := nodes.CreateSpace(store, "other")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	_, err = h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:        target.DeploymentID,
		ExpectedVersion:     target.Version + 1,
		AssignedSpaceUpdate: &apigen.AssignedSpaceUpdate{SpaceID: nextSpace.ID},
	})
	if err != deployments.AddressReferencedErr {
		t.Fatalf("err = %v, want %v", err, deployments.AddressReferencedErr)
	}

	_, err = h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:    target.DeploymentID,
		ExpectedVersion: target.Version + 1,
		SpecUpdate:      &apigen.SpecUpdate{Spec: remoteDeploymentSpec("nginx", hostNetworking())},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot leave virtual mode") {
		t.Fatalf("err = %v, want virtual-networking removal rejection", err)
	}

	seedDeploymentRunnerStatus(store, target, apigen.RunningStatus_STOPPED)
	err = h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: target.DeploymentID, Version: target.Version + 1})
	if err == nil || !strings.Contains(err.Error(), "reference_in_use") {
		t.Fatalf("err = %v, want referenced deployment deletion rejection", err)
	}
}

func TestDeploymentCreatePersistsInitialStoppedWorkloadState(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := nodes.EnsurePrimaryNode(store, "primary", "primary")
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries()}

	cfg, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: 1, Name: "web",
		NodeID: primary.ID,
		Spec: func() apigen.DeploymentSpec {
			spec := remoteDeploymentSpec("nginx", hostNetworking())
			spec.Container1Spec.Version = "1.25"
			return spec
		}(),
	})
	if err != nil {
		t.Fatalf("PostV1DeploymentsCreate failed: %v", err)
	}
	if cfg.SpecVersion != 1 {
		t.Fatalf("version = %d, want initial spec version 1", cfg.SpecVersion)
	}
	if cfg.WorkloadVersion() != "1.25" || cfg.WorkloadRunning() {
		t.Fatalf("workload state = %+v, want stopped 1.25", cfg.Value.Spec.Container1Spec)
	}
	if history := erru.Must(store.Queries().ListDeploymentEvents(context.Background(), int64(cfg.DeploymentID))); len(history) != 1 {
		t.Fatalf("history len = %d, want create only", len(history))
	} else if history[0].WorkloadVersion() != "1.25" || history[0].WorkloadRunning() {
		t.Fatalf("history workload state = %+v, want stopped 1.25", history[0].Value.Spec.Container1Spec)
	}
}

func TestDeploymentCreateRejectsIngressClaimsAlreadyUsedOnNode(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := nodes.EnsurePrimaryNode(store, "primary", "primary")
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), NodeID: primary.ID}
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
		SpaceID: 1, Name: "database",
		NodeID: primary.ID,
		Spec:   remoteDeploymentSpec("postgres", ingress("db.example.com")),
	})
	if err != nil {
		t.Fatalf("creating first ingress deployment: %v", err)
	}
	_, err = h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: 1, Name: "database-copy",
		NodeID: primary.ID,
		Spec:   remoteDeploymentSpec("postgres", ingress("DB.EXAMPLE.COM")),
	})
	if err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("err = %v, want duplicate ingress claim rejection", err)
	}
	_, err = h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: 1, Name: "direct",
		NodeID: primary.ID,
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

func TestDeploymentCreateAllowsPrimaryIngressAndRejectsOverlap(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := nodes.EnsurePrimaryNode(store, "primary", "primary")
	nodes.ReportNode(store, "primary", apigen.NodeReported{Identifier: "primary", HostAddresses: []string{"203.0.113.10"}})
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), NodeID: primary.ID}
	spec := func(listen ...*apigen.IngressListen) apigen.DeploymentSpec {
		return remoteDeploymentSpec("postgres", apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
			Ingress: []*apigen.Ingress{{
				Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
				Hostname: "db.example.com",
				TlsPassthroughConfig: &apigen.TlsPassthroughConfig{
					ContainerPort: 5432,
				},
				Listen: listen,
			}},
		})
	}
	// The primary no longer reserves :443 by fiat; the Web UI listener is a
	// reserved claim evaluated like any other (see ingress_listen_validation_test).
	if _, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: 1, Name: "primary-database", NodeID: primary.ID, Spec: spec(),
	}); err != nil {
		t.Fatalf("primary :443 ingress was rejected: %v", err)
	}
	_, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: 1, Name: "primary-database-2", NodeID: primary.ID,
		Spec: spec(&apigen.IngressListen{Address: &apigen.AddressSelector{Prefixes: []string{"203.0.113.10"}}}),
	})
	if err == nil || !strings.Contains(err.Error(), "already claimed by another deployment") {
		t.Fatalf("err = %v, want overlapping listen rejection", err)
	}
	_, err = h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: 1, Name: "primary-database-3", NodeID: primary.ID,
		Spec: spec(&apigen.IngressListen{Address: &apigen.AddressSelector{Prefixes: []string{"not an address"}}}),
	})
	if err == nil || !strings.Contains(err.Error(), "not an IP address or CIDR prefix") {
		t.Fatalf("err = %v, want listen literal rejection", err)
	}
}

func TestDeploymentCreateRejectsInternalIdentity(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := nodes.EnsurePrimaryNode(store, "primary", "primary")
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries()}

	_, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: internaldeploy.SpaceID, Name: "opendeploy-net",
		NodeID: primary.ID,
		Spec:   remoteDeploymentSpec("nginx", hostNetworking()),
	})
	if err == nil || !strings.Contains(err.Error(), "internal-only") {
		t.Fatalf("err = %v, want internal identity rejection", err)
	}
}

func TestDeploymentIdentityIsScopedByNodeID(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	nodeA := nodes.EnsurePrimaryNode(store, "node-a", "node-a-id")
	nodeB := nodes.EnsurePrimaryNode(store, "node-b", "node-b-id")
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries()}
	spec := remoteDeploymentSpec("nginx", hostNetworking())
	create := func(nodeID, spaceID int32) (*apigen.DeploymentEvent, error) {
		return h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			SpaceID: spaceID, Name: "web",
			NodeID: nodeID,
			Spec:   spec,
		})
	}

	_, err := create(nodeA.ID, 1)
	if err != nil {
		t.Fatalf("create first deployment: %v", err)
	}
	if _, err := create(nodeA.ID, 1); err != deployments.DuplicateErr {
		t.Fatalf("same-node duplicate err = %v, want %v", err, deployments.DuplicateErr)
	}
	if _, err := create(nodeB.ID, 1); err != nil {
		t.Fatalf("same identity on another node: %v", err)
	}
	extraSpace, err := nodes.CreateSpace(store, "other")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	otherSpace, err := create(nodeA.ID, extraSpace.ID)
	if err != nil {
		t.Fatalf("create deployment in another space: %v", err)
	}
	if _, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:        otherSpace.DeploymentID,
		ExpectedVersion:     otherSpace.Version + 1,
		AssignedSpaceUpdate: &apigen.AssignedSpaceUpdate{SpaceID: 1},
	}); err != deployments.DuplicateErr {
		t.Fatalf("space move err = %v, want %v", err, deployments.DuplicateErr)
	}
}

func TestDeploymentUpdatePreservesHostNetworking(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	initial := remoteDeploymentSpec("nginx", hostNetworking())
	created := createTestDeployment(store, "primary", 1, "web", &initial)
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries()}

	_, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:    created.DeploymentID,
		ExpectedVersion: created.Version + 1,
		SpecUpdate:      &apigen.SpecUpdate{Spec: remoteDeploymentSpec("nginx:1.26", hostNetworking())},
	})
	if err != nil {
		t.Fatalf("PostV2DeploymentsUpdate failed: %v", err)
	}
	updated := h.findConfigByID(created.DeploymentID)
	if updated.Value.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_HOST {
		t.Fatalf("networking mode = %v, want preserved host", updated.Value.Spec.Networking.Mode)
	}
}

func TestDeploymentUpdatePreservesExistingVirtualNetworking(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	initial := remoteDeploymentSpec("nginx", virtualNetworking())
	created := createTestDeployment(store, "primary", 1, "web", &initial)
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries()}

	_, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:    created.DeploymentID,
		ExpectedVersion: created.Version + 1,
		SpecUpdate:      &apigen.SpecUpdate{Spec: remoteDeploymentSpec("nginx:1.26", virtualNetworking())},
	})
	if err != nil {
		t.Fatalf("PostV2DeploymentsUpdate failed: %v", err)
	}
	updated := h.findConfigByID(created.DeploymentID)
	if updated.Value.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
		t.Fatalf("networking mode = %v, want preserved virtual", updated.Value.Spec.Networking.Mode)
	}
}

func TestDeploymentUpdateAcceptsCrossDeploymentMount(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	secretsManager, err := secrets.Initialize(t.TempDir(), store)
	if err != nil {
		t.Fatalf("secrets.Initialize failed: %v", err)
	}
	sourceSpec := remoteDeploymentSpec("postgres", hostNetworking())
	source := createTestDeployment(store, "primary", 1, "database", &sourceSpec)
	targetSpec := remoteDeploymentSpec("nginx", hostNetworking())
	targetSpec.Container1Spec.Runtime.CrossDeploymentMounts = []*apigen.CrossDeploymentMount{{
		DeploymentID: source.DeploymentID, ContainerPath: "/var/lib/postgresql/data", Permission: apigen.FilePermission_READ_WRITE,
	}}
	target := createTestDeployment(store, "primary", 1, "web", &targetSpec)
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), Secrets: secretsManager}

	spec := target.Value.Spec
	spec.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{
		"LOG_LEVEL": {Value: ptrString("debug")},
	}
	_, err = h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:    target.DeploymentID,
		ExpectedVersion: target.Version + 1,
		SpecUpdate:      &apigen.SpecUpdate{Spec: spec},
	})
	if err != nil {
		t.Fatalf("PostV2DeploymentsUpdate failed: %v", err)
	}
	updated := h.findConfigByID(target.DeploymentID)
	if got := updated.Value.Spec.Container1Spec.Runtime.EnvVars["LOG_LEVEL"].Value; got == nil || *got != "debug" {
		t.Fatalf("LOG_LEVEL = %+v, want debug", updated.Value.Spec.Container1Spec.Runtime.EnvVars["LOG_LEVEL"])
	}
}

func TestDeploymentDeleteRequiresStoppedDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	initial := remoteDeploymentSpec("nginx", hostNetworking())
	initial.Container1Spec.Version = "1.25"
	created := createTestDeployment(store, "primary", 1, "web", &initial)
	seedDeploymentRunnerStatus(store, created, apigen.RunningStatus_RUNNING)
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), NodeID: created.Value.NodeID}

	_, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:      created.DeploymentID,
		ExpectedVersion:   created.Version + 1,
		VersionOnlyUpdate: &apigen.VersionOnlyUpdate{TargetVersion: "1.26"},
	})
	if err != nil {
		t.Fatalf("PostV2DeploymentsUpdate setup failed: %v", err)
	}
	cfg := h.findConfigByID(created.DeploymentID)
	if cfg == nil {
		t.Fatal("deployment not found")
	}
	err = h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: created.DeploymentID, Version: cfg.Version + 1})
	if err == nil || !strings.Contains(err.Error(), "must be stopped") {
		t.Fatalf("err = %v, want stopped-only rejection", err)
	}
}

func TestDeploymentDeleteAllowsNeverScheduledStoppedDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	initial := remoteDeploymentSpec("nginx", hostNetworking())
	initial.Container1Spec.Version = "1.25"
	initial.Container1Spec.Running = false
	created := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "web", node.ID, &initial)
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), NodeID: node.ID}

	if err := h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: created.DeploymentID, Version: created.Version + 1}); err != nil {
		t.Fatalf("PostV1DeploymentsDelete failed: %v", err)
	}
	if cfg := h.findConfigByID(created.DeploymentID); cfg != nil {
		t.Fatalf("deleted stopped deployment still active: %+v", cfg)
	}
}

func TestDeploymentDeleteAllowsRunningDisconnectedNodeDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := nodes.EnsurePrimaryNode(store, "primary", "primary")
	secondary := nodes.EnsurePrimaryNode(store, "secondary", "secondary-a")
	initial := remoteDeploymentSpec("nginx", hostNetworking())
	initial.Container1Spec.Version = "1.25"
	initial.Container1Spec.Running = true
	created := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "web", secondary.ID, &initial)
	seedDeploymentRunnerStatus(store, created, apigen.RunningStatus_RUNNING)
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), NodeID: primary.ID}

	err := h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: created.DeploymentID, Version: created.Version + 1})
	if err != nil {
		t.Fatalf("PostV1DeploymentsDelete failed: %v", err)
	}
	if cfg := h.findConfigByID(created.DeploymentID); cfg != nil {
		t.Fatalf("deleted deployment still active: %+v", cfg)
	}
}

func TestDeploymentDeleteAllowsStaleDisconnectedSystemDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := nodes.EnsurePrimaryNode(store, "primary", "primary")
	secondary := nodes.EnsurePrimaryNode(store, "secondary", "secondary-a")
	deployments.EnsureSystem(store, secondary.ID, "v0.0.194")
	system := findSystemDeployment(t, store, secondary.ID)
	seedDeploymentRunnerStatus(store, system, apigen.RunningStatus_CRASHED)
	h := &Handler{SystemConfig: &systemconfig.Service{},
		Store: store, Queries: store.Queries(),
		NodeID:  primary.ID,
		Cluster: clusterhandler.New(store, nil, nil, nil, network.Prefix{}, nil, nil, nil, nil),
	}

	err := h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: system.DeploymentID, Version: system.Version + 1})
	if err != nil {
		t.Fatalf("PostV1DeploymentsDelete failed: %v", err)
	}
	if cfg := h.findConfigByID(system.DeploymentID); cfg != nil {
		t.Fatalf("deleted system deployment still active: %+v", cfg)
	}
}

func TestDeploymentDeleteRejectsPrimarySystemDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := nodes.EnsurePrimaryNode(store, "primary", "primary")
	deployments.EnsureSystem(store, primary.ID, "v0.0.194")
	system := findSystemDeployment(t, store, primary.ID)
	seedDeploymentRunnerStatus(store, system, apigen.RunningStatus_STOPPED)
	h := &Handler{SystemConfig: &systemconfig.Service{},
		Store: store, Queries: store.Queries(),
		NodeID:  primary.ID,
		Cluster: clusterhandler.New(store, nil, nil, nil, network.Prefix{}, nil, nil, nil, nil),
	}

	err := h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: system.DeploymentID, Version: system.Version + 1})
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
	created := createTestDeployment(store, "primary", 1, "web", &initial)
	seedInstanceRunnerStatus(store, created.DeploymentID, created.SpecVersion, created.Value.NodeID, apigen.RunningStatus_RUNNING)

	next := remoteDeploymentSpec("nginx", hostNetworking())
	next.Container1Spec.Version = "1.27"
	updated := statetest.UpdateDeploymentSpec(store, apigen.Context{}, created.DeploymentID, &next)
	seedInstanceRunnerStatus(store, updated.DeploymentID, updated.SpecVersion, updated.Value.NodeID, apigen.RunningStatus_STOPPED)

	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), NodeID: created.Value.NodeID}
	err := h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{
		DeploymentID: created.DeploymentID,
		Version:      updated.Version + 1,
	})
	if err == nil || !strings.Contains(err.Error(), "must be stopped") {
		t.Fatalf("err = %v, want deletion rejected while an older instance is running", err)
	}

	// Once the older instance stops too, deletion is allowed again.
	for _, state := range store.FetchScheduledSnapshot(nil) {
		if state.Instance.DeploymentSpecVersion != created.SpecVersion {
			continue
		}
		scheduledinstances.WriteStatus(store, state.Instance.ID, func(s *apigen.ScheduledInstanceStatus) bool {
			s.BumpUpdatedAt()
			s.Runner.Status = apigen.RunningStatus_STOPPED
			return true
		})
	}
	if err := h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{
		DeploymentID: created.DeploymentID,
		Version:      updated.Version + 1,
	}); err != nil {
		t.Fatalf("PostV1DeploymentsDelete = %v, want success once all instances are stopped", err)
	}
}

func TestDeploymentDeleteSoftDeletesStoppedDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	initial := remoteDeploymentSpec("nginx", hostNetworking())
	initial.Container1Spec.Version = "1.25"
	created := createTestDeployment(store, "primary", 1, "web", &initial)
	seedDeploymentRunnerStatus(store, created, apigen.RunningStatus_STOPPED)
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries()}

	err := h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{DeploymentID: created.DeploymentID, Version: created.Version + 1})
	if err != nil {
		t.Fatalf("PostV1DeploymentsDelete failed: %v", err)
	}
	if cfg := h.findConfigByID(created.DeploymentID); cfg != nil {
		t.Fatalf("deleted deployment still active: %+v", cfg)
	}
	history := erru.Must(store.Queries().ListDeploymentEvents(context.Background(), int64(created.DeploymentID)))
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}
	deleted := history[len(history)-1]
	if !deleted.Deleted() || deleted.WorkloadRunning() || deleted.WorkloadVersion() != "1.25" {
		t.Fatalf("deleted history entry = %+v", deleted)
	}
}

func TestDeploymentCreateWithDeletedIdentityCreatesIndependentDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	primary := nodes.EnsurePrimaryNode(store, "primary", "primary")
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries()}
	create := func(version string) *apigen.DeploymentEvent {
		t.Helper()
		spec := remoteDeploymentSpec("nginx", hostNetworking())
		spec.Container1Spec.Version = version
		cfg, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			SpaceID: 1, Name: "web",
			NodeID: primary.ID,
			Spec:   spec,
		})
		if err != nil {
			t.Fatalf("PostV1DeploymentsCreate: %v", err)
		}
		return cfg
	}

	first := create("1.25")
	seedDeploymentRunnerStatus(store, first, apigen.RunningStatus_STOPPED)
	if err := h.PostV1DeploymentsDelete(apigen.Context{}, &apigen.DeploymentDeleteRequest{
		DeploymentID: first.DeploymentID,
		Version:      first.Version + 1,
	}); err != nil {
		t.Fatalf("PostV1DeploymentsDelete: %v", err)
	}

	second := create("1.26")
	if second.DeploymentID == first.DeploymentID {
		t.Fatalf("new deployment reused deleted deployment ID %d", first.DeploymentID)
	}
	if second.SpecVersion != 1 {
		t.Fatalf("new deployment version = %d, want 1", second.SpecVersion)
	}
	if second.WorkloadVersion() != "1.26" {
		t.Fatalf("new deployment workload state = %+v", second.Value.Spec.Container1Spec)
	}

	firstHistory := erru.Must(store.Queries().ListDeploymentEvents(context.Background(), int64(first.DeploymentID)))
	if len(firstHistory) != 2 || !firstHistory[len(firstHistory)-1].Deleted() {
		t.Fatalf("deleted deployment history = %+v, want independent two-entry history", firstHistory)
	}
	secondHistory := erru.Must(store.Queries().ListDeploymentEvents(context.Background(), int64(second.DeploymentID)))
	if len(secondHistory) != 1 || secondHistory[0].Deleted() || secondHistory[0].SpecVersion != 1 {
		t.Fatalf("new deployment history = %+v, want independent initial history", secondHistory)
	}
	active := erru.Must(store.Queries().ListActiveDeployments(context.Background()))
	if len(active) != 1 || active[0].DeploymentID != second.DeploymentID {
		t.Fatalf("active deployments = %+v, want only new deployment %d", active, second.DeploymentID)
	}
}

func TestDeploymentUpdateRejectsStaleExpectedVersion(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	initial := remoteDeploymentSpec("nginx", hostNetworking())
	created := createTestDeployment(store, "primary", 1, "web", &initial)
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries()}

	_, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:      created.DeploymentID,
		ExpectedVersion:   created.Version,
		VersionOnlyUpdate: &apigen.VersionOnlyUpdate{TargetVersion: "1.25"},
	})
	apiErr, ok := err.(apigen.ApiErr)
	if !ok || apiErr.Code != http.StatusBadRequest || !strings.Contains(apiErr.Error(), "version mismatch") {
		t.Fatalf("err = %#v, want 400 version mismatch", err)
	}
}

func TestDeploymentUpdateRequiresExpectedVersion(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	initial := remoteDeploymentSpec("nginx", hostNetworking())
	created := createTestDeployment(store, "primary", 1, "web", &initial)
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries()}

	_, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:      created.DeploymentID,
		VersionOnlyUpdate: &apigen.VersionOnlyUpdate{TargetVersion: "1.25"},
	})
	apiErr, ok := err.(apigen.ApiErr)
	if !ok || apiErr.Code != http.StatusBadRequest || !strings.Contains(apiErr.Error(), "version mismatch") {
		t.Fatalf("err = %#v, want 400 version mismatch", err)
	}
}

func ptrInt32(v int32) *int32    { return &v }
func ptrString(v string) *string { return &v }
