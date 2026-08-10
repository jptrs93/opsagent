package secondarydb

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/instancecache"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

// Storage is the storage layer for secondary (worker) nodes. Its schema is
// fully independent of the primary's and holds only machine-local runtime
// state; see sql/schema.sql.
type Storage struct {
	// Cache is the shared scheduled-instance runtime view; its Mu is the
	// storage-wide mutex.
	*instancecache.Cache

	db *sql.DB
	q  *Queries
}

func Open(dbPath string) *Storage {
	db := mustInit(dbPath)
	s := &Storage{
		db: db,
		q:  New(db),
	}
	s.Cache = instancecache.New(s.persistStatus)
	s.loadLocalScheduledInstanceCache()
	return s
}

func mustInit(dbPath string) *sql.DB {
	db := sqlitedb.MustOpen(dbPath)
	sqlitedb.ApplySchema(db, schemaFiles, "sql/schema.sql")
	sqlitedb.ApplyMigrations(db, migrations)
	return db
}

func (s *Storage) Close() error {
	return s.db.Close()
}

// persistStatus is the instancecache persistence hook: it durably appends a
// status row, panicking on failure per the storage error policy.
func (s *Storage) persistStatus(ctx context.Context, st *apigen.ScheduledInstanceStatus) {
	if err := s.q.InsertScheduledInstanceStatus(ctx, scheduledInstanceStatusProtoToInsertParams(st)); err != nil {
		panic(fmt.Sprintf("InsertScheduledInstanceStatus: %v", err))
	}
}

func (s *Storage) loadLocalScheduledInstanceCache() {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// The durable assignment source is local_scheduled_instance_cache. Each
	// blob is a full ScheduledInstanceState with its pinned config version.
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
		s.Scheduled[cp.Instance.ID] = &cp
	}
	// Prefer durable local status rows over the watermark embedded in the assignment blob.
	statuses, err := s.q.ListLatestScheduledInstanceStatuses(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListLatestScheduledInstanceStatuses: %v", err))
	}
	for _, row := range statuses {
		st := scheduledInstanceStatusRowToProto(row)
		if state, ok := s.Scheduled[st.ScheduledInstanceID]; ok {
			state.Status = *st
		}
	}
}

// MustWriteScheduledInstanceAssignment durably stores a full assignment blob,
// updates the in-memory ScheduledInstanceState cache, then publishes.
func (s *Storage) MustWriteScheduledInstanceAssignment(state *apigen.ScheduledInstanceState) {
	if state == nil || state.Instance.ID == 0 {
		return
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()
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
	if existing := s.Scheduled[id]; existing != nil && !existing.Status.IsZero() {
		if cp.Status.IsZero() || existing.Status.UpdatedAt.After(cp.Status.UpdatedAt) {
			cp.Status = existing.Status
		}
	}
	s.Scheduled[id] = &cp
	s.NotifyInstanceLocked(id)
}

// finalizeLocked removes an instance from durable local storage and the cache,
// publishing a FINALIZED state on the way out so the operator tears the workload
// down rather than merely forgetting about it. Caller must hold s.Mu.
func (s *Storage) finalizeLocked(ctx context.Context, state *apigen.ScheduledInstanceState) {
	id := state.Instance.ID
	if err := s.q.DeleteLocalScheduledInstanceCache(ctx, int64(id)); err != nil {
		panic(fmt.Sprintf("DeleteLocalScheduledInstanceCache: %v", err))
	}
	cp := *state
	cp.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED
	if existing := s.Scheduled[id]; existing != nil && !existing.Status.IsZero() {
		if cp.Status.IsZero() || existing.Status.UpdatedAt.After(cp.Status.UpdatedAt) {
			cp.Status = existing.Status
		}
	}
	s.Scheduled[id] = &cp
	s.NotifyInstanceLocked(id)
	delete(s.Scheduled, id)
}

// MustFinalizeScheduledInstancesAbsent finalizes every locally held instance whose
// id is missing from present, returning the ids it dropped.
//
// The primary's snapshot is its complete set of assignments for this node, so
// anything held locally and absent from it is an instance the primary no longer
// knows about — and precisely because it is gone there, no FINALIZED update for it
// can ever arrive. Reconciling only on receipt would leave the assignment, its
// durable cache row, and its running workload alive across every restart.
func (s *Storage) MustFinalizeScheduledInstancesAbsent(present map[int32]struct{}) []int32 {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	stale := make([]int32, 0)
	for id := range s.Scheduled {
		if _, ok := present[id]; !ok {
			stale = append(stale, id)
		}
	}
	slices.Sort(stale)
	ctx := context.Background()
	for _, id := range stale {
		s.finalizeLocked(ctx, s.Scheduled[id])
	}
	return stale
}

func (s *Storage) FetchScheduledInstanceStatusHistorySince(instanceID int32, since time.Time) []*apigen.ScheduledInstanceStatus {
	s.Mu.Lock()
	defer s.Mu.Unlock()
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
