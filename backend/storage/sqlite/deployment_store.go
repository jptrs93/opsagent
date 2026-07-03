package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jptrs93/goutil/pubsubu"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type deploymentStore struct {
	db *sql.DB
	q  *Queries

	mu sync.Mutex

	configCache map[int32]*apigen.DeploymentConfig
	statusCache map[int32]*apigen.DeploymentStatus
	subs        *pubsubu.PubSub[apigen.DeploymentWithStatus]
}

func newDeploymentStore(db *sql.DB) *deploymentStore {
	s := &deploymentStore{
		db:          db,
		q:           New(db),
		configCache: make(map[int32]*apigen.DeploymentConfig),
		statusCache: make(map[int32]*apigen.DeploymentStatus),
		subs:        &pubsubu.PubSub[apigen.DeploymentWithStatus]{},
	}
	s.loadCache()
	return s
}

func (s *deploymentStore) loadCache() {
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

	for id := range s.configCache {
		if _, ok := s.statusCache[id]; ok {
			continue
		}
		s.insertDefaultStatus(s.q, int64(id))
	}
}

func (s *deploymentStore) FetchDeploymentSnapshot(machine string) []apigen.DeploymentWithStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked(machine)
}

func (s *deploymentStore) MustFetchSnapshotAndSubscribe(machine string) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot := s.snapshotLocked(machine)
	sub := s.subs.Subscribe(deploymentFilter(machine))
	return snapshot, sub.Ch, sub.UnsubscribeFunc
}

func (s *deploymentStore) MustWriteDeploymentStatus(deploymentID int32, f func(*apigen.DeploymentStatus) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()

	current := s.statusCache[deploymentID]
	if current == nil {
		current = &apigen.DeploymentStatus{DeploymentID: deploymentID}
	}

	if !f(current) {
		return
	}

	params := statusProtoToInsertParams(int64(deploymentID), current)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

	if err := q.UpsertDeploymentStatus(ctx, statusInsertToUpsert(params)); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentStatus: %v", err))
	}
	if err := q.InsertDeploymentStatusHistory(ctx, params); err != nil {
		panic(fmt.Sprintf("InsertDeploymentStatusHistory: %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	s.statusCache[deploymentID] = current
	s.notifyFromCache(deploymentID)
}

func (s *deploymentStore) FetchDeploymentStatus(deploymentID int32) *apigen.DeploymentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusCache[deploymentID]
}

func (s *deploymentStore) SubscribeDeploymentUpdates(machine string) (chan apigen.DeploymentWithStatus, func()) {
	sub := s.subs.Subscribe(deploymentFilter(machine))
	return sub.Ch, sub.UnsubscribeFunc
}

func (s *deploymentStore) insertDefaultStatus(q *Queries, dbID int64) {
	id := int32(dbID)
	st := &apigen.DeploymentStatus{DeploymentID: id}
	params := statusProtoToInsertParams(dbID, st)
	if err := q.UpsertDeploymentStatus(context.Background(), statusInsertToUpsert(params)); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentStatus (default): %v", err))
	}
	s.statusCache[id] = st
}

func (s *deploymentStore) snapshotLocked(machine string) []apigen.DeploymentWithStatus {
	out := make([]apigen.DeploymentWithStatus, 0, len(s.configCache))
	for id, cfg := range s.configCache {
		if cfg.Deleted || !deploymentMatchesMachine(cfg, machine) {
			continue
		}
		out = append(out, apigen.DeploymentWithStatus{
			Config: *cfg,
			Status: s.withRunningVersion(cfg, statusValue(s.statusCache[id])),
		})
	}
	return out
}

func (s *deploymentStore) notifyFromCache(id int32) {
	cfg := s.configCache[id]
	if cfg == nil {
		return
	}
	st := s.statusCache[id]
	name := ""
	if !cfg.ConfigID.IsZero() {
		name = fmt.Sprintf("%d:%s:%s", cfg.ConfigID.SpaceID, cfg.ConfigID.Machine, cfg.ConfigID.Name)
	}
	slog.Info("store: notifyFromCache",
		"id", id,
		"name", name,
		"configSeqNo", cfg.Version,
		"hasPreparer", st != nil && !st.Preparer.IsZero(),
		"hasRunner", st != nil && !st.Runner.IsZero(),
	)
	s.subs.Notify(apigen.DeploymentWithStatus{Config: *cfg, Status: s.withRunningVersion(cfg, statusValue(st))})
}

// withRunningVersion fills Status.Runner.RunningVersion — the version string
// (commit/tag) the running artifact was built from — by resolving the runner's
// config version against the config history. The current config (the common,
// steady-state case) is served from the in-memory cache; an older version, seen
// only while a rollout is in flight, falls back to a primary-key lookup in
// deployment_config_history.
func (s *deploymentStore) withRunningVersion(cfg *apigen.DeploymentConfig, st apigen.DeploymentStatus) apigen.DeploymentStatus {
	if st.Runner.IsZero() {
		return st
	}
	ver := st.Runner.DeploymentConfigVersion
	if ver == 0 {
		return st
	}
	if cfg != nil && ver == cfg.Version {
		st.Runner.RunningVersion = cfg.DesiredState.Version
		return st
	}
	dv, err := s.q.GetConfigHistoryDesiredVersion(context.Background(), GetConfigHistoryDesiredVersionParams{
		DeploymentID: int64(st.DeploymentID),
		Version:      int64(ver),
	})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("store: resolve running version", "id", st.DeploymentID, "version", ver, "err", err)
		}
		return st
	}
	st.Runner.RunningVersion = dv
	return st
}

func deploymentFilter(machine string) func(apigen.DeploymentWithStatus, apigen.DeploymentWithStatus) bool {
	if machine == "" {
		return nil
	}
	return func(prev, dws apigen.DeploymentWithStatus) bool {
		return deploymentMatchesMachine(&dws.Config, machine)
	}
}

func deploymentMatchesMachine(cfg *apigen.DeploymentConfig, machine string) bool {
	return machine == "" || (cfg != nil && cfg.ConfigID.Machine == machine)
}

func statusValue(st *apigen.DeploymentStatus) apigen.DeploymentStatus {
	if st == nil {
		return apigen.DeploymentStatus{}
	}
	return *st
}
