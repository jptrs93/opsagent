package pq

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
)

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

type scheduledInstanceStatusEnvelope struct {
	rowEnvelope
	Status *apigen.ScheduledInstanceStatus
}

func (q *Queries) listScheduledInstanceStatusRows(ctx context.Context) ([]scheduledInstanceStatusEnvelope, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+scheduledInstanceStatusColumnsS+`, s.seq, s.event_time, s.author, s.created_time FROM scheduled_instance_status s ORDER BY s.scheduled_instance_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []scheduledInstanceStatusEnvelope
	for rows.Next() {
		var r scheduledInstanceStatusRow
		var env rowEnvelope
		if err := rows.Scan(append(r.fields(), &env.Seq, &env.EventTime, &env.Author, &env.CreatedTime)...); err != nil {
			return nil, err
		}
		out = append(out, scheduledInstanceStatusEnvelope{rowEnvelope: env, Status: r.toStatus()})
	}
	return out, rows.Err()
}

func (q *Queries) GetLatestScheduledInstanceStatus(ctx context.Context, id int32) (*apigen.ScheduledInstanceStatus, error) {
	var r scheduledInstanceStatusRow
	if err := q.db.QueryRowContext(ctx, `SELECT `+scheduledInstanceStatusColumnsS+` FROM scheduled_instance_status s WHERE s.scheduled_instance_id = ?`, id).Scan(r.fields()...); err != nil {
		return nil, err
	}
	return r.toStatus(), nil
}

func (q *Queries) ListLatestScheduledInstanceStatuses(ctx context.Context) ([]*apigen.ScheduledInstanceStatus, error) {
	rows, err := q.listScheduledInstanceStatusRows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*apigen.ScheduledInstanceStatus, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Status)
	}
	return out, nil
}

// ListScheduledInstanceStatusHistorySince is an instance's observed history
// after since, oldest first, read from the write log.
func (q *Queries) ListScheduledInstanceStatusHistorySince(ctx context.Context, id int32, since time.Time) ([]*apigen.ScheduledInstanceStatus, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT payload FROM write_event_mutations WHERE entity_type = ? AND entity_id = ? AND op != ? ORDER BY seq, idx`,
		int64(apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS), int64(id), int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.ScheduledInstanceStatus
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		entity, err := apigen.DecodeCoreEntity(payload)
		if err != nil || entity.ScheduledInstanceStatus == nil {
			return nil, fmt.Errorf("scheduled instance %d status: %v", id, err)
		}
		if st := entity.ScheduledInstanceStatus; st.UpdatedAt.After(since) {
			out = append(out, st)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	return out, nil
}

// ListScheduledInstanceStatusHistoryForDeployment is the observed history of
// every instance a deployment ever had, oldest first, read from the write
// log: the instance ids come from the instance creates, the statuses from
// each instance's own log entries.
func (q *Queries) ListScheduledInstanceStatusHistoryForDeployment(ctx context.Context, deploymentID int32) ([]*apigen.ScheduledInstanceStatus, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT entity_id, payload FROM write_event_mutations WHERE entity_type = ? AND op = ? ORDER BY entity_id`,
		int64(apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE), int64(apigen.AuthzVerb_AUTHZ_VERB_CREATE))
	if err != nil {
		return nil, err
	}
	var instances []int32
	for rows.Next() {
		var id int64
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			rows.Close()
			return nil, err
		}
		entity, err := apigen.DecodeCoreEntity(payload)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if entity.ScheduledInstance != nil && entity.ScheduledInstance.DeploymentID == deploymentID {
			instances = append(instances, int32(id))
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []*apigen.ScheduledInstanceStatus
	for _, id := range instances {
		history, err := q.ListScheduledInstanceStatusHistorySince(ctx, id, time.Time{})
		if err != nil {
			return nil, err
		}
		out = append(out, history...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	return out, nil
}

func runnerStatusExtraBlob(r apigen.RunnerStatus) []byte {
	if len(r.NetworkDiagnostics) == 0 {
		return []byte{}
	}
	return (&apigen.RunnerStatus{NetworkDiagnostics: r.NetworkDiagnostics}).Encode()
}

// reduceScheduledInstanceStatus keeps the report with the greater clock: a
// report older than the row's is the stale half of a merge and is dropped,
// and its meta is the row's.
func (q *Queries) reduceScheduledInstanceStatus(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id int64, st *apigen.ScheduledInstanceStatus) error {
	if st == nil {
		return fmt.Errorf("payload has no status")
	}
	var preparerSpecVersion, runnerSpecVersion, runnerPid, runnerStatus, runnerNumRestarts, runnerLastRestartAt, runnerExitCode sql.NullInt64
	var preparerArtifact, runnerArtifact sql.NullString
	var preparerInputs, preparerImage int64
	extra := []byte{}
	if !st.Preparer.IsZero() {
		preparerSpecVersion = sql.NullInt64{Int64: int64(st.Preparer.DeploymentSpecVersion), Valid: true}
		preparerArtifact = sql.NullString{String: st.Preparer.Artifact, Valid: true}
		preparerInputs = int64(st.Preparer.Inputs)
		preparerImage = int64(st.Preparer.Image)
	}
	if !st.Runner.IsZero() {
		runnerSpecVersion = sql.NullInt64{Int64: int64(st.Runner.DeploymentSpecVersion), Valid: true}
		runnerPid = sql.NullInt64{Int64: int64(st.Runner.RunningPid), Valid: true}
		runnerArtifact = sql.NullString{String: st.Runner.RunningArtifact, Valid: true}
		runnerStatus = sql.NullInt64{Int64: int64(st.Runner.Status), Valid: true}
		runnerNumRestarts = sql.NullInt64{Int64: int64(st.Runner.NumberOfRestarts), Valid: true}
		if !st.Runner.LastRestartAt.IsZero() {
			runnerLastRestartAt = sql.NullInt64{Int64: st.Runner.LastRestartAt.UnixMilli(), Valid: true}
		}
		if st.Runner.ExitCode != nil {
			runnerExitCode = sql.NullInt64{Int64: int64(*st.Runner.ExitCode), Valid: true}
		}
		extra = runnerStatusExtraBlob(st.Runner)
	}
	if err := q.upsert(ctx, meta, `INSERT INTO scheduled_instance_status (
 scheduled_instance_id, updated_at, deployment_id,
 preparer_spec_version, preparer_artifact, preparer_inputs_status, preparer_image_status,
 runner_spec_version, runner_pid, runner_artifact, runner_status, runner_num_restarts, runner_last_restart_at, runner_extra_blob, runner_exit_code,
 seq, event_time, author, created_time
 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
 ON CONFLICT (scheduled_instance_id) DO UPDATE SET
 updated_at = excluded.updated_at, deployment_id = excluded.deployment_id,
 preparer_spec_version = excluded.preparer_spec_version, preparer_artifact = excluded.preparer_artifact,
 preparer_inputs_status = excluded.preparer_inputs_status, preparer_image_status = excluded.preparer_image_status,
 runner_spec_version = excluded.runner_spec_version, runner_pid = excluded.runner_pid, runner_artifact = excluded.runner_artifact,
 runner_status = excluded.runner_status, runner_num_restarts = excluded.runner_num_restarts, runner_last_restart_at = excluded.runner_last_restart_at,
 runner_extra_blob = excluded.runner_extra_blob, runner_exit_code = excluded.runner_exit_code,
 seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
 WHERE excluded.updated_at >= scheduled_instance_status.updated_at
 RETURNING created_time`,
		id, clockToNanos(st.UpdatedAt), int64(st.DeploymentID),
		preparerSpecVersion, preparerArtifact, preparerInputs, preparerImage,
		runnerSpecVersion, runnerPid, runnerArtifact, runnerStatus, runnerNumRestarts, runnerLastRestartAt, extra, runnerExitCode,
		env.Seq, env.EventTime, env.Author, env.EventTime); err != nil {
		return err
	}
	return q.rowMetaIfStale(ctx, meta, apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS, id)
}

func (q *Queries) deleteScheduledInstanceStatusRow(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM scheduled_instance_status WHERE scheduled_instance_id = ?`, id)
	return err
}

// deleteOrphanScheduledInstanceStatus drops a status whose instance is not
// retained: a report for a pruned instance has nothing to attach to.
func (q *Queries) deleteOrphanScheduledInstanceStatus(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM scheduled_instance_status WHERE scheduled_instance_id = ? AND NOT EXISTS (SELECT 1 FROM scheduled_instances WHERE id = ?)`, id, id)
	return err
}

// CanonicalScheduledInstanceStatus is the status as its row reads back:
// restart times at millisecond precision and the runner's extra fields as
// the blob carries them. A published report equals its materialised row.
func CanonicalScheduledInstanceStatus(st *apigen.ScheduledInstanceStatus) *apigen.ScheduledInstanceStatus {
	out := *st
	if !out.Runner.LastRestartAt.IsZero() {
		out.Runner.LastRestartAt = time.UnixMilli(out.Runner.LastRestartAt.UnixMilli())
	}
	if out.Runner.IsZero() {
		out.Runner = apigen.RunnerStatus{}
	} else if len(out.Runner.NetworkDiagnostics) == 0 {
		out.Runner.NetworkDiagnostics = nil
	}
	if out.Preparer.IsZero() {
		out.Preparer = apigen.PreparerStatus{}
	}
	return &out
}
