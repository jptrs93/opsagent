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
	if req.ScheduledInstanceID <= 0 || req.Run <= 0 {
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

	cfg := h.findConfigByID(inst.DeploymentID)
	if cfg == nil {
		return nil, deployments.NotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vViewLogs, eDeployment, int64(cfg.Value.SpaceID), int64(cfg.DeploymentID), deployments.NotFoundErr); err != nil {
		return nil, err
	}

	latest := current.Runner
	currentRun := int32(0)
	if !latest.IsZero() {
		currentRun = latest.NumberOfRestarts + 1
	}
	if req.Run > currentRun {
		return nil, RunNotFoundErr
	}
	running := req.Run == currentRun &&
		(latest.Status == apigen.RunningStatus_RUNNING || latest.Status == apigen.RunningStatus_STARTING)

	var startedAt, stoppedAt time.Time
	var exitCode *int32
	var finalStatus apigen.RunningStatus
	found := false
	for _, st := range history {
		r := st.Runner
		if r.IsZero() || r.NumberOfRestarts != req.Run-1 {
			continue
		}
		found = true
		if startedAt.IsZero() {
			startedAt = r.LastRestartAt
		}
		if stoppedAt.IsZero() && (r.Status == apigen.RunningStatus_STOPPED || r.Status == apigen.RunningStatus_CRASHED) {
			stoppedAt = st.UpdatedAt
			exitCode = r.ExitCode
			finalStatus = r.Status
		}
	}

	report := &apigen.DeploymentRunReport{
		DeploymentID:          inst.DeploymentID,
		DeploymentSpecVersion: inst.DeploymentSpecVersion,
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
		report.Status = latest.Status
	}
	if !found {
		report.Warnings = append(report.Warnings, fmt.Sprintf("no status history recorded for run %d", req.Run))
		return report, nil
	}
	if running || stoppedAt.IsZero() {
		return report, nil
	}

	lq := &apigen.LogQueryRequest{
		DeploymentID: inst.DeploymentID,
		SpecVersion:  inst.DeploymentSpecVersion,
		TimeEnd:      stoppedAt.Add(time.Minute),
		Filters: []*apigen.LogFilter{
			{Field: "run", Op: "eq", Value: strconv.Itoa(int(req.Run))},
			{Field: "instance", Op: "eq", Value: strconv.Itoa(int(inst.InstanceOrdinal))},
		},
		Limit:      20,
		Order:      "desc",
		IncludeRaw: true,
	}
	if !startedAt.IsZero() {
		lq.TimeStart = startedAt.Add(-time.Minute)
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
