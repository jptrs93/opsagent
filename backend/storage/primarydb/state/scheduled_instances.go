package state

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// EnsureRunScheduledInstance returns the existing matching runnable assignment
// or creates and publishes one atomically in the given initial state.
//
// The initial state is the caller's to choose and cannot be corrected after the
// fact: creating a replacement as RUN_SERVING would hand it the instance's
// inbound route before its container exists, so a cross-node rollover must
// create its replacement as RUN_STANDBY from the very first write.
func (s *Service) EnsureRunScheduledInstance(deploymentID, deploymentVersion, nodeID, instanceOrdinal int32, initial apigen.ScheduledInstanceTarget) (*apigen.ScheduledInstance, bool) {
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
func (s *Service) CreateScheduledInstance(deploymentID, deploymentVersion, nodeID, instanceOrdinal int32, initial apigen.ScheduledInstanceTarget) *apigen.ScheduledInstance {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.createScheduledInstanceLocked(deploymentID, deploymentVersion, nodeID, instanceOrdinal, initial)
}

func (s *Service) createScheduledInstanceLocked(deploymentID, deploymentVersion, nodeID, instanceOrdinal int32, initial apigen.ScheduledInstanceTarget) *apigen.ScheduledInstance {
	ctx := context.Background()
	if !initial.WantsRunning() {
		panic(fmt.Sprintf("createScheduledInstance: initial state %v is not runnable", initial))
	}
	row, err := s.q.InsertScheduledInstance(ctx, pq.InsertScheduledInstanceParams{
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
func (s *Service) SetScheduledInstanceState(instanceID int32, state apigen.ScheduledInstanceTarget) {
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
	if err := s.q.UpdateScheduledInstanceState(ctx, pq.UpdateScheduledInstanceStateParams{
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
func (s *Service) MustWriteReplicatedScheduledInstanceStatus(st *apigen.ScheduledInstanceStatus) {
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

func (s *Service) MustFetchDeploymentStatusHistory(deploymentID int32) []*apigen.ScheduledInstanceStatus {
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

func (s *Service) ListNonFinalScheduledInstancesForDeployment(deploymentID int32) []*apigen.ScheduledInstance {
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
