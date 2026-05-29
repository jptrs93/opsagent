package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/logstore"
)

// SecondaryStorageAdapter is the storage layer for secondary (slave) nodes.
// It uses the primary's integer ID directly and fully owns deployment status.
// It shares the primary's schema (schema.sql) and generated queries; the
// deployment_identifiers, deployment_config_history, users, and public_keys
// tables exist but are never written to on a secondary.
type SecondaryStorageAdapter struct {
	db *sql.DB
	q  *Queries

	mu sync.Mutex

	configCache map[int32]*apigen.DeploymentConfig
	statusCache map[int32]*apigen.DeploymentStatus
	subs        *logstore.Subs[apigen.DeploymentWithStatus]
}

func NewSecondaryStorageAdapter(dbPath string) *SecondaryStorageAdapter {
	db := mustInitSecondary(dbPath)
	s := &SecondaryStorageAdapter{
		db:          db,
		q:           New(db),
		configCache: make(map[int32]*apigen.DeploymentConfig),
		statusCache: make(map[int32]*apigen.DeploymentStatus),
		subs:        &logstore.Subs[apigen.DeploymentWithStatus]{},
	}
	s.loadCache()
	return s
}

func (s *SecondaryStorageAdapter) loadCache() {
	ctx := context.Background()

	rows, err := s.q.ListAllDeploymentConfigs(ctx)
	if err != nil {
		panic(fmt.Sprintf("loadCache: ListAllDeploymentConfigs: %v", err))
	}
	for _, row := range rows {
		id := int32(row.DeploymentID)
		s.configCache[id] = configRowToProto(row)
	}

	statuses, err := s.q.ListAllDeploymentStatuses(ctx)
	if err != nil {
		panic(fmt.Sprintf("loadCache: ListAllDeploymentStatuses: %v", err))
	}
	for _, st := range statuses {
		id := int32(st.DeploymentID)
		s.statusCache[id] = statusRowToProto(st.DeploymentID, statusToHistory(st))
	}

	// Ensure every config has a status entry (invariant: status is never nil).
	for id := range s.configCache {
		if _, ok := s.statusCache[id]; ok {
			continue
		}
		// Zero UpdatedAt marks a "no status yet" placeholder.
		st := &apigen.DeploymentStatus{DeploymentID: id}
		params := statusProtoToInsertParams(int64(id), st)
		if err := s.q.UpsertDeploymentStatus(ctx, statusInsertToUpsert(params)); err != nil {
			panic(fmt.Sprintf("loadCache: UpsertDeploymentStatus (default): %v", err))
		}
		// No history row: the placeholder carries no preparer/runner data,
		// so it would show up as a meaningless "status update" entry in the
		// UI. The current-row upsert above is enough to maintain the
		// status-never-nil invariant.
		s.statusCache[id] = st
	}
}

// MustWriteDeploymentConfig persists a full DeploymentConfig received from the primary.
func (s *SecondaryStorageAdapter) MustWriteDeploymentConfig(ctx context.Context, cfg *apigen.DeploymentConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := cfg.ID
	dbID := int64(id)
	_, exists := s.configCache[id]

	if err := s.q.UpsertDeploymentConfig(ctx, configProtoToUpsertParams(cfg)); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentConfig: %v", err))
	}

	s.configCache[id] = cfg

	if !exists {
		// Zero UpdatedAt marks a "no status yet" placeholder.
		st := &apigen.DeploymentStatus{DeploymentID: id}
		params := statusProtoToInsertParams(dbID, st)
		if err := s.q.UpsertDeploymentStatus(ctx, statusInsertToUpsert(params)); err != nil {
			panic(fmt.Sprintf("UpsertDeploymentStatus (default): %v", err))
		}
		// No history row: the placeholder carries no preparer/runner data,
		// so it would show up as a meaningless "status update" entry in the
		// UI. The current-row upsert above is enough to maintain the
		// status-never-nil invariant.
		s.statusCache[id] = st
	}

	s.notifyFromCache(id)
}

// MustWriteDeploymentStatus applies a mutation to the current status and persists it.
// If the callback returns false, no DB writes happen — callers use this to
// skip stale/superseded writes that would otherwise collide on the
// (deployment_id, updated_at) primary key of deployment_status_history.
func (s *SecondaryStorageAdapter) MustWriteDeploymentStatus(ctx context.Context, deploymentID int32, f func(*apigen.DeploymentStatus) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current := s.statusCache[deploymentID]
	if current == nil {
		current = &apigen.DeploymentStatus{DeploymentID: deploymentID}
	}

	if !f(current) {
		return
	}

	params := statusProtoToInsertParams(int64(deploymentID), current)
	if err := s.q.UpsertDeploymentStatus(ctx, statusInsertToUpsert(params)); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentStatus: %v", err))
	}
	if err := s.q.InsertDeploymentStatusHistory(ctx, params); err != nil {
		panic(fmt.Sprintf("InsertDeploymentStatusHistory: %v", err))
	}

	s.statusCache[deploymentID] = current
	s.notifyFromCache(deploymentID)
}

// FetchDeploymentStatusHistorySince returns history rows with
// updated_at > since, in ascending order. Used on reconnect to
// replay any history the primary is missing.
func (s *SecondaryStorageAdapter) FetchDeploymentStatusHistorySince(deploymentID int32, since time.Time) []*apigen.DeploymentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.q.ListDeploymentStatusHistorySince(context.Background(), ListDeploymentStatusHistorySinceParams{
		DeploymentID: int64(deploymentID),
		UpdatedAt:    clockToNanos(since),
	})
	if err != nil {
		panic(fmt.Sprintf("FetchDeploymentStatusHistorySince: %v", err))
	}

	out := make([]*apigen.DeploymentStatus, 0, len(rows))
	for _, r := range rows {
		out = append(out, statusRowToProto(r.DeploymentID, r))
	}
	return out
}

func (s *SecondaryStorageAdapter) MustFetchSnapshotAndSubscribe(ctx context.Context, machine string) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var snapshot []apigen.DeploymentWithStatus
	for id, cfg := range s.configCache {
		if machine != "" && (cfg.ConfigID == nil || cfg.ConfigID.Machine != machine) {
			continue
		}
		if cfg.Deleted {
			continue
		}
		snapshot = append(snapshot, apigen.DeploymentWithStatus{
			Config: cfg,
			Status: s.statusCache[id],
		})
	}

	var filter func(apigen.DeploymentWithStatus) bool
	if machine != "" {
		filter = func(dws apigen.DeploymentWithStatus) bool {
			return dws.Config.ConfigID != nil && dws.Config.ConfigID.Machine == machine
		}
	}
	sub, _ := s.subs.Subscribe(filter)
	return snapshot, sub.Ch
}

func (s *SecondaryStorageAdapter) FetchDeploymentStatus(deploymentID int32) *apigen.DeploymentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusCache[deploymentID]
}

func (s *SecondaryStorageAdapter) SubscribeDeploymentUpdates(machine string) (chan apigen.DeploymentWithStatus, func()) {
	var filter func(apigen.DeploymentWithStatus) bool
	if machine != "" {
		filter = func(dws apigen.DeploymentWithStatus) bool {
			return dws.Config.ConfigID != nil && dws.Config.ConfigID.Machine == machine
		}
	}
	sub, unsub := s.subs.Subscribe(filter)
	return sub.Ch, unsub
}

// --- internal helpers ---

func (s *SecondaryStorageAdapter) notifyFromCache(id int32) {
	cfg := s.configCache[id]
	if cfg == nil {
		return
	}
	st := s.statusCache[id]
	s.subs.Notify(apigen.DeploymentWithStatus{Config: cfg, Status: st})
}
