package webuihandler

import (
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func (h *Handler) GetV1NodesStatus(ctx apigen.Context, r *http.Request, w http.ResponseWriter) error {
	connected := map[int32]timeAndConnected{}
	if h.Cluster != nil {
		for nodeID, connectedAt := range h.Cluster.ConnectedNodes() {
			connected[nodeID] = timeAndConnected{connectedAt: connectedAt, connected: true}
		}
	}
	connected[h.NodeID] = timeAndConnected{connected: true}

	nodes := h.Store.ListNodes()
	machines := make([]*apigen.ClusterMachine, 0, len(nodes))
	for _, node := range nodes {
		if node == nil || node.Name == "" || node.Identifier == "" {
			continue
		}
		conn := connected[node.ID]
		machines = append(machines, &apigen.ClusterMachine{
			ID:            node.ID,
			Name:          node.Name,
			Identifier:    node.Identifier,
			IsPrimary:     nodeHasRole(node, state.NodeRolePrimary),
			Connected:     conn.connected,
			ConnectedAt:   conn.connectedAt,
			AllowedSpaces: node.AllowedSpaces,
		})
	}
	respond(w, &apigen.NodeStatusResponse{Machines: machines})
	return nil
}

var InvalidNodeRenameErr = apigen.NewApiErr("Node name and identifier are required", "invalid_node_rename", http.StatusBadRequest)
var NodeNotFoundErr = apigen.NewApiErr("Node not found", "node_not_found", http.StatusNotFound)
var DuplicateNodeNameErr = apigen.NewApiErr("A node with this display name already exists", "duplicate_node_name", http.StatusConflict)

func (h *Handler) PostV1NodesRename(ctx apigen.Context, req *apigen.NodeRenameRequest) (*apigen.ClusterNode, error) {
	if req == nil {
		return nil, InvalidNodeRenameErr
	}
	identifier := strings.TrimSpace(req.Identifier)
	name := strings.TrimSpace(req.Name)
	if identifier == "" || name == "" {
		return nil, InvalidNodeRenameErr
	}
	node, err := h.Store.RenameNode(identifier, name)
	if err == nil {
		return node, nil
	}
	if err == sql.ErrNoRows {
		return nil, NodeNotFoundErr
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed: nodes.name") {
		return nil, DuplicateNodeNameErr
	}
	return nil, err
}

var InvalidAllowedSpacesErr = apigen.NewApiErr("Node identifier is required", "invalid_allowed_spaces", http.StatusBadRequest)
var UnknownSpaceErr = apigen.NewApiErr("One or more spaces do not exist", "unknown_space", http.StatusBadRequest)

// PostV1NodesAllowedSpaces replaces the set of spaces whose deployments may
// be placed on a node.
func (h *Handler) PostV1NodesAllowedSpaces(ctx apigen.Context, req *apigen.NodeAllowedSpacesRequest) (*apigen.ClusterNode, error) {
	if req == nil {
		return nil, InvalidAllowedSpacesErr
	}
	identifier := strings.TrimSpace(req.Identifier)
	if identifier == "" {
		return nil, InvalidAllowedSpacesErr
	}
	node := h.nodeByIdentifier(identifier)
	if node == nil {
		return nil, NodeNotFoundErr
	}

	// A list naming a space that does not exist is a caller mistake, not a
	// narrowing: accepting it would silently store an id that can never match.
	existing := map[int32]struct{}{}
	for _, space := range h.Store.ListSpaces() {
		existing[space.ID] = struct{}{}
	}
	requested := map[int32]struct{}{}
	for _, id := range req.SpaceIds {
		if _, ok := existing[id]; !ok {
			return nil, UnknownSpaceErr
		}
		requested[id] = struct{}{}
	}
	// The invariant, applied here too so the check below sees the same list
	// that will be stored rather than the one the caller sent.
	requested[state.OpendeploySpaceID] = struct{}{}

	// Narrowing must not contradict what is already placed on the node. This is
	// the same shape as refusing to delete a space with live deployments.
	for _, cfg := range h.Store.FetchDeploymentSnapshot(nil) {
		if cfg.Deleted || cfg.NodeID != node.ID {
			continue
		}
		if _, ok := requested[cfg.Identity.SpaceID]; !ok {
			return nil, apigen.NewApiErr(
				fmt.Sprintf("Deployment %q is already on this node in a space you are removing", cfg.Identity.Name),
				"node_space_in_use", http.StatusConflict)
		}
	}

	spaces := make([]int32, 0, len(requested))
	for id := range requested {
		spaces = append(spaces, id)
	}
	updated, err := h.Store.SetNodeAllowedSpaces(identifier, spaces)
	if err == sql.ErrNoRows {
		return nil, NodeNotFoundErr
	}
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (h *Handler) nodeByIdentifier(identifier string) *state.Node {
	for _, node := range h.Store.ListNodes() {
		if node != nil && node.Identifier == identifier {
			return node
		}
	}
	return nil
}

// nodeAllowsSpace reports whether deployments in spaceID may be placed on
// nodeID. A node the caller cannot resolve is reported as disallowing
// everything; callers check node existence separately and give a better error.
func (h *Handler) nodeAllowsSpace(nodeID, spaceID int32) bool {
	for _, node := range h.Store.ListNodes() {
		if node == nil || node.ID != nodeID {
			continue
		}
		return slices.Contains(node.AllowedSpaces, spaceID)
	}
	return false
}

func (h *Handler) validateNodeAllowsSpace(nodeID, spaceID int32) error {
	if h.nodeAllowsSpace(nodeID, spaceID) {
		return nil
	}
	return apigen.NewApiErr(
		"This node does not allow deployments from that space",
		"node_space_not_allowed", http.StatusConflict)
}

type timeAndConnected struct {
	connectedAt time.Time
	connected   bool
}

func nodeHasRole(node *state.Node, role int32) bool {
	for _, r := range node.Roles {
		if r == role {
			return true
		}
	}
	return false
}
