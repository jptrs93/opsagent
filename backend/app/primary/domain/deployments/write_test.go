package deployments

import (
	"context"
	"net/netip"
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
	node := nodes.EnsurePrimaryNode(store, "primary", "primary", netip.MustParseAddr("10.0.0.1"))
	svc := &Service{Store: store}
	ctx := apigen.Context{Ctx: context.Background()}
	initial, err := svc.Create(ctx, &apigen.Deployment{
		Name: "web", SpaceID: nodes.DefaultSpaceID, Scheduling: apigen.DedicatedScheduling(false, node.ID),
		Spec: remoteDeploymentSpec("nginx", virtualNetworking()),
	})
	if err != nil {
		t.Fatal(err)
	}
	// An administrator adds host access after another request has loaded and
	// authorized the initial, unprivileged deployment.
	hostSpec := remoteDeploymentSpec("nginx", hostNetworking())
	privileged, err := svc.Update(ctx, initial, &apigen.DeploymentUpdateRequest{
		DeploymentID: initial.Deployment.ID, ExpectedSeq: initial.Meta.UpdatedSeq,
		Update: apigen.DeploymentUpdateRequestUpdateOneof{Spec: &apigen.SpecUpdate{Spec: hostSpec}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, existing := range []*apigen.DeploymentRecord{initial, privileged} {
		_, err := svc.Update(ctx, existing, &apigen.DeploymentUpdateRequest{
			DeploymentID: initial.Deployment.ID, ExpectedSeq: initial.Meta.UpdatedSeq,
			Update: apigen.DeploymentUpdateRequestUpdateOneof{VersionOnly: &apigen.VersionOnlyUpdate{TargetVersion: "1.29"}},
		})
		if err == nil || !strings.Contains(err.Error(), "changed since it was loaded") {
			t.Fatalf("stale token against the row at seq %d: %v", existing.Meta.UpdatedSeq, err)
		}
	}
	latest, err := store.Queries().GetLatestDeployment(ctx, initial.Deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Meta.Version != privileged.Meta.Version || latest.Deployment.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_HOST {
		t.Fatal("stale update overwrote the host-access deployment")
	}
}

func TestRestartUpdateWritesTheUnchangedDefinition(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := nodes.EnsurePrimaryNode(store, "primary", "primary", netip.MustParseAddr("10.0.0.1"))
	svc := &Service{Store: store}
	ctx := apigen.Context{Ctx: context.Background()}
	initial, err := svc.Create(ctx, &apigen.Deployment{
		Name: "web", SpaceID: nodes.DefaultSpaceID, Scheduling: apigen.DedicatedScheduling(false, node.ID),
		Spec: remoteDeploymentSpec("nginx", virtualNetworking()),
	})
	if err != nil {
		t.Fatal(err)
	}
	restart := func(existing *apigen.DeploymentRecord) (*apigen.DeploymentRecord, error) {
		return svc.Update(ctx, existing, &apigen.DeploymentUpdateRequest{
			DeploymentID: existing.Deployment.ID, ExpectedSeq: existing.Meta.UpdatedSeq,
			Update: apigen.DeploymentUpdateRequestUpdateOneof{Restart: &apigen.RestartUpdate{}},
		})
	}
	if _, err := restart(initial); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("restart of a stopped deployment: %v, want rejection", err)
	}
	running, err := svc.Update(ctx, initial, &apigen.DeploymentUpdateRequest{
		DeploymentID: initial.Deployment.ID, ExpectedSeq: initial.Meta.UpdatedSeq,
		Update: apigen.DeploymentUpdateRequestUpdateOneof{VersionOnly: &apigen.VersionOnlyUpdate{TargetVersion: "1.29"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := restart(running)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Meta.Version != running.Meta.Version+1 || restarted.Meta.UpdatedSeq <= running.Meta.UpdatedSeq {
		t.Fatalf("version/seq = %d/%d, want %d and a later seq than %d", restarted.Meta.Version, restarted.Meta.UpdatedSeq, running.Meta.Version+1, running.Meta.UpdatedSeq)
	}
	if restarted.Deployment.Scheduling.RestartGeneration != running.Deployment.Scheduling.RestartGeneration+1 {
		t.Fatalf("generation = %d, want %d", restarted.Deployment.Scheduling.RestartGeneration, running.Deployment.Scheduling.RestartGeneration+1)
	}
	if restarted.Meta.SpecVersion != running.Meta.SpecVersion || restarted.Deployment.SpaceID != running.Deployment.SpaceID || restarted.Deployment.Name != running.Deployment.Name {
		t.Fatalf("facets moved: spec %d->%d space %d->%d name %q->%q",
			running.Meta.SpecVersion, restarted.Meta.SpecVersion, running.Deployment.SpaceID, restarted.Deployment.SpaceID, running.Deployment.Name, restarted.Deployment.Name)
	}
	if !pq.DeploymentSpecsEqual(&restarted.Deployment.Spec, &running.Deployment.Spec) ||
		restarted.Deployment.Name != running.Deployment.Name || restarted.Deployment.SpaceID != running.Deployment.SpaceID || restarted.Deployment.PlacementNodeID() != running.Deployment.PlacementNodeID() {
		t.Fatal("restart changed the definition")
	}
	if restarted.Meta.Deleted {
		t.Fatal("restart produced a delete")
	}
	again, err := restart(restarted)
	if err != nil {
		t.Fatal(err)
	}
	if again.Meta.Version != restarted.Meta.Version+1 || again.Meta.SpecVersion != restarted.Meta.SpecVersion {
		t.Fatalf("second restart version/spec = %d/%d, want %d/%d", again.Meta.Version, again.Meta.SpecVersion, restarted.Meta.Version+1, restarted.Meta.SpecVersion)
	}
	if _, err := restart(restarted); err == nil || !strings.Contains(err.Error(), "changed since it was loaded") {
		t.Fatalf("stale restart: %v, want a stale token rejection", err)
	}
}

func TestRestartUpdateRejectsTheSelfDeployment(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := nodes.EnsurePrimaryNode(store, "primary", "primary", netip.MustParseAddr("10.0.0.1"))
	EnsureSystem(store, node.ID, "v0.0.1")
	var self *apigen.DeploymentRecord
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
	_, err := svc.Update(apigen.Context{Ctx: context.Background()}, self, &apigen.DeploymentUpdateRequest{
		DeploymentID: self.Deployment.ID, ExpectedSeq: self.Meta.UpdatedSeq,
		Update: apigen.DeploymentUpdateRequestUpdateOneof{Restart: &apigen.RestartUpdate{}},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be restarted") {
		t.Fatalf("self restart: %v, want rejection", err)
	}
}
