package webuihandler

import (
	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
)

func (h *Handler) GetV1GlobalSnapshot(ctx apigen.Context) (*apigen.Snapshot, error) {
	return h.visibleSnapshot(ctx, h.snapshotWithSidecars(ctx)), nil
}

func (h *Handler) snapshotWithSidecars(ctx apigen.Context) *apigen.Snapshot {
	snapshot := h.Store.BuildSnapshot(ctx)
	status, ok := h.secretsUpdates.ValueOK()
	if !ok {
		status = h.secretsStatus()
	}
	snapshot.SecretsStatus = &status
	snapshot.BackupStatus = &apigen.BackupStatus{}
	if h.BackupStatus != nil {
		status := h.BackupStatus.Snapshot()
		snapshot.BackupStatus = &status
	}
	if ctx.User != nil {
		snapshot.AgentSessions = erru.Must(h.agentSessions().Snapshot(ctx.User.ID))
	}
	if h.IngressDiagnostics != nil {
		initial, _, unsubscribe := h.IngressDiagnostics.DiagnosticsSnapshotAndSubscribe()
		unsubscribe()
		snapshot.IngressDiagnostics = initial
	}
	return snapshot
}

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
	snapshot := h.Store.BuildSnapshot(ctx)
	out := &apigen.DeploymentGetResponse{DeploymentEvent: cfg}
	for _, e := range snapshot.ScheduledInstanceEvents {
		if e.Value.DeploymentID == req.ID {
			out.ScheduledInstanceEvents = append(out.ScheduledInstanceEvents, e)
		}
	}
	for _, status := range snapshot.InstanceStatuses {
		if status.DeploymentID == req.ID {
			out.InstanceStatuses = append(out.InstanceStatuses, status)
		}
	}
	return out, nil
}
