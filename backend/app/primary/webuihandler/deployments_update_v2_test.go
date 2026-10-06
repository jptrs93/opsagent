package webuihandler

import (
	"context"
	"errors"
	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func newV2DeploymentHandler(t *testing.T) (*Handler, *apigen.DeploymentRecord, *state.Service) {
	t.Helper()
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := ensureTestNode(store, "primary", "primary-id")
	h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries()}
	cfg, err := h.deploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: 1, Name: "web",
		Scheduling: apigen.DedicatedScheduling(false, node.ID),
		Spec:       remoteDeploymentSpec("nginx", hostNetworking()),
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	return h, cfg, store
}

func TestPostV2DeploymentsUpdateRequiresExactlyOneKind(t *testing.T) {
	h, cfg, _ := newV2DeploymentHandler(t)
	for name, req := range map[string]*apigen.DeploymentUpdateRequest{
		"none": {DeploymentID: cfg.Deployment.ID, ExpectedSeq: cfg.Meta.UpdatedSeq},
		"two": {DeploymentID: cfg.Deployment.ID, ExpectedSeq: cfg.Meta.UpdatedSeq,
			Update: apigen.DeploymentUpdateRequestUpdateOneof{VersionOnly: &apigen.VersionOnlyUpdate{TargetVersion: "1.29"}, RunningOnly: &apigen.RunningOnlyUpdate{DesiredRunning: true}}},
	} {
		_, err := h.deploymentsUpdate(apigen.Context{}, req)
		var apiErr apigen.ApiErr
		if !errors.As(err, &apiErr) || !strings.Contains(apiErr.DisplayErr, "exactly one alternative") {
			t.Fatalf("%s kinds err = %v, want exactly-one rejection", name, err)
		}
	}
}

func TestPostV2DeploymentsUpdateVersionOnly(t *testing.T) {
	h, cfg, _ := newV2DeploymentHandler(t)
	updated, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  cfg.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{VersionOnly: &apigen.VersionOnlyUpdate{TargetVersion: "1.29"}},
	})
	if err != nil {
		t.Fatalf("version-only update: %v", err)
	}
	if updated.WorkloadVersion() != "1.29" || !updated.WorkloadRunning() {
		t.Fatalf("workload state = %q/%v, want 1.29/running", updated.WorkloadVersion(), updated.WorkloadRunning())
	}
	if updated.Meta.Version != cfg.Meta.Version+1 || updated.Meta.SpecVersion != cfg.Meta.SpecVersion+1 || updated.Deployment.SpaceID != cfg.Deployment.SpaceID {
		t.Fatalf("versions = %d/%d space %d, want %d/%d space %d",
			updated.Meta.Version, updated.Meta.SpecVersion, updated.Deployment.SpaceID,
			cfg.Meta.Version+1, cfg.Meta.SpecVersion+1, cfg.Deployment.SpaceID)
	}
	if updated.Deployment.Spec.Workload.Value.Container.Source.Value.RemoteImage.Image != "nginx" {
		t.Fatalf("image = %q, rest of spec must be untouched", updated.Deployment.Spec.Workload.Value.Container.Source.Value.RemoteImage.Image)
	}
	if _, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  updated.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{VersionOnly: &apigen.VersionOnlyUpdate{}},
	}); err == nil || !strings.Contains(err.Error(), "no version to start") {
		t.Fatalf("empty target version err = %v, want rejection", err)
	}
}

func TestPostV2DeploymentsUpdateRunningOnly(t *testing.T) {
	h, cfg, _ := newV2DeploymentHandler(t)

	if _, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  cfg.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{RunningOnly: &apigen.RunningOnlyUpdate{DesiredRunning: true}},
	}); err == nil || !strings.Contains(err.Error(), "no version to start") {
		t.Fatalf("start without version err = %v, want rejection", err)
	}

	running, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  cfg.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{VersionOnly: &apigen.VersionOnlyUpdate{TargetVersion: "1.29"}},
	})
	if err != nil {
		t.Fatalf("version-only update: %v", err)
	}
	stopped, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  running.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{RunningOnly: &apigen.RunningOnlyUpdate{DesiredRunning: false}},
	})
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if stopped.WorkloadRunning() || stopped.WorkloadVersion() != "1.29" {
		t.Fatalf("stopped state = %q/%v, want version preserved and not running", stopped.WorkloadVersion(), stopped.WorkloadRunning())
	}
	started, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  stopped.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{RunningOnly: &apigen.RunningOnlyUpdate{DesiredRunning: true}},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !started.WorkloadRunning() || started.WorkloadVersion() != "1.29" {
		t.Fatalf("started state = %q/%v, want preserved version running", started.WorkloadVersion(), started.WorkloadRunning())
	}
}

func TestPostV2DeploymentsUpdateRestart(t *testing.T) {
	h, cfg, _ := newV2DeploymentHandler(t)
	if _, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  cfg.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{Restart: &apigen.RestartUpdate{}},
	}); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("restart stopped err = %v, want rejection", err)
	}
	running, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  cfg.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{VersionOnly: &apigen.VersionOnlyUpdate{TargetVersion: "1.29"}},
	})
	if err != nil {
		t.Fatalf("version-only update: %v", err)
	}
	restarted, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  running.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{Restart: &apigen.RestartUpdate{}},
	})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if restarted.Meta.Version != running.Meta.Version+1 || restarted.Meta.SpecVersion != running.Meta.SpecVersion {
		t.Fatalf("restart version/spec = %d/%d, want %d/%d", restarted.Meta.Version, restarted.Meta.SpecVersion, running.Meta.Version+1, running.Meta.SpecVersion)
	}
	if !restarted.WorkloadRunning() || restarted.WorkloadVersion() != "1.29" {
		t.Fatalf("restart state = %q/%v, want 1.29 running", restarted.WorkloadVersion(), restarted.WorkloadRunning())
	}
	if _, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  running.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{Restart: &apigen.RestartUpdate{}},
	}); err == nil || !strings.Contains(err.Error(), "changed since it was loaded") {
		t.Fatalf("stale restart err = %v, want changed since it was loaded", err)
	}
}

func TestPostV2DeploymentsUpdateSpec(t *testing.T) {
	h, cfg, _ := newV2DeploymentHandler(t)
	spec := remoteDeploymentSpec("nginx", hostNetworking())
	spec.Workload.Value.Container.Version = "2.8"
	spec.Workload.Value.Container.Runtime.User = "1000"
	updated, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  cfg.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{Spec: &apigen.SpecUpdate{Spec: spec}},
	})
	if err != nil {
		t.Fatalf("spec update: %v", err)
	}
	if updated.Deployment.Spec.Workload.Value.Container.Runtime.User != "1000" ||
		updated.WorkloadVersion() != "2.8" || updated.WorkloadRunning() != cfg.WorkloadRunning() {
		t.Fatalf("updated spec = %+v, want user 1000 at 2.8 with running state unchanged", updated.Deployment.Spec.Workload.Value.Container)
	}
	if updated.Meta.SpecVersion != cfg.Meta.SpecVersion+1 || updated.Meta.Version != cfg.Meta.Version+1 {
		t.Fatalf("versions = %d/%d, want spec and top-level bumps", updated.Meta.SpecVersion, updated.Meta.Version)
	}

	before := globalSeq(t, h)
	same, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  updated.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{Spec: &apigen.SpecUpdate{Spec: spec}},
	})
	if err != nil || same.Meta.UpdatedSeq != updated.Meta.UpdatedSeq || same.Meta.Version != updated.Meta.Version || globalSeq(t, h) != before {
		t.Fatalf("no-op spec update = %+v, %v, want the current event without a write", same, err)
	}
}

func TestPostV2DeploymentsUpdateAssignedSpace(t *testing.T) {
	h, cfg, store := newV2DeploymentHandler(t)
	extraSpace, err := nodes.CreateSpace(store, "other", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	moved, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  cfg.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{AssignedSpace: &apigen.AssignedSpaceUpdate{SpaceID: extraSpace.ID}},
	})
	if err != nil {
		t.Fatalf("space move: %v", err)
	}
	if moved.Deployment.SpaceID != extraSpace.ID || moved.Meta.SpecVersion != cfg.Meta.SpecVersion || moved.Meta.Version != cfg.Meta.Version+1 {
		t.Fatalf("moved = space %d specV%d v%d, want space %d specV%d v%d",
			moved.Deployment.SpaceID, moved.Meta.SpecVersion, moved.Meta.Version, extraSpace.ID, cfg.Meta.SpecVersion, cfg.Meta.Version+1)
	}

	before := globalSeq(t, h)
	same, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  moved.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{AssignedSpace: &apigen.AssignedSpaceUpdate{SpaceID: extraSpace.ID}},
	})
	if err != nil || same.Meta.UpdatedSeq != moved.Meta.UpdatedSeq || same.Meta.Version != moved.Meta.Version || globalSeq(t, h) != before {
		t.Fatalf("same-space move = %+v, %v, want the current event without a write", same, err)
	}

	if _, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  moved.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{AssignedSpace: &apigen.AssignedSpaceUpdate{SpaceID: 0}},
	}); err == nil || !strings.Contains(err.Error(), "spaceId must be between 1") {
		t.Fatalf("move into space 0 err = %v, want spaceId range error", err)
	}

	zeroSpec := remoteDeploymentSpec("nginx", hostNetworking())
	zeroDep := statetest.MustCreateStoppedDeploymentForNode(store, apigen.Context{}, 0, "zerodep", cfg.Deployment.PlacementNodeID(), &zeroSpec)
	if _, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: zeroDep.Deployment.ID,
		ExpectedSeq:  zeroDep.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{AssignedSpace: &apigen.AssignedSpaceUpdate{SpaceID: extraSpace.ID}},
	}); err == nil || !strings.Contains(err.Error(), "space 0 cannot be moved") {
		t.Fatalf("move out of space 0 err = %v, want immovable error", err)
	}
}

func TestPostV2DeploymentsUpdateAssignedSpaceRejectsDuplicateIdentity(t *testing.T) {
	h, cfg, store := newV2DeploymentHandler(t)
	extraSpace, err := nodes.CreateSpace(store, "other", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	spec := remoteDeploymentSpec("nginx", hostNetworking())
	twin := statetest.MustCreateStoppedDeploymentForNode(store, apigen.Context{}, extraSpace.ID, cfg.Deployment.Name, cfg.Deployment.PlacementNodeID(), &spec)
	if _, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: twin.Deployment.ID,
		ExpectedSeq:  twin.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{AssignedSpace: &apigen.AssignedSpaceUpdate{SpaceID: cfg.Deployment.SpaceID}},
	}); !errors.Is(err, deployments.DuplicateErr) {
		t.Fatalf("duplicate identity move err = %v, want %v", err, deployments.DuplicateErr)
	}
}

func TestPostV2DeploymentsUpdateGuardCoversAllKinds(t *testing.T) {
	h, cfg, store := newV2DeploymentHandler(t)
	staleExpected := cfg.Meta.UpdatedSeq
	extraSpace, err := nodes.CreateSpace(store, "other", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if _, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  staleExpected,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{AssignedSpace: &apigen.AssignedSpaceUpdate{SpaceID: extraSpace.ID}},
	}); err != nil {
		t.Fatalf("space move: %v", err)
	}
	if _, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  staleExpected,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{VersionOnly: &apigen.VersionOnlyUpdate{TargetVersion: "1.29"}},
	}); err == nil || !strings.Contains(err.Error(), "changed since it was loaded") {
		t.Fatalf("stale guard after space move err = %v, want changed since it was loaded", err)
	}
}

func TestPostV2DeploymentsUpdateNixVerification(t *testing.T) {
	t.Run("starting stopped verifies and failure does not update", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, false)
		provider.sourceErr = errors.New("remote unavailable")
		req := &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.Deployment.ID,
			ExpectedSeq:  cfg.Meta.UpdatedSeq,
			Update:       apigen.DeploymentUpdateRequestUpdateOneof{RunningOnly: &apigen.RunningOnlyUpdate{DesiredRunning: true}},
		}
		if _, err := h.deploymentsUpdate(apigen.Context{Ctx: context.Background()}, req); err == nil {
			t.Fatal("expected source verification failure")
		}
		unchanged := h.findConfigByID(cfg.Deployment.ID)
		if unchanged.Meta.Version != cfg.Meta.Version || unchanged.WorkloadRunning() {
			t.Fatalf("deployment changed after failed verification: %+v", unchanged)
		}
		provider.sourceErr = nil
		provider.sourceCommitValid = true
		if _, err := h.deploymentsUpdate(apigen.Context{Ctx: context.Background()}, req); err != nil {
			t.Fatal(err)
		}
		if len(provider.validateCalls) != 2 {
			t.Fatalf("source calls = %v", provider.validateCalls)
		}
	})

	t.Run("target version change verifies", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, true)
		provider.validateCalls = nil
		if _, err := h.deploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.Deployment.ID,
			ExpectedSeq:  cfg.Meta.UpdatedSeq,
			Update:       apigen.DeploymentUpdateRequestUpdateOneof{VersionOnly: &apigen.VersionOnlyUpdate{TargetVersion: testNixCommit2}},
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
		stopped, err := h.deploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.Deployment.ID,
			ExpectedSeq:  cfg.Meta.UpdatedSeq,
			Update:       apigen.DeploymentUpdateRequestUpdateOneof{RunningOnly: &apigen.RunningOnlyUpdate{DesiredRunning: false}},
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
		if _, err := h.deploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.Deployment.ID,
			ExpectedSeq:  cfg.Meta.UpdatedSeq,
			Update:       apigen.DeploymentUpdateRequestUpdateOneof{Spec: &apigen.SpecUpdate{Spec: spec}},
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
		same, err := h.deploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.Deployment.ID,
			ExpectedSeq:  cfg.Meta.UpdatedSeq,
			Update:       apigen.DeploymentUpdateRequestUpdateOneof{VersionOnly: &apigen.VersionOnlyUpdate{TargetVersion: testNixCommit}},
		})
		if err != nil || same.Meta.UpdatedSeq != cfg.Meta.UpdatedSeq || len(provider.validateCalls) != 0 {
			t.Fatalf("no-op = %+v err %v calls %+v, want the current event without a write and no source calls", same, err, provider.validateCalls)
		}
		if current := erru.Must(h.Store.Queries().GetLatestDeployment(context.Background(), cfg.Deployment.ID)); current.Meta.Version != cfg.Meta.Version {
			t.Fatalf("store version = %d, want unchanged %d", current.Meta.Version, cfg.Meta.Version)
		}
	})

	t.Run("stopped source kind change clears incompatible version", func(t *testing.T) {
		store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
		node := ensureTestNode(store, "primary", "primary")
		provider := &fakeGitSourceProvider{sourceErr: errors.New("must not be called")}
		h := &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), GitVersions: provider}
		cfg, err := h.deploymentsCreate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentCreateRequest{
			SpaceID: 1, Name: "web",
			Scheduling: apigen.DedicatedScheduling(false, node.ID),
			Spec: func() apigen.DeploymentSpec {
				spec := remoteDeploymentSpec("nginx", hostNetworking())
				spec.Workload.Value.Container.Version = "latest"
				return spec
			}(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.deploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.Deployment.ID,
			ExpectedSeq:  cfg.Meta.UpdatedSeq,
			Update:       apigen.DeploymentUpdateRequestUpdateOneof{Spec: &apigen.SpecUpdate{Spec: nixDeploymentSpecWithVersion("github.com/acme/app", "flake.nix", "latest")}},
		}); err != nil {
			t.Fatal(err)
		}
		updated := h.findConfigByID(cfg.Deployment.ID)
		if updated.WorkloadVersion() != "" || updated.WorkloadRunning() {
			t.Fatalf("workload state = %+v, want stopped with empty version", updated.Deployment.Spec.Workload.Value.Container)
		}
		if len(provider.validateCalls) != 0 {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
	})

	t.Run("stopped spec source change clears incompatible version", func(t *testing.T) {
		h, cfg, provider := newNixDeploymentHandler(t, false)
		provider.validateCalls = nil
		spec := nixDeploymentSpecWithVersion("github.com/acme/other", "flake.nix", testNixCommit)
		if _, err := h.deploymentsUpdate(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentUpdateRequest{
			DeploymentID: cfg.Deployment.ID,
			ExpectedSeq:  cfg.Meta.UpdatedSeq,
			Update:       apigen.DeploymentUpdateRequestUpdateOneof{Spec: &apigen.SpecUpdate{Spec: spec}},
		}); err != nil {
			t.Fatal(err)
		}
		updated := h.findConfigByID(cfg.Deployment.ID)
		if updated.WorkloadVersion() != "" || updated.WorkloadRunning() {
			t.Fatalf("workload state = %+v, want stopped with empty version", updated.Deployment.Spec.Workload.Value.Container)
		}
		if len(provider.validateCalls) != 0 {
			t.Fatalf("source calls = %+v", provider.validateCalls)
		}
	})
}
