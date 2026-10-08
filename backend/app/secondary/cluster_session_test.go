package secondary

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/state"
)

func testAssignment(id, deploymentID, nodeID uint64) apigen.NodeInstance {
	return apigen.NodeInstance{
		Instance: apigen.ScheduledInstance{
			ID: id, NodeID: nodeID, Deployment: apigen.DeploymentRef{DeploymentID: deploymentID, Version: 1},
			State: apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
		},
		Config: apigen.DeploymentRecord{
			Deployment: apigen.Deployment{
				ID:         deploymentID,
				Name:       "app",
				Scheduling: apigen.DedicatedScheduling(false, nodeID),
				Spec: apigen.DeploymentSpec{
					Workload: apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
						Source:          apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{RemoteImage: &apigen.RemoteImage{Image: "example/app"}}},
						Runtime:         apigen.ContainerRuntime{User: "1000"},
						UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE,
					}}},
					Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
				},
			},
			Meta: apigen.EntityMeta{Version: 1, SpecVersion: 1},
		},
	}
}

func applySnapshotFrame(ctx context.Context, out *outbox, store *state.Service, nodeID uint64, items ...apigen.NodeInstance) {
	sess := &primarySessionState{netMapSnapshotPending: true}
	applyProjectionFrame(ctx, out, store, nodeID, projectionFrame{instances: items, snapshot: true}, sess, nil, nil, nil)
}

func instanceIDs(states []apigen.ScheduledInstanceState) []uint64 {
	out := make([]uint64, 0, len(states))
	for _, s := range states {
		out = append(out, s.Instance.ID)
	}
	return out
}

// TestApplySnapshotPrunesInstancesMissingFromSnapshot covers a secondary rejoining
// after the primary has dropped one of its assignments. The snapshot is the
// primary's complete set for this node, so the instance it omits must be torn
// down: no further update naming it will ever arrive.
func TestApplySnapshotPrunesInstancesMissingFromSnapshot(t *testing.T) {
	const nodeID uint64 = 5
	store := state.Open(filepath.Join(t.TempDir(), "secondary.db"))
	defer store.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &outbox{ch: make(chan *apigen.MsgToPrimary, 16), ctx: ctx}

	// Two assignments arrive, then the primary reconnects knowing only about 41.
	applySnapshotFrame(ctx, out, store, nodeID, testAssignment(41, 8, nodeID), testAssignment(42, 9, nodeID))
	if got := instanceIDs(store.FetchScheduledSnapshot(nil)); len(got) != 2 {
		t.Fatalf("instances after first snapshot = %v, want 41 and 42", got)
	}

	applySnapshotFrame(ctx, out, store, nodeID, testAssignment(41, 8, nodeID))

	got := instanceIDs(store.FetchScheduledSnapshot(nil))
	if len(got) != 1 || got[0] != 41 {
		t.Fatalf("instances after second snapshot = %v, want [41]", got)
	}
}

func TestApplyProjectionIsAtomic(t *testing.T) {
	const nodeID uint64 = 1
	store := state.Open(filepath.Join(t.TempDir(), "secondary.db"))
	defer store.Close()
	prefix := network.GeneratePrefix()
	ctx := context.Background()

	rejected := testClusterNetMap(t, prefix, 1)
	rejected.TargetNodeID = 2
	frame := projectionFrame{instances: []apigen.NodeInstance{testAssignment(41, 8, nodeID)}, snapshot: true, netMap: rejected, acme: &apigen.AcmeState{Seq: 1}}
	if _, _, err := applyProjection(ctx, store, nodeID, frame, prefix, true, nil, nil); err == nil {
		t.Fatal("frame with a map for another node accepted")
	}
	if got := instanceIDs(store.FetchScheduledSnapshot(nil)); len(got) != 0 {
		t.Fatalf("instances after rejected frame = %v, want none", got)
	}
	if _, ok := store.FetchLocalKV(storage.LocalKVAcmeState); ok {
		t.Fatal("ACME state written by a rejected frame")
	}

	frame.netMap = testClusterNetMap(t, prefix, 1)
	status, _, err := applyProjection(ctx, store, nodeID, frame, prefix, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status == nil || status.PersistedSeq != 1 {
		t.Fatalf("status = %+v", status)
	}
	if got := instanceIDs(store.FetchScheduledSnapshot(nil)); len(got) != 1 || got[0] != 41 {
		t.Fatalf("instances after accepted frame = %v, want [41]", got)
	}
	if _, ok := store.FetchLocalKV(storage.LocalKVAcmeState); !ok {
		t.Fatal("ACME state missing after accepted frame")
	}
	if cached, _, ok, err := cachedClusterNetMap(ctx, store, nodeID, prefix); err != nil || !ok || cached.DerivedFromSeq != 1 {
		t.Fatalf("cached map after accepted frame: %+v ok=%v err=%v", cached, ok, err)
	}
}
