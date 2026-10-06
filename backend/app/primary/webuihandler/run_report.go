package webuihandler

import (
	"database/sql"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

var RunNotFoundErr = apigen.NewApiErr("Run not found", "run_not_found", http.StatusNotFound)

func (h *Handler) PostV1DeploymentsRunReport(ctx apigen.Context, req *apigen.DeploymentRunReportRequest) (*apigen.DeploymentRunReport, error) {
	if req.ScheduledInstanceID == 0 || req.Run <= 0 {
		return nil, MissingKeyErr
	}
	event, err := h.Queries.GetScheduledInstance(ctx, req.ScheduledInstanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, deployments.NotFoundErr
	}
	if err != nil {
		return nil, err
	}
	inst := event.Value
	history, err := h.Queries.ListScheduledInstanceStatusHistorySince(ctx, inst.ID, time.Time{})
	if err != nil {
		return nil, err
	}
	var current apigen.ScheduledInstanceStatus
	if len(history) > 0 {
		current = *history[len(history)-1]
	}

	cfg := h.findConfigByID(inst.Deployment.DeploymentID)
	if cfg == nil {
		return nil, deployments.NotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vViewLogs, eDeployment, cfg.Deployment.SpaceID, cfg.Deployment.ID, deployments.NotFoundErr); err != nil {
		return nil, err
	}
	specVersion := cfg.Meta.SpecVersion
	if pinned, err := h.Queries.GetDeploymentVersion(ctx, inst.Deployment.DeploymentID, inst.Deployment.Version); err == nil {
		specVersion = pinned.Meta.SpecVersion
	}

	latest := current.Runner
	currentRun := int32(0)
	if latest.Present {
		currentRun = int32(latest.Value.NumberOfRestarts) + 1
	}
	if req.Run > currentRun {
		return nil, RunNotFoundErr
	}
	running := req.Run == currentRun &&
		(latest.Value.Status == apigen.RunningStatus_RUNNING_STATUS_RUNNING || latest.Value.Status == apigen.RunningStatus_RUNNING_STATUS_STARTING)

	var startedAt time.Time
	var stoppedAt apigen.Maybe[time.Time]
	var exitCode apigen.Maybe[int32]
	var finalStatus apigen.RunningStatus
	found := false
	for _, st := range history {
		if !st.Runner.Present || int32(st.Runner.Value.NumberOfRestarts) != req.Run-1 {
			continue
		}
		r := st.Runner.Value
		found = true
		if startedAt.IsZero() && r.LastRestartAt.Present {
			startedAt = r.LastRestartAt.Value
		}
		if !stoppedAt.Present && (r.Status == apigen.RunningStatus_RUNNING_STATUS_STOPPED || r.Status == apigen.RunningStatus_RUNNING_STATUS_CRASHED) {
			stoppedAt = st.UpdatedAt
			exitCode = r.ExitCode
			finalStatus = r.Status
		}
	}

	report := &apigen.DeploymentRunReport{
		DeploymentID:          inst.Deployment.DeploymentID,
		DeploymentSpecVersion: specVersion,
		DeploymentVersion:     inst.Deployment.Version,
		NodeID:                inst.NodeID,
		InstanceOrdinal:       inst.InstanceOrdinal,
		Run:                   req.Run,
		Running:               running,
		StartedAt:             startedAt,
		StoppedAt:             stoppedAt,
		ExitCode:              exitCode,
		Status:                finalStatus,
	}
	if running {
		report.Status = latest.Value.Status
	}
	if !found {
		report.Warnings = append(report.Warnings, fmt.Sprintf("no status history recorded for run %d", req.Run))
		return report, nil
	}
	if running || !stoppedAt.Present {
		return report, nil
	}

	lq := &apigen.LogQueryRequest{
		DeploymentID:      inst.Deployment.DeploymentID,
		DeploymentVersion: inst.Deployment.Version,
		TimeEnd:           apigen.TimeOf(stoppedAt.Value.Add(time.Minute)),
		Filters: []apigen.LogFilter{
			{Field: "run", Op: "eq", Value: strconv.Itoa(int(req.Run))},
			{Field: "instance", Op: "eq", Value: strconv.Itoa(int(inst.InstanceOrdinal))},
		},
		Limit:      20,
		Order:      "desc",
		IncludeRaw: true,
	}
	if !startedAt.IsZero() {
		lq.TimeStart = apigen.TimeOf(startedAt.Add(-time.Minute))
	}
	var resp *apigen.LogQueryResponse
	switch {
	case inst.NodeID > 0 && inst.NodeID != h.NodeID && h.Cluster != nil:
		resp, err = h.Cluster.RequestLogQuery(ctx, inst.NodeID, lq)
	case h.LogManager != nil:
		resp, err = h.LogManager.Query(ctx, lq)
	default:
		err = fmt.Errorf("log manager is not running")
	}
	if err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("fetching logs failed: %v", err))
		return report, nil
	}
	report.Warnings = append(report.Warnings, resp.Warnings...)
	report.LogLines = make([]string, 0, len(resp.Records))
	for i := len(resp.Records) - 1; i >= 0; i-- {
		rec := resp.Records[i]
		line := rec.Msg
		if len(rec.Raw) > 0 {
			line = string(rec.Raw)
		}
		report.LogLines = append(report.LogLines, strings.TrimRight(line, "\r\n"))
	}
	return report, nil
}
