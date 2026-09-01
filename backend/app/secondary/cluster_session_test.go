package secondary

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/state"
)

func testAssignment(id, deploymentID, nodeID int32) *apigen.ScheduledInstanceState {
	return &apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{
			ID: id, DeploymentID: deploymentID, DeploymentSpecVersion: 1, NodeID: nodeID,
			State: apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
		},
		Config: apigen.Deployment{
			ID: deploymentID, NodeID: nodeID, SpecVersion: 1,
			Spec: apigen.DeploymentSpec{
				Container1Spec: &apigen.ContainerSpec{
					Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "example/app"}},
					Runtime: apigen.ContainerRuntime{User: "1000"},
				},
				Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
			},
		},
	}
}

func instanceIDs(states []apigen.ScheduledInstanceState) []int32 {
	out := make([]int32, 0, len(states))
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
	const nodeID int32 = 5
	store := state.Open(filepath.Join(t.TempDir(), "secondary.db"))
	defer store.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &outbox{ch: make(chan *apigen.MsgToPrimary, 16), ctx: ctx}

	// Two assignments arrive, then the primary reconnects knowing only about 41.
	applySnapshot(ctx, out, store, &apigen.ScheduledInstanceSnapshot{
		Items: []*apigen.ScheduledInstanceState{
			testAssignment(41, 8, nodeID),
			testAssignment(42, 9, nodeID),
		},
	}, nodeID)
	if got := instanceIDs(store.FetchScheduledSnapshot(nil)); len(got) != 2 {
		t.Fatalf("instances after first snapshot = %v, want 41 and 42", got)
	}

	applySnapshot(ctx, out, store, &apigen.ScheduledInstanceSnapshot{
		Items: []*apigen.ScheduledInstanceState{testAssignment(41, 8, nodeID)},
	}, nodeID)

	got := instanceIDs(store.FetchScheduledSnapshot(nil))
	if len(got) != 1 || got[0] != 41 {
		t.Fatalf("instances after second snapshot = %v, want [41]", got)
	}
}

// TestApplySnapshotKeepsInstancesForOtherNodes guards the prune against dropping
// assignments it merely failed to recognise: items addressed to another node are
// skipped on the way in, and must not therefore count as absent.
func TestApplySnapshotKeepsInstancesForOtherNodes(t *testing.T) {
	const nodeID int32 = 5
	store := state.Open(filepath.Join(t.TempDir(), "secondary.db"))
	defer store.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &outbox{ch: make(chan *apigen.MsgToPrimary, 16), ctx: ctx}

	applySnapshot(ctx, out, store, &apigen.ScheduledInstanceSnapshot{
		Items: []*apigen.ScheduledInstanceState{testAssignment(41, 8, nodeID)},
	}, nodeID)

	// A snapshot naming this node's instance plus one for a different node.
	applySnapshot(ctx, out, store, &apigen.ScheduledInstanceSnapshot{
		Items: []*apigen.ScheduledInstanceState{
			testAssignment(41, 8, nodeID),
			testAssignment(99, 12, nodeID+1),
		},
	}, nodeID)

	got := instanceIDs(store.FetchScheduledSnapshot(nil))
	if len(got) != 1 || got[0] != 41 {
		t.Fatalf("instances = %v, want [41]: the other node's item must be ignored, not stored", got)
	}
}
