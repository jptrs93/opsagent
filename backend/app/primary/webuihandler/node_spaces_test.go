package webuihandler

import (
	"context"
	"errors"
	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func newNodeSpacesHandler(t *testing.T) (*Handler, *nodes.Node) {
	t.Helper()
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := ensureTestNode(store, "primary", "primary-id")
	return &Handler{SystemConfig: testConfigService(t, store), Store: store, Queries: store.Queries()}, node
}

func setAllowed(t *testing.T, h *Handler, identifier string, spaces []uint64) (*pq.NodeEvent, error) {
	t.Helper()
	return h.nodesAllowedSpaces(apigen.Context{Ctx: context.Background()},
		&apigen.NodeAllowedSpacesRequest{Identifier: identifier, SpaceIds: spaces})
}

func TestDeploymentCannotBeCreatedInADisallowedSpace(t *testing.T) {
	h, node := newNodeSpacesHandler(t)
	space, err := nodes.CreateSpace(h.Store, "staging", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}

	// Creating the space opened it on every node, so placing into it works
	// before anyone narrows anything. This is the default-open half.
	spec := remoteDeploymentSpec("nginx", hostNetworking())
	if _, err := h.deploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: space.ID, Name: "web",
		Scheduling: apigen.DedicatedScheduling(false, node.ID),
		Spec:       spec,
	}); err != nil {
		t.Fatalf("create before narrowing: %v", err)
	}

	// A second space, narrowed off this node while nothing occupies it.
	fenced, err := nodes.CreateSpace(h.Store, "fenced", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if _, err := setAllowed(t, h, node.Identifier, []uint64{nodes.DefaultSpaceID, space.ID}); err != nil {
		t.Fatalf("narrowing: %v", err)
	}

	_, err = h.deploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: fenced.ID, Name: "web2",
		Scheduling: apigen.DedicatedScheduling(false, node.ID),
		Spec:       spec,
	})
	if err == nil || !strings.Contains(err.Error(), "node_space_not_allowed") {
		t.Fatalf("err = %v, want node_space_not_allowed", err)
	}
}

func TestDeploymentCannotMoveIntoADisallowedSpace(t *testing.T) {
	h, node := newNodeSpacesHandler(t)
	space, err := nodes.CreateSpace(h.Store, "staging", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	spec := remoteDeploymentSpec("nginx", hostNetworking())
	cfg, err := h.deploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: nodes.DefaultSpaceID, Name: "web",
		Scheduling: apigen.DedicatedScheduling(false, node.ID),
		Spec:       spec,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := setAllowed(t, h, node.Identifier, []uint64{nodes.DefaultSpaceID}); err != nil {
		t.Fatalf("narrowing: %v", err)
	}

	_, err = h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  cfg.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{AssignedSpace: &apigen.AssignedSpaceUpdate{SpaceID: space.ID}},
	})
	if err == nil || !strings.Contains(err.Error(), "node_space_not_allowed") {
		t.Fatalf("err = %v, want node_space_not_allowed", err)
	}
}

// Narrowing must not contradict what is already placed on the node, or the
// stored policy would claim something the running cluster disproves.
func TestNarrowingIsRejectedWhileDeploymentsUseTheSpace(t *testing.T) {
	h, node := newNodeSpacesHandler(t)
	spec := remoteDeploymentSpec("nginx", hostNetworking())
	if _, err := h.deploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: nodes.DefaultSpaceID, Name: "web",
		Scheduling: apigen.DedicatedScheduling(false, node.ID),
		Spec:       spec,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err := setAllowed(t, h, node.Identifier, nil)
	if err == nil || !strings.Contains(err.Error(), "node_space_in_use") {
		t.Fatalf("err = %v, want node_space_in_use", err)
	}
	// And the stored list is untouched.
	if got := nodeAllowsSpaceForTest(h, node.ID, nodes.DefaultSpaceID); !got {
		t.Fatal("a rejected narrowing still changed the stored list")
	}
}

func TestSetAllowedSpacesRejectsUnknownAndMissingInput(t *testing.T) {
	h, node := newNodeSpacesHandler(t)

	if _, err := setAllowed(t, h, "  ", nil); !errors.Is(err, InvalidAllowedSpacesErr) {
		t.Fatalf("blank identifier err = %v, want InvalidAllowedSpacesErr", err)
	}
	if _, err := setAllowed(t, h, "no-such-node", nil); !errors.Is(err, NodeNotFoundErr) {
		t.Fatalf("unknown node err = %v, want NodeNotFoundErr", err)
	}
	if _, err := setAllowed(t, h, node.Identifier, []uint64{999}); !errors.Is(err, UnknownSpaceErr) {
		t.Fatalf("unknown space err = %v, want UnknownSpaceErr", err)
	}
}

// The deployment create panel loads its node list from the state stream's
// nodes snapshot, so the nodes there must carry the allow list or the panel
// treats every node as disallowing every space.
func TestClusterNodesCarryAllowedSpaces(t *testing.T) {
	h, node := newNodeSpacesHandler(t)
	space, err := nodes.CreateSpace(h.Store, "staging", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if _, err := setAllowed(t, h, node.Identifier, []uint64{space.ID}); err != nil {
		t.Fatalf("narrowing: %v", err)
	}

	var got []uint64
	for _, row := range erru.Must(h.Store.Queries().ListNodeRows(context.Background(), pq.MemberNodeStatuses)) {
		if row.Event.NodeID == node.ID {
			got = row.Event.Value.Operator.AllowedSpaces
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, []uint64{internaldeploy.SpaceID, space.ID}) {
		t.Fatalf("AllowedSpaces = %v, want [%d %d]", got, internaldeploy.SpaceID, space.ID)
	}
}

func TestSetAllowedSpacesAlwaysKeepsTheOpendeploySpace(t *testing.T) {
	h, node := newNodeSpacesHandler(t)

	updated, err := setAllowed(t, h, node.Identifier, nil)
	if err != nil {
		t.Fatalf("SetAllowedSpaces: %v", err)
	}
	if !slices.Equal(updated.Value.Operator.AllowedSpaces, []uint64{internaldeploy.SpaceID}) {
		t.Fatalf("AllowedSpaces = %v, want just the opendeploy space", updated.Value.Operator.AllowedSpaces)
	}
	// Which means an internal deployment can still be placed there.
	if !nodeAllowsSpaceForTest(h, node.ID, internaldeploy.SpaceID) {
		t.Fatal("node stopped allowing the opendeploy space")
	}
}

func nodeAllowsSpaceForTest(h *Handler, nodeID, spaceID uint64) bool {
	node := liveNodes(h.Store.Queries()).Nodes[nodeID]
	return node != nil && slices.Contains(node.AllowedSpaces, spaceID)
}

func liveNodes(q *pq.Queries) nodes.LiveState {
	return erru.Must(nodes.ReadLiveState(context.Background(), q))
}
