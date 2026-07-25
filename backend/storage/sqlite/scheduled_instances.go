package sqlite

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
	if r.PreparerStatus.Valid {
		st.Preparer = apigen.PreparerStatus{
			DeploymentConfigVersion: int32(r.PreparerConfigVersion.Int64),
			Artifact:                r.PreparerArtifact.String,
			Status:                  apigen.PreparationStatus(r.PreparerStatus.Int64),
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
		p.PreparerStatus = sql.NullInt64{Int64: int64(st.Preparer.Status), Valid: true}
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
func (s *PrimaryStorage) EnsureRunScheduledInstance(deploymentID, deploymentVersion, nodeID, instanceOrdinal int32, initial apigen.ScheduledInstanceTarget) (*apigen.ScheduledInstance, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, state := range s.scheduledCache {
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
func (s *PrimaryStorage) CreateScheduledInstance(deploymentID, deploymentVersion, nodeID, instanceOrdinal int32, initial apigen.ScheduledInstanceTarget) *apigen.ScheduledInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createScheduledInstanceLocked(deploymentID, deploymentVersion, nodeID, instanceOrdinal, initial)
}

func (s *PrimaryStorage) createScheduledInstanceLocked(deploymentID, deploymentVersion, nodeID, instanceOrdinal int32, initial apigen.ScheduledInstanceTarget) *apigen.ScheduledInstance {
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
	s.scheduledCache[inst.ID] = state
	s.notifyInstanceLocked(inst.ID)
	return inst
}

// SetScheduledInstanceState updates target state and publishes. When finalized,
// the instance is removed from the active cache after notify.
func (s *PrimaryStorage) SetScheduledInstanceState(instanceID int32, state apigen.ScheduledInstanceTarget) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	cached := s.scheduledCache[instanceID]
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
		s.scheduledCache[instanceID] = entry
		s.notifyInstanceLocked(instanceID)
		delete(s.scheduledCache, instanceID)
		return
	}
	s.scheduledCache[instanceID] = entry
	s.notifyInstanceLocked(instanceID)
}

// MustWriteReplicatedScheduledInstanceStatus persists a worker status write
// using the worker's UpdatedAt as the authoritative identity.
func (s *PrimaryStorage) MustWriteReplicatedScheduledInstanceStatus(st *apigen.ScheduledInstanceStatus) {
	if st == nil || st.ScheduledInstanceID == 0 || st.UpdatedAt.IsZero() {
		return
	}
	ctx := context.Background()
	ctx = logu.ExtendLogContext(ctx, "scheduled_instance", st.ScheduledInstanceID)

	s.mu.Lock()
	defer s.mu.Unlock()

	entry := s.scheduledCache[st.ScheduledInstanceID]
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
			slog.InfoContext(ctx, "replicated scheduled instance status",
				"updatedAt", st.UpdatedAt,
				"preparerStatus", st.Preparer.Status,
				"runnerStatus", st.Runner.Status,
			)
			return
		}
		entry = &apigen.ScheduledInstanceState{Instance: *inst}
		if cfg := s.configForInstanceLocked(inst); cfg != nil {
			entry.Config = *cfg
		}
		s.scheduledCache[inst.ID] = entry
	}
	st.DeploymentID = entry.Instance.DeploymentID

	params := scheduledInstanceStatusProtoToInsertParams(st)
	if err := s.q.InsertScheduledInstanceStatus(ctx, params); err != nil {
		panic(fmt.Sprintf("InsertScheduledInstanceStatus: %v", err))
	}

	if !st.UpdatedAt.Before(entry.Status.UpdatedAt) {
		entry.Status = *st
		s.notifyInstanceLocked(st.ScheduledInstanceID)
	}
	slog.InfoContext(ctx, "replicated scheduled instance status",
		"updatedAt", st.UpdatedAt,
		"preparerStatus", st.Preparer.Status,
		"runnerStatus", st.Runner.Status,
	)
}

func (s *PrimaryStorage) MustFetchScheduledInstanceStatusHistory(instanceID int32) []*apigen.ScheduledInstanceStatus {
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

func (s *PrimaryStorage) MustFetchDeploymentStatusHistory(deploymentID int32) []*apigen.ScheduledInstanceStatus {
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

func (s *PrimaryStorage) ListNonFinalScheduledInstancesForDeployment(deploymentID int32) []*apigen.ScheduledInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*apigen.ScheduledInstance, 0)
	for _, state := range s.scheduledCache {
		if state.Instance.DeploymentID == deploymentID {
			cp := state.Instance
			out = append(out, &cp)
		}
	}
	return out
}
