package webuihandler

import (
	"database/sql"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
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

var NodeNotMemberErr = apigen.NewApiErr("Node is not a cluster member", "node_not_member", http.StatusConflict)
var NodeIsPrimaryErr = apigen.NewApiErr("The primary node cannot be drained or evicted", "node_is_primary", http.StatusBadRequest)
var NodeVersionChangedErr = apigen.NewApiErr("The node changed while you were looking at it; reload and try again", "node_version_changed", http.StatusConflict)
var InvalidNodeRequestErr = apigen.NewApiErr("Node identifier is required", "invalid_node_request", http.StatusBadRequest)

func (h *Handler) visibleMemberNode(ctx apigen.Context, identifier string) (*nodes.Node, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return nil, InvalidNodeRequestErr
	}
	node := h.nodeByIdentifier(identifier)
	if node == nil || !h.nodeVisible(ctx, int64(node.ID), node.AllowedSpaces) {
		return nil, NodeNotFoundErr
	}
	return node, nil
}

func mapNodeLifecycleErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return NodeNotFoundErr
	case errors.Is(err, nodes.ErrNodeNotMember):
		return NodeNotMemberErr
	case errors.Is(err, nodes.ErrNodeIsPrimary):
		return NodeIsPrimaryErr
	case errors.Is(err, nodes.ErrNodeVersionChanged):
		return NodeVersionChangedErr
	}
	var hasDeployments *nodes.ErrNodeHasDeployments
	if errors.As(err, &hasDeployments) {
		return apigen.NewApiErr(
			fmt.Sprintf("%d deployments still target this node; move or stop them first, or evict anyway", hasDeployments.Count),
			"node_has_deployments", http.StatusConflict)
	}
	return err
}

func (h *Handler) PostV1NodesDrain(ctx apigen.Context, req *apigen.NodeDrainRequest) (*apigen.NodeEvent, error) {
	if req == nil {
		return nil, InvalidNodeRequestErr
	}
	node, err := h.visibleMemberNode(ctx, req.Identifier)
	if err != nil {
		return nil, err
	}
	if err := h.requireAccess(ctx, vUpdate, eNode, 0, int64(node.ID)); err != nil {
		return nil, err
	}
	event, err := nodes.SetNodeDraining(ctx, h.Store, node.Identifier, req.Draining)
	return event, mapNodeLifecycleErr(err)
}

func (h *Handler) PostV1NodesEvict(ctx apigen.Context, req *apigen.NodeEvictRequest) (*apigen.NodeEvent, error) {
	if req == nil {
		return nil, InvalidNodeRequestErr
	}
	node, err := h.visibleMemberNode(ctx, req.Identifier)
	if err != nil {
		return nil, err
	}
	if err := h.requireAccess(ctx, vDelete, eNode, 0, int64(node.ID)); err != nil {
		return nil, err
	}
	event, err := nodes.EvictNode(ctx, h.Store, node.Identifier, req.ExpectedVersion, req.Force)
	if err != nil {
		return nil, mapNodeLifecycleErr(err)
	}
	return event, nil
}

func (h *Handler) PostV1NodesExposure(ctx apigen.Context, req *apigen.NodeExposureRequest) (*apigen.NodeExposure, error) {
	if req == nil || strings.TrimSpace(req.Identifier) == "" {
		return nil, InvalidNodeRequestErr
	}
	row, err := h.Queries.GetNodeRowByIdentifier(ctx, strings.TrimSpace(req.Identifier))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NodeNotFoundErr
	}
	if err != nil {
		return nil, err
	}
	nodeID := row.Event.NodeID
	if !h.nodeVisible(ctx, int64(nodeID), row.Event.Value.Operator.AllowedSpaces) {
		return nil, NodeNotFoundErr
	}
	if err := h.requireAccess(ctx, vDelete, eNode, 0, int64(nodeID)); err != nil {
		return nil, err
	}
	exposure, err := nodes.NodeExposure(ctx, h.Queries, nodeID)
	if err != nil {
		return nil, err
	}
	out := &apigen.NodeExposure{NodeID: nodeID, GithubToken: exposure.GithubToken, AcmeHostnames: exposure.AcmeHostnames}
	for _, cfg := range exposure.Deployments {
		out.Deployments = append(out.Deployments, &apigen.NodeExposureItem{ID: cfg.DeploymentID, Name: cfg.Value.Name, SpaceID: cfg.Value.SpaceID, Version: cfg.Version})
	}
	for _, cfg := range exposure.IssuedTLSDeployments {
		out.IssuedTlsDeployments = append(out.IssuedTlsDeployments, &apigen.NodeExposureItem{ID: cfg.DeploymentID, Name: cfg.Value.Name, SpaceID: cfg.Value.SpaceID, Version: cfg.Version})
	}
	for _, id := range exposure.SecretVersionIDs {
		item := &apigen.NodeExposureItem{ID: id}
		if h.Secrets != nil {
			if meta, ok := h.Secrets.MetaByID(id); ok {
				item.Name, item.SpaceID, item.Version = meta.Name, meta.SpaceID, meta.Version
			}
		}
		out.Secrets = append(out.Secrets, item)
	}
	for _, id := range exposure.ConfigVersionIDs {
		item := &apigen.NodeExposureItem{ID: id}
		if ref, ok := values.GetConfigVersion(h.Queries, id); ok {
			item.Name, item.SpaceID, item.Version = ref.Name, ref.SpaceID, ref.Version
		}
		out.Configs = append(out.Configs, item)
	}
	return out, nil
}

func (h *Handler) nodeByIdentifier(identifier string) *nodes.Node {
	for _, node := range nodes.ListNodes(h.Store.Queries()) {
		if node != nil && node.Identifier == identifier {
			return node
		}
	}
	return nil
}
