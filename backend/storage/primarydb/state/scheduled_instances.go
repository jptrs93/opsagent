package state

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// EnsureRunScheduledInstance returns the existing matching runnable assignment
// or creates and publishes one atomically in the given initial state.
//
// The scan below is the only enforcement of at-most-one-runnable-incarnation
// per assignment tuple: the database no longer carries a state column, so the
// former partial unique index backing it is gone. Creation must stay behind
// s.Mu.
//
// The initial state is the caller's to choose and cannot be corrected after the
// fact: creating a replacement as RUN_SERVING would hand it the instance's
// inbound route before its container exists, so a cross-node rollover must
// create its replacement as RUN_STANDBY from the very first write.
func (s *Service) EnsureRunScheduledInstance(deploymentID, deploymentSpecVersion, nodeID, instanceOrdinal int32, initial apigen.ScheduledInstanceTarget) (*apigen.ScheduledInstance, bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	spaceID := s.currentSpaceLocked(deploymentID)
	for _, state := range s.Scheduled {
		inst := state.Instance
		if inst.DeploymentID == deploymentID &&
			inst.DeploymentSpecVersion == deploymentSpecVersion &&
			inst.NodeID == nodeID &&
			inst.InstanceOrdinal == instanceOrdinal &&
			inst.SpaceID == spaceID &&
			inst.State.WantsRunning() {
			cp := inst
			return &cp, false
		}
	}
	return s.createScheduledInstanceLocked(deploymentID, deploymentSpecVersion, nodeID, instanceOrdinal, initial), true
}

// CreateScheduledInstanceForTest unconditionally creates a new incarnation,
// bypassing the at-most-one-runnable-per-tuple scan in
// EnsureRunScheduledInstance. It exists only as a fixture for tests in other
// packages; production code must use EnsureRunScheduledInstance.
func (s *Service) CreateScheduledInstanceForTest(deploymentID, deploymentSpecVersion, nodeID, instanceOrdinal int32, initial apigen.ScheduledInstanceTarget) *apigen.ScheduledInstance {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.createScheduledInstanceLocked(deploymentID, deploymentSpecVersion, nodeID, instanceOrdinal, initial)
}

func (s *Service) currentSpaceLocked(deploymentID int32) int32 {
	if cfg := s.deploymentCache[deploymentID]; cfg != nil {
		return cfg.Def.SpaceID
	}
	return 0
}

func (s *Service) createScheduledInstanceLocked(deploymentID, deploymentSpecVersion, nodeID, instanceOrdinal int32, initial apigen.ScheduledInstanceTarget) *apigen.ScheduledInstance {
	ctx := context.Background()
	if !initial.WantsRunning() {
		panic(fmt.Sprintf("createScheduledInstance: initial state %v is not runnable", initial))
	}
	spaceID := s.currentSpaceLocked(deploymentID)
	now := time.Now().UnixMilli()
	var id int64
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		id = erru.Must(q.InsertScheduledInstance(ctx, pq.InsertScheduledInstanceParams{
			DeploymentID:          int64(deploymentID),
			DeploymentSpecVersion: int64(deploymentSpecVersion),
			NodeID:                int64(nodeID),
			InstanceOrdinal:       int64(instanceOrdinal),
			SpaceID:               int64(spaceID),
		}))
		seq := erru.Must(q.NextGlobalSeq(ctx))
		return q.AppendScheduledInstanceVersion(ctx, pq.AppendScheduledInstanceVersionParams{
			ScheduledInstanceID: id,
			CreatedAt:           now,
			State:               int64(initial),
			GlobalSeq:           seq,
		})
	}); err != nil {
		panic(fmt.Sprintf("InsertScheduledInstance: %v", err))
	}
	inst := &apigen.ScheduledInstance{
		ID:                    int32(id),
		CreatedAt:             millisToTime(now),
		DeploymentID:          deploymentID,
		DeploymentSpecVersion: deploymentSpecVersion,
		NodeID:                nodeID,
		InstanceOrdinal:       instanceOrdinal,
		SpaceID:               spaceID,
		State:                 initial,
	}
	state := s.instanceStateLocked(inst)
	s.Scheduled[inst.ID] = state
	// This incarnation now speaks for the ordinal, so whatever ran last is no
	// longer worth retaining.
	delete(s.latestFinalCache, ordinalKeyOf(inst))
	s.NotifyInstanceLocked(inst.ID)
	return inst
}

// SetScheduledInstanceState appends the transition to
// scheduled_instance_versions — the sole store of instance state — and
// publishes. When finalized, the instance is removed from the active cache
// after notify. It returns the global write sequence allocated for the
// transition, or 0 when nothing was written, so callers can wait for the
// network map derived from their own decision.
func (s *Service) SetScheduledInstanceState(instanceID int32, state apigen.ScheduledInstanceTarget) int64 {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := logu.AddTag(context.Background(), "Store")
	cached := s.Scheduled[instanceID]
	var inst *apigen.ScheduledInstance
	if cached != nil {
		inst = &cached.Instance
	} else {
		row, err := s.q.GetScheduledInstance(ctx, int64(instanceID))
		if err != nil {
			slog.WarnContext(ctx, "SetScheduledInstanceState: unknown instance", "scheduled_instance", instanceID, "err", err)
			return 0
		}
		inst = scheduledInstanceRowToProto(row)
	}
	if inst.State == state {
		return 0
	}
	var allocated int64
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		seq := erru.Must(q.NextGlobalSeq(ctx))
		allocated = seq
		return q.AppendScheduledInstanceVersion(ctx, pq.AppendScheduledInstanceVersionParams{
			ScheduledInstanceID: int64(instanceID),
			CreatedAt:           time.Now().UnixMilli(),
			State:               int64(state),
			GlobalSeq:           seq,
		})
	}); err != nil {
		panic(fmt.Sprintf("AppendScheduledInstanceVersion: %v", err))
	}
	updated := *inst
	updated.State = state
	entry := cached
	if entry == nil {
		entry = s.instanceStateLocked(&updated)
	} else {
		entry.Instance = updated
	}
	if state == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
		s.Scheduled[instanceID] = entry
		s.NotifyInstanceLocked(instanceID)
		delete(s.Scheduled, instanceID)
		s.retainFinalizedLocked(entry)
		return allocated
	}
	s.Scheduled[instanceID] = entry
	s.NotifyInstanceLocked(instanceID)
	return allocated
}

func (s *Service) FlipScheduledInstanceServing(drainIDs []int32, serveID int32) int64 {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := logu.AddTag(context.Background(), "Store")
	type transition struct {
		id    int32
		state apigen.ScheduledInstanceTarget
		inst  *apigen.ScheduledInstance
	}
	writes := make([]transition, 0, len(drainIDs)+1)
	resolve := func(id int32, state apigen.ScheduledInstanceTarget) {
		cached := s.Scheduled[id]
		var inst *apigen.ScheduledInstance
		if cached != nil {
			inst = &cached.Instance
		} else {
			row, err := s.q.GetScheduledInstance(ctx, int64(id))
			if err != nil {
				slog.WarnContext(ctx, "FlipScheduledInstanceServing: unknown instance", "scheduled_instance", id, "err", err)
				return
			}
			inst = scheduledInstanceRowToProto(row)
		}
		if inst.State == state {
			return
		}
		writes = append(writes, transition{id: id, state: state, inst: inst})
	}
	for _, id := range drainIDs {
		resolve(id, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING)
	}
	resolve(serveID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	if len(writes) == 0 {
		return 0
	}
	var allocated int64
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		for _, w := range writes {
			seq := erru.Must(q.NextGlobalSeq(ctx))
			allocated = seq
			if err := q.AppendScheduledInstanceVersion(ctx, pq.AppendScheduledInstanceVersionParams{
				ScheduledInstanceID: int64(w.id),
				CreatedAt:           time.Now().UnixMilli(),
				State:               int64(w.state),
				GlobalSeq:           seq,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("AppendScheduledInstanceVersion: %v", err))
	}
	for _, w := range writes {
		updated := *w.inst
		updated.State = w.state
		entry := s.Scheduled[w.id]
		if entry == nil {
			entry = s.instanceStateLocked(&updated)
		} else {
			entry.Instance = updated
		}
		s.Scheduled[w.id] = entry
	}
	for _, w := range writes {
		s.NotifyInstanceLocked(w.id)
	}
	return allocated
}

// MustWriteReplicatedScheduledInstanceStatus persists a secondary status write
// using the secondary's UpdatedAt as the authoritative identity.
func (s *Service) MustWriteReplicatedScheduledInstanceStatus(st *apigen.ScheduledInstanceStatus) {
	if st == nil || st.ScheduledInstanceID == 0 || st.UpdatedAt.IsZero() {
		return
	}
	ctx := logu.AddTag(context.Background(), "Store")
	ctx = logu.AddKV(ctx, "scheduled_instance", st.ScheduledInstanceID)

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
			// A secondary's last write can land after the primary has finalized the
			// instance. It is history, not schedule, but if this is still the
			// ordinal's last known runtime it is also what the UI shows.
			if retained := s.latestFinalCache[ordinalKeyOf(inst)]; retained != nil &&
				retained.Instance.ID == inst.ID && !st.UpdatedAt.Before(retained.Status.UpdatedAt) {
				retained.Status = *st
				s.Subs.Notify(*retained)
			}
			slog.InfoContext(ctx, fmt.Sprintf("replicated scheduled instance status updatedAt=%v preparerStatus=%v runnerStatus=%v",
				st.UpdatedAt, st.Preparer.Rollup(), st.Runner.Status))
			return
		}
		entry = s.instanceStateLocked(inst)
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
	slog.InfoContext(ctx, fmt.Sprintf("replicated scheduled instance status updatedAt=%v preparerStatus=%v runnerStatus=%v",
		st.UpdatedAt, st.Preparer.Rollup(), st.Runner.Status))
}

func (s *Service) MustFetchDeploymentStatusHistory(deploymentID int32) []*apigen.ScheduledInstanceStatus {
	ctx := context.Background()
	rows := erru.Must(s.q.ListScheduledInstanceStatusHistoryForDeployment(ctx, int64(deploymentID)))
	out := make([]*apigen.ScheduledInstanceStatus, 0, len(rows))
	for _, r := range rows {
		out = append(out, scheduledInstanceStatusRowToProto(r))
	}
	return out
}

func (s *Service) MustFetchInstanceStatusHistory(instanceID int32) []*apigen.ScheduledInstanceStatus {
	ctx := context.Background()
	rows := erru.Must(s.q.ListScheduledInstanceStatusHistorySince(ctx, pq.ListScheduledInstanceStatusHistorySinceParams{
		ScheduledInstanceID: int64(instanceID),
	}))
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
