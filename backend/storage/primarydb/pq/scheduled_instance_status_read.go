package pq

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
)

const scheduledInstanceStatusColumns = `scheduled_instance_id, updated_at, deployment_id,
 preparer_spec_version, preparer_artifact, preparer_inputs_status, preparer_image_status,
 runner_spec_version, runner_pid, runner_artifact, runner_status, runner_num_restarts, runner_last_restart_at, runner_extra_blob, runner_exit_code`

const scheduledInstanceStatusColumnsS = `s.scheduled_instance_id, s.updated_at, s.deployment_id,
 s.preparer_spec_version, s.preparer_artifact, s.preparer_inputs_status, s.preparer_image_status,
 s.runner_spec_version, s.runner_pid, s.runner_artifact, s.runner_status, s.runner_num_restarts, s.runner_last_restart_at, s.runner_extra_blob, s.runner_exit_code`

type scheduledInstanceStatusRow struct {
	scheduledInstanceID  sql.NullInt64
	updatedAt            sql.NullInt64
	deploymentID         sql.NullInt64
	preparerSpecVersion  sql.NullInt64
	preparerArtifact     sql.NullString
	preparerInputsStatus sql.NullInt64
	preparerImageStatus  sql.NullInt64
	runnerSpecVersion    sql.NullInt64
	runnerPid            sql.NullInt64
	runnerArtifact       sql.NullString
	runnerStatus         sql.NullInt64
	runnerNumRestarts    sql.NullInt64
	runnerLastRestartAt  sql.NullInt64
	runnerExtraBlob      []byte
	runnerExitCode       sql.NullInt64
}

func (r *scheduledInstanceStatusRow) fields() []any {
	return []any{&r.scheduledInstanceID, &r.updatedAt, &r.deploymentID,
		&r.preparerSpecVersion, &r.preparerArtifact, &r.preparerInputsStatus, &r.preparerImageStatus,
		&r.runnerSpecVersion, &r.runnerPid, &r.runnerArtifact, &r.runnerStatus, &r.runnerNumRestarts, &r.runnerLastRestartAt, &r.runnerExtraBlob, &r.runnerExitCode}
}

func (r *scheduledInstanceStatusRow) toStatus() *apigen.ScheduledInstanceStatus {
	if !r.scheduledInstanceID.Valid {
		return nil
	}
	st := &apigen.ScheduledInstanceStatus{
		UpdatedAt:           nanosToClock(r.updatedAt.Int64),
		ScheduledInstanceID: int32(r.scheduledInstanceID.Int64),
		DeploymentID:        int32(r.deploymentID.Int64),
	}
	if r.preparerSpecVersion.Valid {
		st.Preparer = apigen.PreparerStatus{
			DeploymentSpecVersion: int32(r.preparerSpecVersion.Int64),
			Artifact:              r.preparerArtifact.String,
			Inputs:                apigen.InputsStatus(r.preparerInputsStatus.Int64),
			Image:                 apigen.ImageStatus(r.preparerImageStatus.Int64),
		}
	}
	if r.runnerStatus.Valid {
		st.Runner = apigen.RunnerStatus{
			DeploymentSpecVersion: int32(r.runnerSpecVersion.Int64),
			RunningPid:            int32(r.runnerPid.Int64),
			RunningArtifact:       r.runnerArtifact.String,
			Status:                apigen.RunningStatus(r.runnerStatus.Int64),
			NumberOfRestarts:      int32(r.runnerNumRestarts.Int64),
		}
		if r.runnerLastRestartAt.Valid {
			st.Runner.LastRestartAt = time.UnixMilli(r.runnerLastRestartAt.Int64)
		}
		if r.runnerExitCode.Valid {
			code := int32(r.runnerExitCode.Int64)
			st.Runner.ExitCode = &code
		}
		if len(r.runnerExtraBlob) > 0 {
			extra, err := apigen.DecodeRunnerStatus(r.runnerExtraBlob)
			if err != nil {
				slog.WarnContext(logu.AddTag(context.Background(), "Store"), "decoding runner status extra blob",
					"scheduled_instance", st.ScheduledInstanceID, "err", err)
			} else {
				st.Runner.NetworkDiagnostics = extra.NetworkDiagnostics
			}
		}
	}
	return st
}

func scanScheduledInstanceStatus(row scanner) (*apigen.ScheduledInstanceStatus, error) {
	var r scheduledInstanceStatusRow
	if err := row.Scan(r.fields()...); err != nil {
		return nil, err
	}
	return r.toStatus(), nil
}

func (q *Queries) queryScheduledInstanceStatuses(ctx context.Context, query string, args ...any) ([]*apigen.ScheduledInstanceStatus, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.ScheduledInstanceStatus
	for rows.Next() {
		st, err := scanScheduledInstanceStatus(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (q *Queries) GetLatestScheduledInstanceStatus(ctx context.Context, id int32) (*apigen.ScheduledInstanceStatus, error) {
	return scanScheduledInstanceStatus(q.db.QueryRowContext(ctx, `SELECT `+scheduledInstanceStatusColumns+` FROM scheduled_instance_status
 WHERE scheduled_instance_id = ? ORDER BY updated_at DESC LIMIT 1`, id))
}

func (q *Queries) ListLatestScheduledInstanceStatuses(ctx context.Context) ([]*apigen.ScheduledInstanceStatus, error) {
	return q.queryScheduledInstanceStatuses(ctx, `SELECT `+scheduledInstanceStatusColumnsS+` FROM scheduled_instance_status s
 JOIN (SELECT scheduled_instance_id, MAX(updated_at) AS updated_at FROM scheduled_instance_status GROUP BY scheduled_instance_id) latest
 ON latest.scheduled_instance_id = s.scheduled_instance_id AND latest.updated_at = s.updated_at
 ORDER BY s.scheduled_instance_id`)
}

func (q *Queries) ListScheduledInstanceStatusHistorySince(ctx context.Context, id int32, since time.Time) ([]*apigen.ScheduledInstanceStatus, error) {
	return q.queryScheduledInstanceStatuses(ctx, `SELECT `+scheduledInstanceStatusColumns+` FROM scheduled_instance_status
 WHERE scheduled_instance_id = ? AND updated_at > ? ORDER BY updated_at ASC`, id, clockToNanos(since))
}

func (q *Queries) ListScheduledInstanceStatusHistoryForDeployment(ctx context.Context, deploymentID int32) ([]*apigen.ScheduledInstanceStatus, error) {
	return q.queryScheduledInstanceStatuses(ctx, `SELECT `+scheduledInstanceStatusColumns+` FROM scheduled_instance_status
 WHERE deployment_id = ? ORDER BY updated_at ASC`, deploymentID)
}

func (q *Queries) ListScheduledInstanceStatusesAtSeq(ctx context.Context, seq int64) ([]*apigen.ScheduledInstanceStatus, error) {
	return q.queryScheduledInstanceStatuses(ctx, `SELECT `+scheduledInstanceStatusColumns+` FROM scheduled_instance_status
 WHERE global_seq = ? ORDER BY scheduled_instance_id, updated_at`, seq)
}
