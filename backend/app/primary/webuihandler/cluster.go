package webuihandler

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

func (h *Handler) GetV1ClusterStatus(ctx apigen.Context, r *http.Request, w http.ResponseWriter) error {
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
			ID:          node.ID,
			Name:        node.Name,
			Identifier:  node.Identifier,
			IsPrimary:   nodeHasRole(node, sqlite.NodeRolePrimary),
			Connected:   conn.connected,
			ConnectedAt: conn.connectedAt,
		})
	}
	respond(w, &apigen.ClusterStatusResponse{Machines: machines})
	return nil
}

var InvalidNodeRenameErr = apigen.NewApiErr("Node name and identifier are required", "invalid_node_rename", http.StatusBadRequest)
var NodeNotFoundErr = apigen.NewApiErr("Node not found", "node_not_found", http.StatusNotFound)
var DuplicateNodeNameErr = apigen.NewApiErr("A node with this display name already exists", "duplicate_node_name", http.StatusConflict)

func (h *Handler) PostV1ClusterRename(ctx apigen.Context, req *apigen.NodeRenameRequest) (*apigen.ClusterNode, error) {
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

type timeAndConnected struct {
	connectedAt time.Time
	connected   bool
}

func nodeHasRole(node *sqlite.Node, role int32) bool {
	for _, r := range node.Roles {
		if r == role {
			return true
		}
	}
	return false
}
