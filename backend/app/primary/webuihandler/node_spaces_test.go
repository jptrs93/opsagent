package webuihandler

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func newNodeSpacesHandler(t *testing.T) (*Handler, *state.Node) {
	t.Helper()
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := store.EnsurePrimaryNode("primary", "primary-id")
	return &Handler{ConfigService: &config.Service{}, Store: store}, node
}

func setAllowed(t *testing.T, h *Handler, identifier string, spaces []int32) (*apigen.ClusterNode, error) {
	t.Helper()
	return h.PostV1NodesAllowedSpaces(apigen.Context{Ctx: context.Background()},
		&apigen.NodeAllowedSpacesRequest{Identifier: identifier, SpaceIds: spaces})
}

func TestDeploymentCannotBeCreatedInADisallowedSpace(t *testing.T) {
	h, node := newNodeSpacesHandler(t)
	space, err := h.Store.CreateSpace("staging")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}

	// Creating the space opened it on every node, so placing into it works
	// before anyone narrows anything. This is the default-open half.
	spec := remoteDeploymentSpec("nginx", hostNetworking())
	if _, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: space.ID, Name: "web",
		NodeID: node.ID,
		Spec:   spec,
	}); err != nil {
		t.Fatalf("create before narrowing: %v", err)
	}

	// A second space, narrowed off this node while nothing occupies it.
	fenced, err := h.Store.CreateSpace("fenced")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if _, err := setAllowed(t, h, node.Identifier, []int32{state.DefaultSpaceID, space.ID}); err != nil {
		t.Fatalf("narrowing: %v", err)
	}

	_, err = h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: fenced.ID, Name: "web2",
		NodeID: node.ID,
		Spec:   spec,
	})
	if err == nil || !strings.Contains(err.Error(), "node_space_not_allowed") {
		t.Fatalf("err = %v, want node_space_not_allowed", err)
	}
}

func TestDeploymentCannotMoveIntoADisallowedSpace(t *testing.T) {
	h, node := newNodeSpacesHandler(t)
	space, err := h.Store.CreateSpace("staging")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	spec := remoteDeploymentSpec("nginx", hostNetworking())
	cfg, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: state.DefaultSpaceID, Name: "web",
		NodeID: node.ID,
		Spec:   spec,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := setAllowed(t, h, node.Identifier, []int32{state.DefaultSpaceID}); err != nil {
		t.Fatalf("narrowing: %v", err)
	}

	_, err = h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID:        cfg.ID,
		ExpectedVersion:     cfg.Version + 1,
		AssignedSpaceUpdate: &apigen.AssignedSpaceUpdate{SpaceID: space.ID},
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
	if _, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: state.DefaultSpaceID, Name: "web",
		NodeID: node.ID,
		Spec:   spec,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err := setAllowed(t, h, node.Identifier, nil)
	if err == nil || !strings.Contains(err.Error(), "node_space_in_use") {
		t.Fatalf("err = %v, want node_space_in_use", err)
	}
	// And the stored list is untouched.
	if got := nodeAllowsSpaceForTest(h, node.ID, state.DefaultSpaceID); !got {
		t.Fatal("a rejected narrowing still changed the stored list")
	}
}

func TestSetAllowedSpacesRejectsUnknownAndMissingInput(t *testing.T) {
	h, node := newNodeSpacesHandler(t)

	if _, err := setAllowed(t, h, "  ", nil); err != InvalidAllowedSpacesErr {
		t.Fatalf("blank identifier err = %v, want InvalidAllowedSpacesErr", err)
	}
	if _, err := setAllowed(t, h, "no-such-node", nil); err != NodeNotFoundErr {
		t.Fatalf("unknown node err = %v, want NodeNotFoundErr", err)
	}
	if _, err := setAllowed(t, h, node.Identifier, []int32{999}); err != UnknownSpaceErr {
		t.Fatalf("unknown space err = %v, want UnknownSpaceErr", err)
	}
}

// The deployment create panel loads its node list from the state stream's
// nodes snapshot, so the nodes there must carry the allow list or the panel
// treats every node as disallowing every space.
func TestClusterNodesCarryAllowedSpaces(t *testing.T) {
	h, node := newNodeSpacesHandler(t)
	space, err := h.Store.CreateSpace("staging")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if _, err := setAllowed(t, h, node.Identifier, []int32{space.ID}); err != nil {
		t.Fatalf("narrowing: %v", err)
	}

	var got []int32
	for _, item := range h.Store.ListClusterNodes() {
		if item != nil && item.ID == node.ID {
			got = item.AllowedSpaces
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, []int32{state.OpendeploySpaceID, space.ID}) {
		t.Fatalf("AllowedSpaces = %v, want [%d %d]", got, state.OpendeploySpaceID, space.ID)
	}
}

func TestSetAllowedSpacesAlwaysKeepsTheOpendeploySpace(t *testing.T) {
	h, node := newNodeSpacesHandler(t)

	updated, err := setAllowed(t, h, node.Identifier, nil)
	if err != nil {
		t.Fatalf("SetAllowedSpaces: %v", err)
	}
	if !slices.Equal(updated.AllowedSpaces, []int32{state.OpendeploySpaceID}) {
		t.Fatalf("AllowedSpaces = %v, want just the opendeploy space", updated.AllowedSpaces)
	}
	// Which means an internal deployment can still be placed there.
	if !nodeAllowsSpaceForTest(h, node.ID, state.OpendeploySpaceID) {
		t.Fatal("node stopped allowing the opendeploy space")
	}
}

func nodeAllowsSpaceForTest(h *Handler, nodeID, spaceID int32) bool {
	node := h.Store.LiveState().Nodes[nodeID]
	return node != nil && slices.Contains(node.AllowedSpaces, spaceID)
}
