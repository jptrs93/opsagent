package webuihandler

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

func newNodeSpacesHandler(t *testing.T) (*Handler, *sqlite.Node) {
	t.Helper()
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	node := store.EnsurePrimaryNode("primary", "primary-id")
	return &Handler{Store: store}, node
}

func setAllowed(t *testing.T, h *Handler, identifier string, spaces []int32) (*apigen.ClusterNode, error) {
	t.Helper()
	return h.PostV1ClusterAllowedSpaces(apigen.Context{Ctx: context.Background()},
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
	if _, err := h.PostV1DeploymentCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: space.ID, Name: "web"},
		NodeID:   node.ID,
		Spec:     spec,
	}); err != nil {
		t.Fatalf("create before narrowing: %v", err)
	}

	// A second space, narrowed off this node while nothing occupies it.
	fenced, err := h.Store.CreateSpace("fenced")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if _, err := setAllowed(t, h, node.Identifier, []int32{sqlite.DefaultSpaceID, space.ID}); err != nil {
		t.Fatalf("narrowing: %v", err)
	}

	_, err = h.PostV1DeploymentCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: fenced.ID, Name: "web2"},
		NodeID:   node.ID,
		Spec:     spec,
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
	cfg, err := h.PostV1DeploymentCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: sqlite.DefaultSpaceID, Name: "web"},
		NodeID:   node.ID,
		Spec:     spec,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := setAllowed(t, h, node.Identifier, []int32{sqlite.DefaultSpaceID}); err != nil {
		t.Fatalf("narrowing: %v", err)
	}

	target := space.ID
	_, err = h.PostV1DeploymentUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.ID,
		Version:      cfg.Version + 1,
		SpaceID:      &target,
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
	if _, err := h.PostV1DeploymentCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		Identity: apigen.DeploymentIdentity{SpaceID: sqlite.DefaultSpaceID, Name: "web"},
		NodeID:   node.ID,
		Spec:     spec,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err := setAllowed(t, h, node.Identifier, nil)
	if err == nil || !strings.Contains(err.Error(), "node_space_in_use") {
		t.Fatalf("err = %v, want node_space_in_use", err)
	}
	// And the stored list is untouched.
	if got := h.nodeAllowsSpace(node.ID, sqlite.DefaultSpaceID); !got {
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

func TestSetAllowedSpacesAlwaysKeepsTheOpendeploySpace(t *testing.T) {
	h, node := newNodeSpacesHandler(t)

	updated, err := setAllowed(t, h, node.Identifier, nil)
	if err != nil {
		t.Fatalf("SetAllowedSpaces: %v", err)
	}
	if !slices.Equal(updated.AllowedSpaces, []int32{sqlite.OpendeploySpaceID}) {
		t.Fatalf("AllowedSpaces = %v, want just the opendeploy space", updated.AllowedSpaces)
	}
	// Which means an internal deployment can still be placed there.
	if !h.nodeAllowsSpace(node.ID, sqlite.OpendeploySpaceID) {
		t.Fatal("node stopped allowing the opendeploy space")
	}
}
