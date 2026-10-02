package webuihandler

import (
	"database/sql"
	"errors"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func (h *Handler) PostV1DeploymentsGet(ctx apigen.Context, req *apigen.DeploymentGetRequest) (*apigen.DeploymentGetResponse, error) {
	if req.ID <= 0 {
		return nil, MissingKeyErr
	}
	cfg := h.deploymentByID(req.ID)
	if cfg == nil || cfg.Deleted() {
		return nil, deployments.NotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vView, eDeployment, int64(cfg.Value.SpaceID), int64(cfg.DeploymentID), deployments.NotFoundErr); err != nil {
		return nil, err
	}
	q := h.Store.Queries()
	instances, err := q.ListRetainedScheduledInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := &apigen.DeploymentGetResponse{Deployment: pq.DeploymentRecord(cfg)}
	for _, e := range instances {
		if e.Value.DeploymentID != req.ID {
			continue
		}
		out.ScheduledInstances = append(out.ScheduledInstances, &e.Value)
		status, err := q.GetLatestScheduledInstanceStatus(ctx, e.ScheduledInstanceID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out.InstanceStatuses = append(out.InstanceStatuses, status)
	}
	return out, nil
}
