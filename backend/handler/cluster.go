package handler

import (
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (h *Handler) GetV1ClusterStatus(ctx apigen.Context, r *http.Request, w http.ResponseWriter) error {
	machines := []*apigen.ClusterMachine{
		{
			Name:      h.MachineName,
			IsPrimary: true,
			Connected: true,
		},
	}

	if h.ClusterPrimary != nil {
		for name, connectedAt := range h.ClusterPrimary.ConnectedMachines() {
			machines = append(machines, &apigen.ClusterMachine{
				Name:        name,
				Connected:   true,
				ConnectedAt: connectedAt,
			})
		}
	}

	respond(w, &apigen.ClusterStatusResponse{Machines: machines})
	return nil
}
