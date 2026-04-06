package handler

import (
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// GetV1DeploymentHistory returns the config history for a single deployment
// identified by env + machine + name query params. All three are required.
func (h *Handler) GetV1DeploymentHistory(ctx apigen.Context, r *http.Request, w http.ResponseWriter) error {
	env := r.URL.Query().Get("environment")
	name := r.URL.Query().Get("deployment")
	machine := r.URL.Query().Get("machine")
	if env == "" || name == "" || machine == "" {
		http.Error(w, "missing environment/deployment/machine query param", http.StatusBadRequest)
		return nil
	}

	entries := h.Store.MustFetchDeploymentHistory(apigen.DeploymentIdentifier{
		Environment: env,
		Machine:     machine,
		Name:        name,
	})

	respond(w, &apigen.DeploymentHistory{Entries: entries})
	return nil
}
