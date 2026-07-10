package handler

import (
	"net/http"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

func (h *Handler) GetV1ClusterStatus(ctx apigen.Context, r *http.Request, w http.ResponseWriter) error {
	connected := map[string]timeAndConnected{}
	if h.ClusterPrimary != nil {
		for name, connectedAt := range h.ClusterPrimary.ConnectedMachines() {
			connected[name] = timeAndConnected{connectedAt: connectedAt, connected: true}
		}
	}
	connected[h.MachineName] = timeAndConnected{connected: true}

	nodes := h.Store.ListNodes()
	machines := make([]*apigen.ClusterMachine, 0, len(nodes))
	for _, node := range nodes {
		if node == nil || node.Name == "" {
			continue
		}
		conn := connected[node.Name]
		machines = append(machines, &apigen.ClusterMachine{
			Name:        node.Name,
			IsPrimary:   nodeHasRole(node, sqlite.NodeRolePrimary),
			Connected:   conn.connected,
			ConnectedAt: conn.connectedAt,
		})
	}
	if len(machines) == 0 {
		machines = append(machines, &apigen.ClusterMachine{Name: h.MachineName, IsPrimary: true, Connected: true})
	}

	respond(w, &apigen.ClusterStatusResponse{Machines: machines})
	return nil
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
