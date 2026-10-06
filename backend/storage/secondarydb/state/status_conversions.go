package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/sq"
)

// clockToNanos serializes a status HLC clock to its DB integer form (unix
// nanoseconds). An absent clock maps to 0 — the "no status yet" placeholder.
func clockToNanos(t apigen.Maybe[time.Time]) int64 {
	if !t.Present || t.Value.IsZero() {
		return 0
	}
	return t.Value.UnixNano()
}

// nanosToClock is the inverse of clockToNanos.
func nanosToClock(n int64) apigen.Maybe[time.Time] {
	if n == 0 {
		return apigen.Maybe[time.Time]{}
	}
	return apigen.Some(time.Unix(0, n))
}

func scheduledInstanceStatusRowToProto(r sq.ScheduledInstanceStatus) *apigen.ScheduledInstanceStatus {
	st := &apigen.ScheduledInstanceStatus{
		UpdatedAt:           nanosToClock(r.UpdatedAt),
		ScheduledInstanceID: uint64(r.ScheduledInstanceID),
	}
	// preparer_spec_version is the presence guard: it is written for every
	// recorded preparer status, and unlike the stage columns it is nullable, so
	// it distinguishes "nothing recorded" from a recorded UNKNOWN.
	if r.PreparerSpecVersion.Valid {
		p := apigen.PreparerStatus{
			DeploymentSpecVersion: uint32(r.PreparerSpecVersion.Int64),
			Artifact:              r.PreparerArtifact.String,
			Inputs:                apigen.InputsStatus(r.PreparerInputsStatus),
		}
		if r.PreparerImageStatus != 0 {
			p.Image = apigen.Some(apigen.ImageStatus(r.PreparerImageStatus))
		}
		st.Preparer = apigen.Some(p)
	}
	if r.RunnerStatus.Valid {
		run := apigen.RunnerStatus{
			DeploymentSpecVersion: uint32(r.RunnerSpecVersion.Int64),
			RunningArtifact:       r.RunnerArtifact.String,
			Status:                apigen.RunningStatus(r.RunnerStatus.Int64),
			NumberOfRestarts:      uint32(r.RunnerNumRestarts.Int64),
		}
		if r.RunnerPid.Valid {
			run.RunningPid = apigen.Some(uint32(r.RunnerPid.Int64))
		}
		if r.RunnerLastRestartAt.Valid {
			run.LastRestartAt = apigen.Some(time.UnixMilli(r.RunnerLastRestartAt.Int64))
		}
		if r.RunnerExitCode.Valid {
			run.ExitCode = apigen.Some(int32(r.RunnerExitCode.Int64))
		}
		if len(r.RunnerExtraBlob) > 0 {
			var diagnostics []string
			if err := json.Unmarshal(r.RunnerExtraBlob, &diagnostics); err != nil {
				slog.WarnContext(logu.AddTag(context.Background(), "Store"), "decoding runner status extra blob",
					"scheduled_instance", r.ScheduledInstanceID, "err", err)
			} else {
				run.NetworkDiagnostics = diagnostics
			}
		}
		st.Runner = apigen.Some(run)
	}
	return st
}

func nullInt(v int64, valid bool) sql.NullInt64 {
	return sql.NullInt64{Int64: v, Valid: valid}
}

func scheduledInstanceStatusProtoToInsertParams(st *apigen.ScheduledInstanceStatus) sq.InsertScheduledInstanceStatusParams {
	p := sq.InsertScheduledInstanceStatusParams{
		ScheduledInstanceID: int64(st.ScheduledInstanceID),
		UpdatedAt:           clockToNanos(st.UpdatedAt),
		RunnerExtraBlob:     []byte{},
	}
	if st.Preparer.Present {
		pr := st.Preparer.Value
		p.PreparerSpecVersion = nullInt(int64(pr.DeploymentSpecVersion), true)
		p.PreparerArtifact = sql.NullString{String: pr.Artifact, Valid: true}
		p.PreparerInputsStatus = int64(pr.Inputs)
		p.PreparerImageStatus = int64(pr.Image.Value)
	}
	if st.Runner.Present {
		r := st.Runner.Value
		p.RunnerSpecVersion = nullInt(int64(r.DeploymentSpecVersion), true)
		p.RunnerPid = nullInt(int64(r.RunningPid.Value), r.RunningPid.Present)
		p.RunnerArtifact = sql.NullString{String: r.RunningArtifact, Valid: true}
		p.RunnerStatus = nullInt(int64(r.Status), true)
		p.RunnerNumRestarts = nullInt(int64(r.NumberOfRestarts), true)
		if r.LastRestartAt.Present {
			p.RunnerLastRestartAt = nullInt(r.LastRestartAt.Value.UnixMilli(), true)
		}
		p.RunnerExitCode = nullInt(int64(r.ExitCode.Value), r.ExitCode.Present)
		p.RunnerExtraBlob = runnerStatusExtraBlob(r)
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
	b, _ := json.Marshal(r.NetworkDiagnostics)
	return b
}
