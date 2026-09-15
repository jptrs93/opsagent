package scheduler

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func TestSchedulerNeverPlacesOnEvictedNode(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	nodes.EnsurePrimaryNode(store, "primary", "primary-id")
	req, version, err := nodes.UpsertEnrollmentRequest(store, "127.0.0.1", "v0.0.1", apigen.NodeReported{Identifier: "secondary-id", UnderlayAddress: "10.0.0.2", WgPublicKey: "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodes.AcceptEnrollmentRequest(store, req.ID, "secondary", req.RequestingMachineID, version); err != nil {
		t.Fatal(err)
	}
	var node *nodes.Node
	for _, member := range nodes.ListNodes(store.Queries()) {
		if member.Identifier == "secondary-id" {
			node = member
		}
	}
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, testRunningSpec("v1"))
	startScheduler(t, store, newFakeBarrier())
	sweep(t, store)
	if got := statesByID(store, cfg.DeploymentID); len(got) != 1 {
		t.Fatalf("placements before eviction = %v, want one", got)
	}

	if _, err := nodes.EvictNode(apigen.Context{Ctx: context.Background()}, store, node.Identifier, node.Version, true); err != nil {
		t.Fatalf("EvictNode: %v", err)
	}
	if got := statetest.NonFinalInstances(store, cfg.DeploymentID); len(got) != 0 {
		t.Fatalf("eviction commit left placements %v on the evicted node", got)
	}
	sweep(t, store)
	if got := statetest.NonFinalInstances(store, cfg.DeploymentID); len(got) != 0 {
		t.Fatalf("sweep recreated placements %v on the evicted node", got)
	}
	statetest.UpdateDeploymentSpec(store, apigen.Context{}, cfg.DeploymentID, testRunningSpec("v2"))
	if got := statetest.NonFinalInstances(store, cfg.DeploymentID); len(got) != 0 {
		t.Fatalf("spec update placed %v on the evicted node", got)
	}
}
