package webuihandler

import (
	"database/sql"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

var InvalidNodeRenameErr = apigen.NewApiErr("Node name and identifier are required", "invalid_node_rename", http.StatusBadRequest)
var NodeNotFoundErr = apigen.NewApiErr("Node not found", "node_not_found", http.StatusNotFound)
var DuplicateNodeNameErr = apigen.NewApiErr("A node with this display name already exists", "duplicate_node_name", http.StatusConflict)

func (h *Handler) PostV1NodesList(ctx apigen.Context) (*apigen.NodeEventList, error) {
	return &apigen.NodeEventList{Items: h.filterNodes(ctx, nodes.ListClusterNodes(h.Store.Queries()))}, nil
}

func (h *Handler) PostV1NodesRename(ctx apigen.Context, req *apigen.NodeRenameRequest) (*apigen.NodeEvent, error) {
	if req == nil {
		return nil, InvalidNodeRenameErr
	}
	identifier := strings.TrimSpace(req.Identifier)
	name := strings.TrimSpace(req.Name)
	if identifier == "" || name == "" {
		return nil, InvalidNodeRenameErr
	}
	existing := h.nodeByIdentifier(identifier)
	if existing == nil {
		return nil, NodeNotFoundErr
	}
	// Derived visibility counts as viewable: a space operator who can see the
	// node gets a 403 here, not a 404.
	if !h.nodeVisible(ctx, int64(existing.ID), existing.AllowedSpaces) {
		return nil, NodeNotFoundErr
	}
	if err := h.requireAccess(ctx, vUpdate, eNode, 0, int64(existing.ID)); err != nil {
		return nil, err
	}
	node, err := nodes.RenameNode(h.Store, identifier, name)
	if err == nil {
		return node, nil
	}
	if err == sql.ErrNoRows {
		return nil, NodeNotFoundErr
	}
	if errors.Is(err, nodes.ErrDuplicateNodeName) {
		return nil, DuplicateNodeNameErr
	}
	return nil, err
}

var InvalidAllowedSpacesErr = apigen.NewApiErr("Node identifier is required", "invalid_allowed_spaces", http.StatusBadRequest)
var UnknownSpaceErr = apigen.NewApiErr("One or more spaces do not exist", "unknown_space", http.StatusBadRequest)

// PostV1NodesAllowedSpaces replaces the set of spaces whose deployments may
// be placed on a node.
func (h *Handler) PostV1NodesAllowedSpaces(ctx apigen.Context, req *apigen.NodeAllowedSpacesRequest) (*apigen.NodeEvent, error) {
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
	if !h.nodeVisible(ctx, int64(node.ID), node.AllowedSpaces) {
		return nil, NodeNotFoundErr
	}
	if err := h.requireAccess(ctx, vUpdate, eNode, 0, int64(node.ID)); err != nil {
		return nil, err
	}

	// A list naming a space that does not exist is a caller mistake, not a
	// narrowing: accepting it would silently store an id that can never match.
	existing := map[int32]struct{}{}
	for _, space := range nodes.ListSpaces(h.Store.Queries()) {
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
	requested[internaldeploy.SpaceID] = struct{}{}

	// Narrowing must not contradict what is already placed on the node. This is
	// the same shape as refusing to delete a space with live deployments.
	for _, cfg := range deployments.Active(h.Queries, nil) {
		if cfg.Value.NodeID != node.ID {
			continue
		}
		if _, ok := requested[cfg.Value.SpaceID]; !ok {
			return nil, apigen.NewApiErr(
				fmt.Sprintf("Deployment %q is already on this node in a space you are removing", cfg.Value.Name),
				"node_space_in_use", http.StatusConflict)
		}
	}

	spaces := make([]int32, 0, len(requested))
	for id := range requested {
		spaces = append(spaces, id)
	}
	updated, err := nodes.SetNodeAllowedSpaces(h.Store, identifier, spaces)
	if err == sql.ErrNoRows {
		return nil, NodeNotFoundErr
	}
	if err != nil {
		return nil, err
	}
	// The allow list feeds derived node visibility, and a viewer who just lost
	// a node has no pending update to take it away — only a full re-filter of
	// each open stream removes (or reveals) the row.
	return updated, nil
}

func (h *Handler) nodeByIdentifier(identifier string) *nodes.Node {
	for _, node := range nodes.ListNodes(h.Store.Queries()) {
		if node != nil && node.Identifier == identifier {
			return node
		}
	}
	return nil
}
