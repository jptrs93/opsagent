package nodes

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func acceptSecondary(t *testing.T, store *state.Service, identifier string) *Node {
	t.Helper()
	req, version := mustUpsertEnrollmentRequest(t, store, "127.0.0.1", "v0.0.1", apigen.NodeReported{Identifier: identifier, UnderlayAddress: "10.0.0.2", WgPublicKey: "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="})
	if _, err := AcceptEnrollmentRequest(store, req.ID, identifier, identifier, version); err != nil {
		t.Fatalf("AcceptEnrollmentRequest: %v", err)
	}
	for _, node := range ListNodes(store.Queries()) {
		if node.Identifier == identifier {
			return node
		}
	}
	t.Fatalf("accepted node %q not listed", identifier)
	return nil
}

func nodeRow(t *testing.T, store *state.Service, identifier string) *Node {
	t.Helper()
	row, err := store.Queries().GetNodeRowByIdentifier(context.Background(), identifier)
	if err != nil {
		t.Fatalf("GetNodeRowByIdentifier(%q): %v", identifier, err)
	}
	return nodeRowToNode(row)
}

func TestEvictNodeRefusesPinnedDeploymentsWithoutForce(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	EnsurePrimaryNode(store, "primary", "primary-id")
	node := acceptSecondary(t, store, "secondary-id")
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, DefaultSpaceID, "web", node.ID, statetest.SpecWithVersion("v1"))
	statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)

	_, err := EvictNode(apigen.Context{Ctx: context.Background()}, store, node.Identifier, node.Version, false)
	var hasDeployments *ErrNodeHasDeployments
	if !errors.As(err, &hasDeployments) || hasDeployments.Count != 1 {
		t.Fatalf("evict with pinned deployment: got %v, want ErrNodeHasDeployments{1}", err)
	}
	if got := nodeRow(t, store, node.Identifier).Status; got != apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL {
		t.Fatalf("refused eviction changed status to %v", got)
	}
	if len(statetest.NonFinalInstances(store, cfg.DeploymentID)) != 1 {
		t.Fatal("refused eviction finalized the placement")
	}
}

func TestEvictNodeForceFinalizesPlacementsAndDeletesSystemDeployments(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	EnsurePrimaryNode(store, "primary", "primary-id")
	node := acceptSecondary(t, store, "secondary-id")
	ctx := apigen.Context{Ctx: context.Background()}
	web := statetest.MustCreateDeploymentForNode(store, ctx, DefaultSpaceID, "web", node.ID, statetest.SpecWithVersion("v1"))
	webInst := statetest.CreateScheduledInstance(store, web.DeploymentID, web.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	netproxy := statetest.MustCreateDeploymentForNode(store, ctx, internaldeploy.SpaceID, internaldeploy.NetproxyName, node.ID, internaldeploy.NetproxySpec())
	statetest.CreateScheduledInstance(store, netproxy.DeploymentID, netproxy.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		st := &apigen.ScheduledInstanceStatus{ScheduledInstanceID: webInst.ID, DeploymentID: web.DeploymentID, Runner: apigen.RunnerStatus{Status: apigen.RunningStatus_RUNNING}}
		st.BumpUpdatedAt()
		return &state.Update{InstanceStatuses: []*apigen.ScheduledInstanceStatus{st}}, q.InsertScheduledInstanceStatus(ctx, seq, st)
	}); err != nil {
		t.Fatalf("seed instance status: %v", err)
	}
	SetNodeStatusByIdentifier(store, node.Identifier, true, time.Now())
	if status, err := store.Queries().GetLatestNodeStatus(context.Background(), node.ID); err != nil || !status.IsConnected {
		t.Fatalf("node status before eviction = %+v (%v), want connected", status, err)
	}

	event, err := EvictNode(ctx, store, node.Identifier, node.Version, true)
	if err != nil {
		t.Fatalf("EvictNode: %v", err)
	}
	if event.Value.Status != apigen.NodeLifecycleStatus_NODE_MEMBER_EVICTED || event.NodeID != node.ID {
		t.Fatalf("evict event = %+v, want evicted node %d", event, node.ID)
	}
	if len(statetest.NonFinalInstances(store, web.DeploymentID)) != 0 || len(statetest.NonFinalInstances(store, netproxy.DeploymentID)) != 0 {
		t.Fatal("eviction left live placements on the node")
	}
	active, err := store.Queries().ListActiveDeployments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var webActive, netproxyActive bool
	for _, cfg := range active {
		webActive = webActive || cfg.DeploymentID == web.DeploymentID
		netproxyActive = netproxyActive || cfg.DeploymentID == netproxy.DeploymentID
	}
	if !webActive || netproxyActive {
		t.Fatalf("active after eviction: web=%v netproxy=%v, want web kept and netproxy deleted", webActive, netproxyActive)
	}
	if status, err := store.Queries().GetLatestScheduledInstanceStatus(context.Background(), webInst.ID); err != nil || status.Runner.Status != apigen.RunningStatus_DEPLOYMENT_STATUS_UNKNOWN {
		t.Fatalf("instance status after eviction = %+v (%v), want tombstone", status, err)
	}
	if status, err := store.Queries().GetLatestNodeStatus(context.Background(), node.ID); err != nil || status.IsConnected {
		t.Fatalf("node status after eviction = %+v (%v), want disconnected tombstone", status, err)
	}
	if _, err := MemberNodeIDByIdentifier(store.Queries(), node.Identifier); err == nil {
		t.Fatal("evicted identifier still resolves as a member")
	}
	if !IsEvictedIdentifier(store.Queries(), node.Identifier) {
		t.Fatal("evicted identifier not recognised as evicted")
	}
	for _, member := range ListNodes(store.Queries()) {
		if member.ID == node.ID {
			t.Fatal("evicted node still listed as a member")
		}
	}
	if _, err := EvictNode(ctx, store, node.Identifier, event.Version, true); !errors.Is(err, ErrNodeNotMember) {
		t.Fatalf("second eviction: got %v, want ErrNodeNotMember", err)
	}
}

func TestEvictNodeRefusesPrimaryAndStaleVersion(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	primary := EnsurePrimaryNode(store, "primary", "primary-id")
	node := acceptSecondary(t, store, "secondary-id")
	ctx := apigen.Context{Ctx: context.Background()}
	if _, err := EvictNode(ctx, store, primary.Identifier, primary.Version, true); !errors.Is(err, ErrNodeIsPrimary) {
		t.Fatalf("evict primary: got %v, want ErrNodeIsPrimary", err)
	}
	if _, err := EvictNode(ctx, store, node.Identifier, node.Version+1, true); !errors.Is(err, ErrNodeVersionChanged) {
		t.Fatalf("stale version: got %v, want ErrNodeVersionChanged", err)
	}
	if _, err := SetNodeDraining(ctx, store, primary.Identifier, true); !errors.Is(err, ErrNodeIsPrimary) {
		t.Fatalf("drain primary: got %v, want ErrNodeIsPrimary", err)
	}
}

func TestEvictedIdentifierCanNeverReenroll(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	EnsurePrimaryNode(store, "primary", "primary-id")
	node := acceptSecondary(t, store, "secondary-id")
	if _, err := EvictNode(apigen.Context{Ctx: context.Background()}, store, node.Identifier, node.Version, false); err != nil {
		t.Fatalf("EvictNode: %v", err)
	}
	_, _, err := UpsertEnrollmentRequest(store, "127.0.0.1", "v0.0.2", apigen.NodeReported{Identifier: node.Identifier})
	if !errors.Is(err, ErrEnrollmentIdentifierEvicted) {
		t.Fatalf("re-enrollment of evicted identifier: got %v, want ErrEnrollmentIdentifierEvicted", err)
	}
	if got := nodeRow(t, store, node.Identifier).Status; got != apigen.NodeLifecycleStatus_NODE_MEMBER_EVICTED {
		t.Fatalf("status after refused re-enrollment = %v, want evicted", got)
	}
	if _, _, err := UpsertEnrollmentRequest(store, "127.0.0.1", "v0.0.2", apigen.NodeReported{Identifier: "fresh-id"}); err != nil {
		t.Fatalf("fresh identifier enrollment: %v", err)
	}
}

func TestSetNodeDrainingTogglesCordon(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	EnsurePrimaryNode(store, "primary", "primary-id")
	node := acceptSecondary(t, store, "secondary-id")
	ctx := apigen.Context{Ctx: context.Background()}
	event, err := SetNodeDraining(ctx, store, node.Identifier, true)
	if err != nil || event.Value.Status != apigen.NodeLifecycleStatus_NODE_MEMBER_DRAINING {
		t.Fatalf("drain: event=%+v err=%v", event, err)
	}
	if _, err := MemberNodeIDByIdentifier(store.Queries(), node.Identifier); err != nil {
		t.Fatalf("draining node must stay a member: %v", err)
	}
	event, err = SetNodeDraining(ctx, store, node.Identifier, false)
	if err != nil || event.Value.Status != apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL {
		t.Fatalf("undrain: event=%+v err=%v", event, err)
	}
}

func TestNodeExposureListsDeliveredData(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	EnsurePrimaryNode(store, "primary", "primary-id")
	node := acceptSecondary(t, store, "secondary-id")
	ctx := apigen.Context{Ctx: context.Background()}
	cfg := statetest.MustCreateDeploymentForNode(store, ctx, DefaultSpaceID, "web", node.ID, statetest.EnvRefSpec(map[string]int32{"CONF": 9}, map[string]int32{"SECRET": 7}))
	statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	other := statetest.MustCreateDeploymentForNode(store, ctx, DefaultSpaceID, "elsewhere", node.ID+1, statetest.EnvRefSpec(map[string]int32{"CONF": 10}, nil))
	statetest.CreateScheduledInstance(store, other.DeploymentID, other.Version, node.ID+1, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	if _, err := EvictNode(ctx, store, node.Identifier, node.Version, true); err != nil {
		t.Fatalf("EvictNode: %v", err)
	}
	exposure, err := NodeExposure(context.Background(), store.Queries(), node.ID)
	if err != nil {
		t.Fatalf("NodeExposure: %v", err)
	}
	if len(exposure.Deployments) != 1 || exposure.Deployments[0].DeploymentID != cfg.DeploymentID {
		t.Fatalf("exposed deployments = %+v, want only %d", exposure.Deployments, cfg.DeploymentID)
	}
	if len(exposure.SecretVersionIDs) != 1 || exposure.SecretVersionIDs[0] != 7 {
		t.Fatalf("exposed secrets = %v, want [7]", exposure.SecretVersionIDs)
	}
	if len(exposure.ConfigVersionIDs) != 1 || exposure.ConfigVersionIDs[0] != 9 {
		t.Fatalf("exposed configs = %v, want [9]", exposure.ConfigVersionIDs)
	}
}
