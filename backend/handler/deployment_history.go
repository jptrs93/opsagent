package handler

import (
	"sort"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (h *Handler) PostV1DeploymentHistory(ctx apigen.Context, req *apigen.DeploymentHistoryRequest) (*apigen.DeploymentHistory, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}

	configs := h.Store.MustFetchDeploymentHistory(req.DeploymentID)
	statuses := h.Store.MustFetchDeploymentStatusHistory(req.DeploymentID)

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
			return entries[i].Config != nil && entries[j].Config == nil
		}
		return ti.After(tj)
	})

	return &apigen.DeploymentHistory{Entries: entries}, nil
}

func entryTime(e *apigen.DeploymentHistoryEntry) time.Time {
	if e.Config != nil {
		return e.Config.UpdatedAt
	}
	return e.Status.Timestamp
}
