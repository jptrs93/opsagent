package deployments

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
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
