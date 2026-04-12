package handler

import (
	"net/http"
	"sort"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// GetV1DeploymentHistory returns the merged config + status history for a
// single deployment identified by env + machine + name query params.
func (h *Handler) GetV1DeploymentHistory(ctx apigen.Context, r *http.Request, w http.ResponseWriter) error {
	env := r.URL.Query().Get("environment")
	name := r.URL.Query().Get("deployment")
	machine := r.URL.Query().Get("machine")
	if env == "" || name == "" || machine == "" {
		http.Error(w, "missing environment/deployment/machine query param", http.StatusBadRequest)
		return nil
	}

	id := apigen.DeploymentIdentifier{
		Environment: env,
		Machine:     machine,
		Name:        name,
	}

	configs := h.Store.MustFetchDeploymentHistory(id)
	statuses := h.Store.MustFetchDeploymentStatusHistory(id)

	entries := make([]*apigen.DeploymentHistoryEntry, 0, len(configs)+len(statuses))
	for _, c := range configs {
		entries = append(entries, &apigen.DeploymentHistoryEntry{Config: c})
	}
	for _, s := range statuses {
		entries = append(entries, &apigen.DeploymentHistoryEntry{Status: s})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		ti, tj := entryTime(entries[i]), entryTime(entries[j])
		if ti.Equal(tj) {
			// On tie, config changes sort before status changes.
			return entries[i].Config != nil && entries[j].Config == nil
		}
		return ti.After(tj)
	})

	respond(w, &apigen.DeploymentHistory{Entries: entries})
	return nil
}

func entryTime(e *apigen.DeploymentHistoryEntry) time.Time {
	if e.Config != nil {
		return e.Config.UpdatedAt
	}
	return e.Status.Timestamp
}
