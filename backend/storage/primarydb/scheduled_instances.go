package primarydb

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
)

func scheduledInstanceRowToProto(r ScheduledInstance) *apigen.ScheduledInstance {
	return &apigen.ScheduledInstance{
		ID:                int32(r.ID),
		CreatedAt:         millisToTime(r.CreatedAt),
		DeploymentID:      int32(r.DeploymentID),
		DeploymentVersion: int32(r.DeploymentVersion),
		NodeID:            int32(r.NodeID),
		InstanceOrdinal:   int32(r.InstanceOrdinal),
		State:             apigen.ScheduledInstanceTarget(r.State),
	}
}

func scheduledInstanceStatusRowToProto(r ScheduledInstanceStatus) *apigen.ScheduledInstanceStatus {
	st := &apigen.ScheduledInstanceStatus{
		UpdatedAt:           nanosToClock(r.UpdatedAt),
		ScheduledInstanceID: int32(r.ScheduledInstanceID),
		DeploymentID:        int32(r.DeploymentID),
	}
	// preparer_config_version is the presence guard: it is written for every
	// non-zero preparer status, and unlike the stage columns it is nullable, so
	// it distinguishes "nothing recorded" from a recorded UNKNOWN.
	if r.PreparerConfigVersion.Valid {
		st.Preparer = apigen.PreparerStatus{
			DeploymentConfigVersion: int32(r.PreparerConfigVersion.Int64),
			Artifact:                r.PreparerArtifact.String,
			Inputs:                  apigen.InputsStatus(r.PreparerInputsStatus),
			Image:                   apigen.ImageStatus(r.PreparerImageStatus),
		}
	}
	if r.RunnerStatus.Valid {
		st.Runner = apigen.RunnerStatus{
			DeploymentConfigVersion: int32(r.RunnerConfigVersion.Int64),
			RunningPid:              int32(r.RunnerPid.Int64),
			RunningArtifact:         r.RunnerArtifact.String,
			Status:                  apigen.RunningStatus(r.RunnerStatus.Int64),
			NumberOfRestarts:        int32(r.RunnerNumRestarts.Int64),
		}
		if r.RunnerLastRestartAt.Valid {
			st.Runner.LastRestartAt = time.UnixMilli(r.RunnerLastRestartAt.Int64)
		}
		if len(r.RunnerExtraBlob) > 0 {
			extra, err := apigen.DecodeRunnerStatus(r.RunnerExtraBlob)
			if err != nil {
				slog.Warn("decoding runner status extra blob", "scheduled_instance_id", r.ScheduledInstanceID, "err", err)
			} else {
				st.Runner.NetworkDiagnostics = extra.NetworkDiagnostics
			}
		}
	}
	return st
}

func scheduledInstanceStatusProtoToInsertParams(st *apigen.ScheduledInstanceStatus) InsertScheduledInstanceStatusParams {
	p := InsertScheduledInstanceStatusParams{
		ScheduledInstanceID: int64(st.ScheduledInstanceID),
		UpdatedAt:           clockToNanos(st.UpdatedAt),
		DeploymentID:        int64(st.DeploymentID),
		RunnerExtraBlob:     []byte{},
	}
	if !st.Preparer.IsZero() {
		p.PreparerConfigVersion = sql.NullInt64{Int64: int64(st.Preparer.DeploymentConfigVersion), Valid: true}
		p.PreparerArtifact = sql.NullString{String: st.Preparer.Artifact, Valid: true}
		p.PreparerInputsStatus = int64(st.Preparer.Inputs)
		p.PreparerImageStatus = int64(st.Preparer.Image)
	}
	if !st.Runner.IsZero() {
		p.RunnerConfigVersion = sql.NullInt64{Int64: int64(st.Runner.DeploymentConfigVersion), Valid: true}
		p.RunnerPid = sql.NullInt64{Int64: int64(st.Runner.RunningPid), Valid: true}
		p.RunnerArtifact = sql.NullString{String: st.Runner.RunningArtifact, Valid: true}
		p.RunnerStatus = sql.NullInt64{Int64: int64(st.Runner.Status), Valid: true}
		p.RunnerNumRestarts = sql.NullInt64{Int64: int64(st.Runner.NumberOfRestarts), Valid: true}
		if !st.Runner.LastRestartAt.IsZero() {
			p.RunnerLastRestartAt = sql.NullInt64{Int64: st.Runner.LastRestartAt.UnixMilli(), Valid: true}
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

func configHistoryRowToFullProto(r DeploymentConfigHistory, identity apigen.DeploymentIdentity, createdAt time.Time) *apigen.DeploymentConfig {
	spec := mustDecodeDeploymentSpec(r.SpecBlob, r.DeploymentID, r.Version)
	identity.SpaceID = int32(r.SpaceID)
	return &apigen.DeploymentConfig{
		ID:        int32(r.DeploymentID),
		NodeID:    int32(r.NodeID),
		Identity:  identity,
		CreatedAt: createdAt,
		Version:   int32(r.Version),
		UpdatedAt: time.UnixMilli(r.UpdatedAt),
		UpdatedBy: int32(r.UpdatedBy),
		Spec:      deploymentSpecValue(spec),
		Deleted:   r.Deleted != 0,
	}
}

// EnsureRunScheduledInstance returns the existing matching runnable assignment
// or creates and publishes one atomically in the given initial state.
//
// The initial state is the caller's to choose and cannot be corrected after the
// fact: creating a replacement as RUN_SERVING would hand it the instance's
// inbound route before its container exists, so a cross-node rollover must
// create its replacement as RUN_STANDBY from the very first write.
func (s *Storage) EnsureRunScheduledInstance(deploymentID, deploymentVersion, nodeID, instanceOrdinal int32, initial apigen.ScheduledInstanceTarget) (*apigen.ScheduledInstance, bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	for _, state := range s.Scheduled {
		inst := state.Instance
		if inst.DeploymentID == deploymentID &&
			inst.DeploymentVersion == deploymentVersion &&
			inst.NodeID == nodeID &&
			inst.InstanceOrdinal == instanceOrdinal &&
			inst.State.WantsRunning() {
			cp := inst
			return &cp, false
		}
	}
	return s.createScheduledInstanceLocked(deploymentID, deploymentVersion, nodeID, instanceOrdinal, initial), true
}

// CreateScheduledInstance is retained for callers that explicitly need a new
// incarnation. Scheduler and enrollment paths should use EnsureRunScheduledInstance.
func (s *Storage) CreateScheduledInstance(deploymentID, deploymentVersion, nodeID, instanceOrdinal int32, initial apigen.ScheduledInstanceTarget) *apigen.ScheduledInstance {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.createScheduledInstanceLocked(deploymentID, deploymentVersion, nodeID, instanceOrdinal, initial)
}

func (s *Storage) createScheduledInstanceLocked(deploymentID, deploymentVersion, nodeID, instanceOrdinal int32, initial apigen.ScheduledInstanceTarget) *apigen.ScheduledInstance {
	ctx := context.Background()
	if !initial.WantsRunning() {
		panic(fmt.Sprintf("createScheduledInstance: initial state %v is not runnable", initial))
	}
	row, err := s.q.InsertScheduledInstance(ctx, InsertScheduledInstanceParams{
		CreatedAt:         time.Now().UnixMilli(),
		DeploymentID:      int64(deploymentID),
		DeploymentVersion: int64(deploymentVersion),
		NodeID:            int64(nodeID),
		InstanceOrdinal:   int64(instanceOrdinal),
		State:             int64(initial),
	})
	if err != nil {
		panic(fmt.Sprintf("InsertScheduledInstance: %v", err))
	}
	inst := scheduledInstanceRowToProto(row)
	state := &apigen.ScheduledInstanceState{Instance: *inst}
	if cfg := s.configForInstanceLocked(inst); cfg != nil {
		state.Config = *cfg
	}
	s.Scheduled[inst.ID] = state
	// This incarnation now speaks for the ordinal, so whatever ran last is no
	// longer worth retaining.
	delete(s.latestFinalCache, ordinalKeyOf(inst))
	s.NotifyInstanceLocked(inst.ID)
	return inst
}

// SetScheduledInstanceState updates target state and publishes. When finalized,
// the instance is removed from the active cache after notify.
func (s *Storage) SetScheduledInstanceState(instanceID int32, state apigen.ScheduledInstanceTarget) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := context.Background()
	cached := s.Scheduled[instanceID]
	var inst *apigen.ScheduledInstance
	if cached != nil {
		inst = &cached.Instance
	} else {
		row, err := s.q.GetScheduledInstance(ctx, int64(instanceID))
		if err != nil {
			slog.Warn("SetScheduledInstanceState: unknown instance", "id", instanceID, "err", err)
			return
		}
		inst = scheduledInstanceRowToProto(row)
	}
	if inst.State == state {
		return
	}
	if err := s.q.UpdateScheduledInstanceState(ctx, UpdateScheduledInstanceStateParams{
		State: int64(state),
		ID:    int64(instanceID),
	}); err != nil {
		panic(fmt.Sprintf("UpdateScheduledInstanceState: %v", err))
	}
	updated := *inst
	updated.State = state
	entry := cached
	if entry == nil {
		entry = &apigen.ScheduledInstanceState{Instance: updated}
		if cfg := s.configForInstanceLocked(&updated); cfg != nil {
			entry.Config = *cfg
		}
	} else {
		entry.Instance = updated
	}
	if state == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
		s.Scheduled[instanceID] = entry
		s.NotifyInstanceLocked(instanceID)
		delete(s.Scheduled, instanceID)
		s.retainFinalizedLocked(entry)
		return
	}
	s.Scheduled[instanceID] = entry
	s.NotifyInstanceLocked(instanceID)
}

// MustWriteReplicatedScheduledInstanceStatus persists a worker status write
// using the worker's UpdatedAt as the authoritative identity.
func (s *Storage) MustWriteReplicatedScheduledInstanceStatus(st *apigen.ScheduledInstanceStatus) {
	if st == nil || st.ScheduledInstanceID == 0 || st.UpdatedAt.IsZero() {
		return
	}
	ctx := context.Background()
	ctx = logu.ExtendLogContext(ctx, "scheduled_instance", st.ScheduledInstanceID)

	s.Mu.Lock()
	defer s.Mu.Unlock()

	entry := s.Scheduled[st.ScheduledInstanceID]
	if entry == nil {
		row, err := s.q.GetScheduledInstance(ctx, int64(st.ScheduledInstanceID))
		if err != nil {
			slog.WarnContext(ctx, "rejecting status for unknown scheduled instance")
			return
		}
		inst := scheduledInstanceRowToProto(row)
		if inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
			st.DeploymentID = inst.DeploymentID
			params := scheduledInstanceStatusProtoToInsertParams(st)
			if err := s.q.InsertScheduledInstanceStatus(ctx, params); err != nil {
				panic(fmt.Sprintf("InsertScheduledInstanceStatus: %v", err))
			}
			// A worker's last write can land after the primary has finalized the
			// instance. It is history, not schedule, but if this is still the
			// ordinal's last known runtime it is also what the UI shows.
			if retained := s.latestFinalCache[ordinalKeyOf(inst)]; retained != nil &&
				retained.Instance.ID == inst.ID && !st.UpdatedAt.Before(retained.Status.UpdatedAt) {
				retained.Status = *st
				s.Subs.Notify(*retained)
			}
			slog.InfoContext(ctx, "replicated scheduled instance status",
				"updatedAt", st.UpdatedAt,
				"preparerStatus", st.Preparer.Rollup(),
				"runnerStatus", st.Runner.Status,
			)
			return
		}
		entry = &apigen.ScheduledInstanceState{Instance: *inst}
		if cfg := s.configForInstanceLocked(inst); cfg != nil {
			entry.Config = *cfg
		}
		s.Scheduled[inst.ID] = entry
	}
	st.DeploymentID = entry.Instance.DeploymentID

	params := scheduledInstanceStatusProtoToInsertParams(st)
	if err := s.q.InsertScheduledInstanceStatus(ctx, params); err != nil {
		panic(fmt.Sprintf("InsertScheduledInstanceStatus: %v", err))
	}

	if !st.UpdatedAt.Before(entry.Status.UpdatedAt) {
		entry.Status = *st
		s.NotifyInstanceLocked(st.ScheduledInstanceID)
	}
	slog.InfoContext(ctx, "replicated scheduled instance status",
		"updatedAt", st.UpdatedAt,
		"preparerStatus", st.Preparer.Rollup(),
		"runnerStatus", st.Runner.Status,
	)
}

func (s *Storage) MustFetchScheduledInstanceStatusHistory(instanceID int32) []*apigen.ScheduledInstanceStatus {
	ctx := context.Background()
	rows, err := s.q.ListScheduledInstanceStatusHistory(ctx, int64(instanceID))
	if err != nil {
		panic(fmt.Sprintf("ListScheduledInstanceStatusHistory: %v", err))
	}
	out := make([]*apigen.ScheduledInstanceStatus, 0, len(rows))
	for _, r := range rows {
		out = append(out, scheduledInstanceStatusRowToProto(r))
	}
	return out
}

func (s *Storage) MustFetchDeploymentStatusHistory(deploymentID int32) []*apigen.ScheduledInstanceStatus {
	ctx := context.Background()
	rows, err := s.q.ListScheduledInstanceStatusHistoryForDeployment(ctx, int64(deploymentID))
	if err != nil {
		panic(fmt.Sprintf("ListScheduledInstanceStatusHistoryForDeployment: %v", err))
	}
	out := make([]*apigen.ScheduledInstanceStatus, 0, len(rows))
	for _, r := range rows {
		out = append(out, scheduledInstanceStatusRowToProto(r))
	}
	return out
}

func (s *Storage) ListNonFinalScheduledInstancesForDeployment(deploymentID int32) []*apigen.ScheduledInstance {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	out := make([]*apigen.ScheduledInstance, 0)
	for _, state := range s.Scheduled {
		if state.Instance.DeploymentID == deploymentID {
			cp := state.Instance
			out = append(out, &cp)
		}
	}
	return out
}
