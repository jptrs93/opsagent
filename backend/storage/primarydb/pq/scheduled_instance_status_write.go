package pq

import (
	"context"
	"database/sql"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func runnerStatusExtraBlob(r apigen.RunnerStatus) []byte {
	if len(r.NetworkDiagnostics) == 0 {
		return []byte{}
	}
	return (&apigen.RunnerStatus{NetworkDiagnostics: r.NetworkDiagnostics}).Encode()
}

func (q *Queries) InsertScheduledInstanceStatus(ctx context.Context, seq int64, st *apigen.ScheduledInstanceStatus) error {
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
	_, err := q.db.ExecContext(ctx, `INSERT INTO scheduled_instance_status (
 scheduled_instance_id, updated_at, global_seq, deployment_id,
 preparer_spec_version, preparer_artifact, preparer_inputs_status, preparer_image_status,
 runner_spec_version, runner_pid, runner_artifact, runner_status, runner_num_restarts, runner_last_restart_at, runner_extra_blob, runner_exit_code
 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
 ON CONFLICT(scheduled_instance_id, updated_at) DO UPDATE SET
 global_seq = excluded.global_seq,
 deployment_id = excluded.deployment_id,
 preparer_spec_version = excluded.preparer_spec_version,
 preparer_artifact = excluded.preparer_artifact,
 preparer_inputs_status = excluded.preparer_inputs_status,
 preparer_image_status = excluded.preparer_image_status,
 runner_spec_version = excluded.runner_spec_version,
 runner_pid = excluded.runner_pid,
 runner_artifact = excluded.runner_artifact,
 runner_status = excluded.runner_status,
 runner_num_restarts = excluded.runner_num_restarts,
 runner_last_restart_at = excluded.runner_last_restart_at,
 runner_extra_blob = excluded.runner_extra_blob,
 runner_exit_code = excluded.runner_exit_code`,
		st.ScheduledInstanceID, clockToNanos(st.UpdatedAt), seq, st.DeploymentID,
		preparerSpecVersion, preparerArtifact, preparerInputs, preparerImage,
		runnerSpecVersion, runnerPid, runnerArtifact, runnerStatus, runnerNumRestarts, runnerLastRestartAt, extra, runnerExitCode)
	return err
}
