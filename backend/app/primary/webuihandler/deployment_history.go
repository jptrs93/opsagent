package webuihandler

import (
	"sort"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
)

func (h *Handler) PostV1DeploymentsHistory(ctx apigen.Context, req *apigen.DeploymentHistoryRequest) (*apigen.DeploymentHistory, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}
	cfg := h.findConfigByID(req.DeploymentID)
	if cfg == nil {
		return nil, deployments.NotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vView, eDeployment, cfg.Deployment.SpaceID, cfg.Deployment.ID, deployments.NotFoundErr); err != nil {
		return nil, err
	}

	configs, err := h.Queries.ListDeploymentHistory(ctx, req.DeploymentID)
	if err != nil {
		return nil, err
	}
	statuses, err := h.Queries.ListScheduledInstanceStatusHistoryForDeployment(ctx, req.DeploymentID)
	if err != nil {
		return nil, err
	}

	entries := make([]apigen.DeploymentHistoryEntry, 0, len(configs)+len(statuses))
	for _, c := range configs {
		entries = append(entries, apigen.DeploymentHistoryEntry{Value: apigen.DeploymentHistoryEntryValueOneof{Deployment: c}})
	}
	for _, s := range statuses {
		entries = append(entries, apigen.DeploymentHistoryEntry{Value: apigen.DeploymentHistoryEntryValueOneof{Status: s}})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		ti, tj := entryTime(&entries[i]), entryTime(&entries[j])
		if ti.Equal(tj) {
			return entries[i].Value.Deployment != nil && entries[j].Value.Deployment == nil
		}
		return ti.After(tj)
	})

	return &apigen.DeploymentHistory{Entries: entries}, nil
}

func entryTime(e *apigen.DeploymentHistoryEntry) time.Time {
	if e.Value.Deployment != nil {
		return time.UnixMilli(e.Value.Deployment.Meta.UpdatedTime)
	}
	return e.Value.Status.UpdatedAt.Value
}
