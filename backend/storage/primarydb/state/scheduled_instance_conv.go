package state

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func scheduledInstanceRowToProto(r pq.ScheduledInstanceRow) *apigen.ScheduledInstance {
	return &apigen.ScheduledInstance{
		ID:                    int32(r.ID),
		CreatedAt:             millisToTime(r.CreatedAt),
		DeploymentID:          int32(r.DeploymentID),
		DeploymentSpecVersion: int32(r.DeploymentSpecVersion),
		NodeID:                int32(r.NodeID),
		InstanceOrdinal:       int32(r.InstanceOrdinal),
		SpaceID:               int32(r.SpaceID),
		State:                 apigen.ScheduledInstanceTarget(r.State),
	}
}

func scheduledInstanceStatusRowToProto(r pq.ScheduledInstanceStatus) *apigen.ScheduledInstanceStatus {
	st := &apigen.ScheduledInstanceStatus{
		UpdatedAt:           nanosToClock(r.UpdatedAt),
		ScheduledInstanceID: int32(r.ScheduledInstanceID),
		DeploymentID:        int32(r.DeploymentID),
	}
	// preparer_config_version is the presence guard: it is written for every
	// non-zero preparer status, and unlike the stage columns it is nullable, so
	// it distinguishes "nothing recorded" from a recorded UNKNOWN.
	if r.PreparerSpecVersion.Valid {
		st.Preparer = apigen.PreparerStatus{
			DeploymentSpecVersion: int32(r.PreparerSpecVersion.Int64),
			Artifact:              r.PreparerArtifact.String,
			Inputs:                apigen.InputsStatus(r.PreparerInputsStatus),
			Image:                 apigen.ImageStatus(r.PreparerImageStatus),
		}
	}
	if r.RunnerStatus.Valid {
		st.Runner = apigen.RunnerStatus{
			DeploymentSpecVersion: int32(r.RunnerSpecVersion.Int64),
			RunningPid:            int32(r.RunnerPid.Int64),
			RunningArtifact:       r.RunnerArtifact.String,
			Status:                apigen.RunningStatus(r.RunnerStatus.Int64),
			NumberOfRestarts:      int32(r.RunnerNumRestarts.Int64),
		}
		if r.RunnerLastRestartAt.Valid {
			st.Runner.LastRestartAt = time.UnixMilli(r.RunnerLastRestartAt.Int64)
		}
		if r.RunnerExitCode.Valid {
			code := int32(r.RunnerExitCode.Int64)
			st.Runner.ExitCode = &code
		}
		if len(r.RunnerExtraBlob) > 0 {
			extra, err := apigen.DecodeRunnerStatus(r.RunnerExtraBlob)
			if err != nil {
				slog.WarnContext(logu.AddTag(context.Background(), "Store"), "decoding runner status extra blob",
					"scheduled_instance", r.ScheduledInstanceID, "err", err)
			} else {
				st.Runner.NetworkDiagnostics = extra.NetworkDiagnostics
			}
		}
	}
	return st
}

func scheduledInstanceStatusProtoToInsertParams(st *apigen.ScheduledInstanceStatus) pq.InsertScheduledInstanceStatusParams {
	p := pq.InsertScheduledInstanceStatusParams{
		ScheduledInstanceID: int64(st.ScheduledInstanceID),
		UpdatedAt:           clockToNanos(st.UpdatedAt),
		DeploymentID:        int64(st.DeploymentID),
		RunnerExtraBlob:     []byte{},
	}
	if !st.Preparer.IsZero() {
		p.PreparerSpecVersion = sql.NullInt64{Int64: int64(st.Preparer.DeploymentSpecVersion), Valid: true}
		p.PreparerArtifact = sql.NullString{String: st.Preparer.Artifact, Valid: true}
		p.PreparerInputsStatus = int64(st.Preparer.Inputs)
		p.PreparerImageStatus = int64(st.Preparer.Image)
	}
	if !st.Runner.IsZero() {
		p.RunnerSpecVersion = sql.NullInt64{Int64: int64(st.Runner.DeploymentSpecVersion), Valid: true}
		p.RunnerPid = sql.NullInt64{Int64: int64(st.Runner.RunningPid), Valid: true}
		p.RunnerArtifact = sql.NullString{String: st.Runner.RunningArtifact, Valid: true}
		p.RunnerStatus = sql.NullInt64{Int64: int64(st.Runner.Status), Valid: true}
		p.RunnerNumRestarts = sql.NullInt64{Int64: int64(st.Runner.NumberOfRestarts), Valid: true}
		if !st.Runner.LastRestartAt.IsZero() {
			p.RunnerLastRestartAt = sql.NullInt64{Int64: st.Runner.LastRestartAt.UnixMilli(), Valid: true}
		}
		if st.Runner.ExitCode != nil {
			p.RunnerExitCode = sql.NullInt64{Int64: int64(*st.Runner.ExitCode), Valid: true}
		}
		p.RunnerExtraBlob = runnerStatusExtraBlob(st.Runner)
	}
	return p
}

// runnerStatusExtraBlob carries the runner status fields that have no dedicated
// column. Endpoints used to live here; they are now derived from the placement
// rather than reported, so only diagnostics remain.
func runnerStatusExtraBlob(r apigen.RunnerStatus) []byte {
	if len(r.NetworkDiagnostics) == 0 {
		return []byte{}
	}
	return (&apigen.RunnerStatus{NetworkDiagnostics: r.NetworkDiagnostics}).Encode()
}
