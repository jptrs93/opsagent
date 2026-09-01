package webuihandler

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func newV2DeploymentHandler(t *testing.T) (*Handler, *apigen.Deployment, *state.Service) {
	t.Helper()
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := store.EnsurePrimaryNode("primary", "primary-id")
	h := &Handler{ConfigService: &config.Service{}, Store: store}
	cfg, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: 1, Name: "web",
		NodeID: node.ID,
		Spec:   remoteDeploymentSpec("nginx", hostNetworking()),
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	return h, cfg, store
}

func TestPostV2DeploymentsUpdateRequiresExactlyOneKind(t *testing.T) {
	h, cfg, _ := newV2DeploymentHandler(t)
	for name, req := range map[string]*apigen.DeploymentUpdateRequestV2{
		"none": {DeploymentID: cfg.ID, ExpectedVersion: cfg.Version + 1},
		"two": {DeploymentID: cfg.ID, ExpectedVersion: cfg.Version + 1,
			VersionOnlyUpdate: &apigen.VersionOnlyUpdate{TargetVersion: "1.29"},
			RunningOnlyUpdate: &apigen.RunningOnlyUpdate{DesiredRunning: true}},
	} {
		if _, err := h.PostV2DeploymentsUpdate(apigen.Context{}, req); err == nil || !strings.Contains(err.Error(), "exactly one update kind") {
			t.Fatalf("%s kinds err = %v, want exactly-one rejection", name, err)
		}
	}
}

func TestPostV2DeploymentsUpdateVersionOnly(t *testing.T) {
	h, cfg, _ := newV2DeploymentHandler(t)
	updated, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:      cfg.ID,
		ExpectedVersion:   cfg.Version + 1,
		VersionOnlyUpdate: &apigen.VersionOnlyUpdate{TargetVersion: "1.29"},
	})
	if err != nil {
		t.Fatalf("version-only update: %v", err)
	}
	if updated.WorkloadVersion() != "1.29" || !updated.WorkloadRunning() {
		t.Fatalf("workload state = %q/%v, want 1.29/running", updated.WorkloadVersion(), updated.WorkloadRunning())
	}
	if updated.Version != cfg.Version+1 || updated.SpecVersion != cfg.SpecVersion+1 || updated.SpaceVersion != cfg.SpaceVersion {
		t.Fatalf("versions = %d/%d/%d, want %d/%d/%d",
			updated.Version, updated.SpecVersion, updated.SpaceVersion,
			cfg.Version+1, cfg.SpecVersion+1, cfg.SpaceVersion)
	}
	if updated.Def.Spec.Container1Spec.Source.RemoteImage.Image != "nginx" {
		t.Fatalf("image = %q, rest of spec must be untouched", updated.Def.Spec.Container1Spec.Source.RemoteImage.Image)
	}
	if _, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:      cfg.ID,
		ExpectedVersion:   updated.Version + 1,
		VersionOnlyUpdate: &apigen.VersionOnlyUpdate{},
	}); err == nil || !strings.Contains(err.Error(), "no version to start") {
		t.Fatalf("empty target version err = %v, want rejection", err)
	}
}

func TestPostV2DeploymentsUpdateRunningOnly(t *testing.T) {
	h, cfg, _ := newV2DeploymentHandler(t)

	if _, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:      cfg.ID,
		ExpectedVersion:   cfg.Version + 1,
		RunningOnlyUpdate: &apigen.RunningOnlyUpdate{DesiredRunning: true},
	}); err == nil || !strings.Contains(err.Error(), "no version to start") {
		t.Fatalf("start without version err = %v, want rejection", err)
	}

	running, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:      cfg.ID,
		ExpectedVersion:   cfg.Version + 1,
		VersionOnlyUpdate: &apigen.VersionOnlyUpdate{TargetVersion: "1.29"},
	})
	if err != nil {
		t.Fatalf("version-only update: %v", err)
	}
	stopped, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:      cfg.ID,
		ExpectedVersion:   running.Version + 1,
		RunningOnlyUpdate: &apigen.RunningOnlyUpdate{DesiredRunning: false},
	})
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if stopped.WorkloadRunning() || stopped.WorkloadVersion() != "1.29" {
		t.Fatalf("stopped state = %q/%v, want version preserved and not running", stopped.WorkloadVersion(), stopped.WorkloadRunning())
	}
	started, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:      cfg.ID,
		ExpectedVersion:   stopped.Version + 1,
		RunningOnlyUpdate: &apigen.RunningOnlyUpdate{DesiredRunning: true},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !started.WorkloadRunning() || started.WorkloadVersion() != "1.29" {
		t.Fatalf("started state = %q/%v, want preserved version running", started.WorkloadVersion(), started.WorkloadRunning())
	}
}

func TestPostV2DeploymentsUpdateSpec(t *testing.T) {
	h, cfg, _ := newV2DeploymentHandler(t)
	spec := remoteDeploymentSpec("caddy", hostNetworking())
	spec.Container1Spec.Version = "2.8"
	spec.Container1Spec.Running = true
	updated, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:    cfg.ID,
		ExpectedVersion: cfg.Version + 1,
		SpecUpdate:      &apigen.SpecUpdate{Spec: spec},
	})
	if err != nil {
		t.Fatalf("spec update: %v", err)
	}
	if updated.Def.Spec.Container1Spec.Source.RemoteImage.Image != "caddy" ||
		updated.WorkloadVersion() != "2.8" || !updated.WorkloadRunning() {
		t.Fatalf("updated spec = %+v, want caddy at 2.8 running", updated.Def.Spec.Container1Spec)
	}
	if updated.SpecVersion != cfg.SpecVersion+1 || updated.Version != cfg.Version+1 {
		t.Fatalf("versions = %d/%d, want spec and top-level bumps", updated.SpecVersion, updated.Version)
	}

	same, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:    cfg.ID,
		ExpectedVersion: updated.Version + 1,
		SpecUpdate:      &apigen.SpecUpdate{Spec: spec},
	})
	if err != nil || same.Version != updated.Version {
		t.Fatalf("no-op spec update = v%d err %v, want v%d", same.Version, err, updated.Version)
	}
}

func TestPostV2DeploymentsUpdateAssignedSpace(t *testing.T) {
	h, cfg, store := newV2DeploymentHandler(t)
	extraSpace, err := store.CreateSpace("other")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	moved, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:        cfg.ID,
		ExpectedVersion:     cfg.Version + 1,
		AssignedSpaceUpdate: &apigen.AssignedSpaceUpdate{SpaceID: extraSpace.ID},
	})
	if err != nil {
		t.Fatalf("space move: %v", err)
	}
	if moved.Def.SpaceID != extraSpace.ID || moved.SpaceVersion != cfg.SpaceVersion+1 ||
		moved.SpecVersion != cfg.SpecVersion || moved.Version != cfg.Version+1 {
		t.Fatalf("moved = space %d spaceV%d specV%d v%d, want space %d spaceV%d specV%d v%d",
			moved.Def.SpaceID, moved.SpaceVersion, moved.SpecVersion, moved.Version,
			extraSpace.ID, cfg.SpaceVersion+1, cfg.SpecVersion, cfg.Version+1)
	}

	same, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:        cfg.ID,
		ExpectedVersion:     moved.Version + 1,
		AssignedSpaceUpdate: &apigen.AssignedSpaceUpdate{SpaceID: extraSpace.ID},
	})
	if err != nil || same.Version != moved.Version {
		t.Fatalf("same-space move = v%d err %v, want no-op at v%d", same.Version, err, moved.Version)
	}

	if _, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:        cfg.ID,
		ExpectedVersion:     moved.Version + 1,
		AssignedSpaceUpdate: &apigen.AssignedSpaceUpdate{SpaceID: 0},
	}); err == nil || !strings.Contains(err.Error(), "spaceId must be between 1") {
		t.Fatalf("move into space 0 err = %v, want spaceId range error", err)
	}

	zeroSpec := remoteDeploymentSpec("nginx", hostNetworking())
	zeroDep := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 0, "zerodep", cfg.Def.NodeID, &zeroSpec)
	if _, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:        zeroDep.ID,
		ExpectedVersion:     zeroDep.Version + 1,
		AssignedSpaceUpdate: &apigen.AssignedSpaceUpdate{SpaceID: extraSpace.ID},
	}); err == nil || !strings.Contains(err.Error(), "space 0 cannot be moved") {
		t.Fatalf("move out of space 0 err = %v, want immovable error", err)
	}
}

func TestPostV2DeploymentsUpdateAssignedSpaceRejectsDuplicateIdentity(t *testing.T) {
	h, cfg, store := newV2DeploymentHandler(t)
	extraSpace, err := store.CreateSpace("other")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	spec := remoteDeploymentSpec("nginx", hostNetworking())
	twin := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, extraSpace.ID, cfg.Def.Name, cfg.Def.NodeID, &spec)
	if _, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:        twin.ID,
		ExpectedVersion:     twin.Version + 1,
		AssignedSpaceUpdate: &apigen.AssignedSpaceUpdate{SpaceID: cfg.Def.SpaceID},
	}); !errors.Is(err, DuplicateDeploymentErr) {
		t.Fatalf("duplicate identity move err = %v, want %v", err, DuplicateDeploymentErr)
	}
}

func TestPostV2DeploymentsUpdateGuardCoversAllKinds(t *testing.T) {
	h, cfg, store := newV2DeploymentHandler(t)
	staleExpected := cfg.Version + 1
	extraSpace, err := store.CreateSpace("other")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if _, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:        cfg.ID,
		ExpectedVersion:     staleExpected,
		AssignedSpaceUpdate: &apigen.AssignedSpaceUpdate{SpaceID: extraSpace.ID},
	}); err != nil {
		t.Fatalf("space move: %v", err)
	}
	if _, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:      cfg.ID,
		ExpectedVersion:   staleExpected,
		VersionOnlyUpdate: &apigen.VersionOnlyUpdate{TargetVersion: "1.29"},
	}); err == nil || !strings.Contains(err.Error(), "version mismatch") {
		t.Fatalf("stale guard after space move err = %v, want version mismatch", err)
	}
}

func TestPostV2DeploymentsUpdateNixVerification(t *testing.T) {
	t.Run("starting stopped verifies and failure does not update", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, false)
		provider.sourceErr = errors.New("remote unavailable")
		req := &apigen.DeploymentUpdateRequestV2{
			DeploymentID:      cfg.ID,
			ExpectedVersion:   cfg.Version + 1,
			RunningOnlyUpdate: &apigen.RunningOnlyUpdate{DesiredRunning: true},
		}
		if _, err := h.PostV2DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, req); err == nil {
			t.Fatal("expected source verification failure")
		}
		unchanged := h.findConfigByID(cfg.ID)
		if unchanged.Version != cfg.Version || unchanged.WorkloadRunning() {
			t.Fatalf("deployment changed after failed verification: %+v", unchanged)
		}
		provider.sourceErr = nil
		provider.sourceCommitValid = true
		if _, err := h.PostV2DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, req); err != nil {
			t.Fatal(err)
		}
		if len(provider.validateCalls) != 2 {
			t.Fatalf("source calls = %v", provider.validateCalls)
		}
	})

	t.Run("target version change verifies", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, true)
		provider.validateCalls = nil
		if _, err := h.PostV2DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequestV2{
			DeploymentID:      cfg.ID,
			ExpectedVersion:   cfg.Version + 1,
			VersionOnlyUpdate: &apigen.VersionOnlyUpdate{TargetVersion: testNixCommit2},
		}); err != nil {
			t.Fatal(err)
		}
		if len(provider.validateCalls) != 1 || provider.validateCalls[0].commit != testNixCommit2 {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
	})

	t.Run("stop skips provider", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, true)
		provider.validateCalls = nil
		provider.sourceErr = errors.New("must not be called")
		stopped, err := h.PostV2DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequestV2{
			DeploymentID:      cfg.ID,
			ExpectedVersion:   cfg.Version + 1,
			RunningOnlyUpdate: &apigen.RunningOnlyUpdate{DesiredRunning: false},
		})
		if err != nil {
			t.Fatal(err)
		}
		if stopped.WorkloadRunning() || stopped.WorkloadVersion() != testNixCommit {
			t.Fatalf("stopped state = %q/%v, want preserved version", stopped.WorkloadVersion(), stopped.WorkloadRunning())
		}
		if len(provider.validateCalls) != 0 {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
	})

	t.Run("spec change while running verifies", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, true)
		provider.validateCalls = nil
		spec := nixDeploymentSpec("github.com/acme/other", "nix/app/flake.nix")
		if _, err := h.PostV2DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequestV2{
			DeploymentID:    cfg.ID,
			ExpectedVersion: cfg.Version + 1,
			SpecUpdate:      &apigen.SpecUpdate{Spec: spec},
		}); err != nil {
			t.Fatal(err)
		}
		if len(provider.validateCalls) != 1 || provider.validateCalls[0].repo != "github.com/acme/other" || provider.validateCalls[0].commit != testNixCommit {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
	})

	t.Run("no-op version update skips provider and store", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, true)
		provider.validateCalls = nil
		same, err := h.PostV2DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequestV2{
			DeploymentID:      cfg.ID,
			ExpectedVersion:   cfg.Version + 1,
			VersionOnlyUpdate: &apigen.VersionOnlyUpdate{TargetVersion: testNixCommit},
		})
		if err != nil {
			t.Fatal(err)
		}
		if same.Version != cfg.Version || len(provider.validateCalls) != 0 {
			t.Fatalf("no-op = v%d calls %+v, want v%d and no source calls", same.Version, provider.validateCalls, cfg.Version)
		}
	})

	t.Run("stopped source kind change clears incompatible version", func(t *testing.T) {
		store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
		node := store.EnsurePrimaryNode("primary", "primary")
		provider := &fakeGitSourceProvider{sourceErr: errors.New("must not be called")}
		h := &Handler{ConfigService: &config.Service{}, Store: store, GitVersions: provider}
		cfg, err := h.PostV1DeploymentsCreate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentCreateRequest{
			SpaceID: 1, Name: "web",
			NodeID: node.ID,
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
		if _, err := h.PostV2DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequestV2{
			DeploymentID:    cfg.ID,
			ExpectedVersion: cfg.Version + 1,
			SpecUpdate:      &apigen.SpecUpdate{Spec: nixDeploymentSpecWithState("github.com/acme/app", "flake.nix", "latest", false)},
		}); err != nil {
			t.Fatal(err)
		}
		updated := h.findConfigByID(cfg.ID)
		if updated.WorkloadVersion() != "" || updated.WorkloadRunning() {
			t.Fatalf("workload state = %+v, want stopped with empty version", updated.Def.Spec.Container1Spec)
		}
		if len(provider.validateCalls) != 0 {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
	})

	t.Run("stopped spec source change clears incompatible version", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, false)
		provider.validateCalls = nil
		spec := nixDeploymentSpecWithState("github.com/acme/other", "flake.nix", testNixCommit, false)
		if _, err := h.PostV2DeploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequestV2{
			DeploymentID:    cfg.ID,
			ExpectedVersion: cfg.Version + 1,
			SpecUpdate:      &apigen.SpecUpdate{Spec: spec},
		}); err != nil {
			t.Fatal(err)
		}
		updated := h.findConfigByID(cfg.ID)
		if updated.WorkloadVersion() != "" || updated.WorkloadRunning() {
			t.Fatalf("workload state = %+v, want stopped with empty version", updated.Def.Spec.Container1Spec)
		}
		if len(provider.validateCalls) != 0 {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
	})
}
