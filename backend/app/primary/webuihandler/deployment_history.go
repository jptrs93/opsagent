package webuihandler

import (
	"sort"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (h *Handler) PostV1DeploymentsHistory(ctx apigen.Context, req *apigen.DeploymentHistoryRequest) (*apigen.DeploymentHistory, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}
	cfg := h.findConfigByID(req.DeploymentID)
	if cfg == nil {
		return nil, DeploymentNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vView, eDeployment, int64(cfg.Identity.SpaceID), int64(cfg.ID), DeploymentNotFoundErr); err != nil {
		return nil, err
	}

	configs := h.Store.MustFetchDeploymentHistory(req.DeploymentID)
	statuses := h.Store.MustFetchDeploymentStatusHistory(req.DeploymentID)

	entries := make([]*apigen.DeploymentHistoryEntry, 0, len(configs)+len(statuses))
	for _, c := range configs {
		entries = append(entries, &apigen.DeploymentHistoryEntry{Config: redactDeploymentConfig(c)})
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
	return e.Status.UpdatedAt
}
