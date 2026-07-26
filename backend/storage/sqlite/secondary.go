package sqlite

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// SecondaryStorage is the storage layer for secondary (worker) nodes.
type SecondaryStorage struct {
	*deploymentStore
}

func NewSecondaryStorage(dbPath string) *SecondaryStorage {
	db := mustInitSecondary(dbPath)
	s := &SecondaryStorage{
		deploymentStore: newDeploymentStore(db),
	}
	s.loadLocalScheduledInstanceCache()
	return s
}

func (s *SecondaryStorage) Close() error {
	return s.db.Close()
}

func (s *SecondaryStorage) loadLocalScheduledInstanceCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Primary-style tables may be empty on a secondary; the durable assignment
	// source is local_scheduled_instance_cache. Each blob is a full
	// ScheduledInstanceState with its pinned config version.
	s.scheduledCache = make(map[int32]*apigen.ScheduledInstanceState)

	rows, err := s.q.ListLocalScheduledInstanceCache(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListLocalScheduledInstanceCache: %v", err))
	}
	for _, row := range rows {
		state, err := apigen.DecodeScheduledInstanceState(row.Blob)
		if err != nil {
			panic(fmt.Sprintf("decode local scheduled instance %d: %v", row.InstanceID, err))
		}
		if state.Instance.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
			continue
		}
		cp := *state
		s.scheduledCache[cp.Instance.ID] = &cp
	}
	// Prefer durable local status rows over the watermark embedded in the assignment blob.
	statuses, err := s.q.ListLatestScheduledInstanceStatuses(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListLatestScheduledInstanceStatuses: %v", err))
	}
	for _, row := range statuses {
		st := scheduledInstanceStatusRowToProto(row)
		if state, ok := s.scheduledCache[st.ScheduledInstanceID]; ok {
			state.Status = *st
		}
	}
}

// MustWriteScheduledInstanceAssignment durably stores a full assignment blob,
// updates the in-memory ScheduledInstanceState cache, then publishes.
func (s *SecondaryStorage) MustWriteScheduledInstanceAssignment(state *apigen.ScheduledInstanceState) {
	if state == nil || state.Instance.ID == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	id := state.Instance.ID

	if state.Instance.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
		s.finalizeLocked(ctx, state)
		return
	}

	if err := s.q.UpsertLocalScheduledInstanceCache(ctx, UpsertLocalScheduledInstanceCacheParams{
		InstanceID: int64(id),
		Blob:       state.Encode(),
	}); err != nil {
		panic(fmt.Sprintf("UpsertLocalScheduledInstanceCache: %v", err))
	}

	cp := *state
	// Preserve newer local status if the assignment only carries a clock watermark.
	if existing := s.scheduledCache[id]; existing != nil && !existing.Status.IsZero() {
		if cp.Status.IsZero() || existing.Status.UpdatedAt.After(cp.Status.UpdatedAt) {
			cp.Status = existing.Status
		}
	}
	s.scheduledCache[id] = &cp
	s.notifyInstanceLocked(id)
}

// finalizeLocked removes an instance from durable local storage and the cache,
// publishing a FINALIZED state on the way out so the operator tears the workload
// down rather than merely forgetting about it. Caller must hold s.mu.
func (s *SecondaryStorage) finalizeLocked(ctx context.Context, state *apigen.ScheduledInstanceState) {
	id := state.Instance.ID
	if err := s.q.DeleteLocalScheduledInstanceCache(ctx, int64(id)); err != nil {
		panic(fmt.Sprintf("DeleteLocalScheduledInstanceCache: %v", err))
	}
	cp := *state
	cp.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED
	if existing := s.scheduledCache[id]; existing != nil && !existing.Status.IsZero() {
		if cp.Status.IsZero() || existing.Status.UpdatedAt.After(cp.Status.UpdatedAt) {
			cp.Status = existing.Status
		}
	}
	s.scheduledCache[id] = &cp
	s.notifyInstanceLocked(id)
	delete(s.scheduledCache, id)
}

// MustFinalizeScheduledInstancesAbsent finalizes every locally held instance whose
// id is missing from present, returning the ids it dropped.
//
// The primary's snapshot is its complete set of assignments for this node, so
// anything held locally and absent from it is an instance the primary no longer
// knows about — and precisely because it is gone there, no FINALIZED update for it
// can ever arrive. Reconciling only on receipt would leave the assignment, its
// durable cache row, and its running workload alive across every restart.
func (s *SecondaryStorage) MustFinalizeScheduledInstancesAbsent(present map[int32]struct{}) []int32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	stale := make([]int32, 0)
	for id := range s.scheduledCache {
		if _, ok := present[id]; !ok {
			stale = append(stale, id)
		}
	}
	slices.Sort(stale)
	ctx := context.Background()
	for _, id := range stale {
		s.finalizeLocked(ctx, s.scheduledCache[id])
	}
	return stale
}

func (s *SecondaryStorage) FetchScheduledInstanceStatusHistorySince(instanceID int32, since time.Time) []*apigen.ScheduledInstanceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.q.ListScheduledInstanceStatusHistorySince(context.Background(), ListScheduledInstanceStatusHistorySinceParams{
		ScheduledInstanceID: int64(instanceID),
		UpdatedAt:           clockToNanos(since),
	})
	if err != nil {
		panic(fmt.Sprintf("FetchScheduledInstanceStatusHistorySince: %v", err))
	}
	out := make([]*apigen.ScheduledInstanceStatus, 0, len(rows))
	for _, r := range rows {
		out = append(out, scheduledInstanceStatusRowToProto(r))
	}
	return out
}
