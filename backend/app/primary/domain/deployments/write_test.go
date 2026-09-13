package deployments

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func TestUpdateCannotUseStaleAuthorizedDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	svc := &Service{Store: store}
	ctx := apigen.Context{Ctx: context.Background()}
	initial, err := svc.Create(ctx, &apigen.Deployment{
		Name: "web", SpaceID: nodes.DefaultSpaceID, NodeID: node.ID,
		Spec: remoteDeploymentSpec("nginx", virtualNetworking()),
	})
	if err != nil {
		t.Fatal(err)
	}
	// An administrator adds host access after another request has loaded and
	// authorized the initial, unprivileged deployment.
	hostSpec := remoteDeploymentSpec("nginx", hostNetworking())
	privileged, err := svc.Update(ctx, initial, &apigen.DeploymentUpdateRequestV2{
		DeploymentID: initial.DeploymentID, ExpectedVersion: initial.Version + 1,
		SpecUpdate: &apigen.SpecUpdate{Spec: hostSpec},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expectedNext := range []int32{initial.Version + 1, privileged.Version + 1} {
		_, err := svc.Update(ctx, initial, &apigen.DeploymentUpdateRequestV2{
			DeploymentID: initial.DeploymentID, ExpectedVersion: expectedNext,
			VersionOnlyUpdate: &apigen.VersionOnlyUpdate{TargetVersion: "1.29"},
		})
		if err == nil || !strings.Contains(err.Error(), "deployment version mismatch") {
			t.Fatalf("stale snapshot with expected next version %d: %v", expectedNext, err)
		}
	}
	latest, err := store.Queries().GetLatestDeploymentEvent(ctx, int64(initial.DeploymentID))
	if err != nil {
		t.Fatal(err)
	}
	if latest.Version != privileged.Version || latest.Value.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_HOST {
		t.Fatal("stale update overwrote the host-access deployment")
	}
}

func TestRestartUpdateWritesTheUnchangedDefinition(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	svc := &Service{Store: store}
	ctx := apigen.Context{Ctx: context.Background()}
	initial, err := svc.Create(ctx, &apigen.Deployment{
		Name: "web", SpaceID: nodes.DefaultSpaceID, NodeID: node.ID,
		Spec: remoteDeploymentSpec("nginx", virtualNetworking()),
	})
	if err != nil {
		t.Fatal(err)
	}
	restart := func(existing *apigen.DeploymentEvent) (*apigen.DeploymentEvent, error) {
		return svc.Update(ctx, existing, &apigen.DeploymentUpdateRequestV2{
			DeploymentID: existing.DeploymentID, ExpectedVersion: existing.Version + 1,
			RestartUpdate: &apigen.RestartUpdate{},
		})
	}
	if _, err := restart(initial); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("restart of a stopped deployment: %v, want rejection", err)
	}
	running, err := svc.Update(ctx, initial, &apigen.DeploymentUpdateRequestV2{
		DeploymentID: initial.DeploymentID, ExpectedVersion: initial.Version + 1,
		VersionOnlyUpdate: &apigen.VersionOnlyUpdate{TargetVersion: "1.29"},
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := restart(running)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Version != running.Version+1 {
		t.Fatalf("version = %d, want %d", restarted.Version, running.Version+1)
	}
	if restarted.SpecVersion != running.SpecVersion || restarted.SpaceVersion != running.SpaceVersion || restarted.NameVersion != running.NameVersion {
		t.Fatalf("facets moved: spec %d->%d space %d->%d name %d->%d",
			running.SpecVersion, restarted.SpecVersion, running.SpaceVersion, restarted.SpaceVersion, running.NameVersion, restarted.NameVersion)
	}
	if !pq.DeploymentSpecsEqual(&restarted.Value.Spec, &running.Value.Spec) ||
		restarted.Value.Name != running.Value.Name || restarted.Value.SpaceID != running.Value.SpaceID || restarted.Value.NodeID != running.Value.NodeID {
		t.Fatal("restart changed the definition")
	}
	if restarted.EventType != apigen.EventType_EVENT_TYPE_UPDATE {
		t.Fatalf("event type = %v, want update", restarted.EventType)
	}
	again, err := restart(restarted)
	if err != nil {
		t.Fatal(err)
	}
	if again.Version != restarted.Version+1 || again.SpecVersion != restarted.SpecVersion {
		t.Fatalf("second restart version/spec = %d/%d, want %d/%d", again.Version, again.SpecVersion, restarted.Version+1, restarted.SpecVersion)
	}
	if _, err := restart(restarted); err == nil || !strings.Contains(err.Error(), "deployment version mismatch") {
		t.Fatalf("stale restart: %v, want version mismatch", err)
	}
}

func TestRestartUpdateRejectsTheSelfDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	EnsureSystem(store, node.ID, "v0.0.1")
	var self *apigen.DeploymentEvent
	for _, cfg := range Active(store.Queries(), nil) {
		if internaldeploy.IsSelfConfig(&cfg) {
			c := cfg
			self = &c
		}
	}
	if self == nil {
		t.Fatal("self deployment not created")
	}
	svc := &Service{Store: store}
	_, err := svc.Update(apigen.Context{Ctx: context.Background()}, self, &apigen.DeploymentUpdateRequestV2{
		DeploymentID: self.DeploymentID, ExpectedVersion: self.Version + 1,
		RestartUpdate: &apigen.RestartUpdate{},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be restarted") {
		t.Fatalf("self restart: %v, want rejection", err)
	}
}
