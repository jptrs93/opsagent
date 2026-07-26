package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/goutil/pubsubu"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

type deploymentStore struct {
	db *sql.DB
	q  *Queries

	mu sync.Mutex

	// configCache holds the latest desired DeploymentConfig per deployment id.
	// Used by primary scheduler/APIs only — never as the pinned config source for
	// a scheduled-instance snapshot.
	configCache map[int32]*apigen.DeploymentConfig
	// scheduledCache holds the authoritative runtime view per scheduled instance
	// id: assignment row, pinned config version, and latest status.
	scheduledCache map[int32]*apigen.ScheduledInstanceState

	configSubs   *pubsubu.PubSub[apigen.DeploymentConfig]
	instanceSubs *pubsubu.PubSub[apigen.ScheduledInstanceState]
}

func newDeploymentStore(db *sql.DB) *deploymentStore {
	s := &deploymentStore{
		db:             db,
		q:              New(db),
		configCache:    make(map[int32]*apigen.DeploymentConfig),
		scheduledCache: make(map[int32]*apigen.ScheduledInstanceState),
		configSubs:     &pubsubu.PubSub[apigen.DeploymentConfig]{},
		instanceSubs:   &pubsubu.PubSub[apigen.ScheduledInstanceState]{},
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

	instances, err := s.q.ListNonFinalScheduledInstances(ctx)
	if err != nil {
		panic(fmt.Sprintf("loadCache: ListNonFinalScheduledInstances: %v", err))
	}
	for _, row := range instances {
		inst := scheduledInstanceRowToProto(row)
		state := &apigen.ScheduledInstanceState{Instance: *inst}
		if cfg := s.configForInstanceLocked(inst); cfg != nil {
			state.Config = *cfg
		}
		s.scheduledCache[inst.ID] = state
	}

	statuses, err := s.q.ListLatestScheduledInstanceStatuses(ctx)
	if err != nil {
		panic(fmt.Sprintf("loadCache: ListLatestScheduledInstanceStatuses: %v", err))
	}
	for _, row := range statuses {
		st := scheduledInstanceStatusRowToProto(row)
		if state, ok := s.scheduledCache[st.ScheduledInstanceID]; ok {
			state.Status = *st
		}
	}
}

func (s *deploymentStore) FetchDeploymentSnapshot(predicate storage.DeploymentPredicate) []apigen.DeploymentConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.configSnapshotLocked(predicate)
}

// FetchDeploymentConfig returns the latest desired config, including a deleted
// tombstone, for scheduler reconciliation.
func (s *deploymentStore) FetchDeploymentConfig(deploymentID int32) *apigen.DeploymentConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.configCache[deploymentID]
	if cfg == nil {
		return nil
	}
	cp := *cfg
	return &cp
}

func (s *deploymentStore) MustFetchDeploymentConfigSnapshotAndSubscribe(predicate storage.DeploymentPredicate) ([]apigen.DeploymentConfig, chan apigen.DeploymentConfig, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := s.configSnapshotLocked(predicate)
	sub := s.configSubs.Subscribe(configFilter(predicate))
	return snapshot, sub.Ch, sub.UnsubscribeFunc
}

func (s *deploymentStore) FetchScheduledSnapshot(predicate storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.instanceSnapshotLocked(predicate)
}

func (s *deploymentStore) MustFetchScheduledSnapshotAndSubscribe(predicate storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan apigen.ScheduledInstanceState, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := s.instanceSnapshotLocked(predicate)
	sub := s.instanceSubs.Subscribe(instanceFilter(predicate))
	return snapshot, sub.Ch, sub.UnsubscribeFunc
}

func (s *deploymentStore) SubscribeScheduledInstanceUpdates(predicate storage.ScheduledInstancePredicate) (chan apigen.ScheduledInstanceState, func()) {
	sub := s.instanceSubs.Subscribe(instanceFilter(predicate))
	return sub.Ch, sub.UnsubscribeFunc
}

func (s *deploymentStore) MustWriteScheduledInstanceStatus(instanceID int32, f func(*apigen.ScheduledInstanceStatus) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	ctx = logu.ExtendLogContext(ctx, "scheduled_instance", instanceID)

	state := s.scheduledCache[instanceID]
	if state == nil {
		slog.WarnContext(ctx, "status write for unknown scheduled instance")
		return
	}

	current := state.Status
	if current.ScheduledInstanceID == 0 {
		current.ScheduledInstanceID = instanceID
		current.DeploymentID = state.Instance.DeploymentID
	}

	if !f(&current) {
		return
	}
	current.ScheduledInstanceID = instanceID
	current.DeploymentID = state.Instance.DeploymentID

	params := scheduledInstanceStatusProtoToInsertParams(&current)
	if err := s.q.InsertScheduledInstanceStatus(ctx, params); err != nil {
		panic(fmt.Sprintf("InsertScheduledInstanceStatus: %v", err))
	}

	state.Status = current
	slog.InfoContext(ctx, "scheduled instance status published",
		"updatedAt", current.UpdatedAt,
		"preparerStatus", current.Preparer.Status,
		"runnerStatus", current.Runner.Status,
	)
	s.notifyInstanceLocked(instanceID)
}

// FetchScheduledInstance returns the assignment alone. Callers reconciling a
// decision made earlier use it to confirm the placement still exists and is
// still in the state they left it in.
func (s *deploymentStore) FetchScheduledInstance(instanceID int32) *apigen.ScheduledInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.scheduledCache[instanceID]
	if state == nil {
		return nil
	}
	cp := state.Instance
	return &cp
}

func (s *deploymentStore) FetchScheduledInstanceStatus(instanceID int32) *apigen.ScheduledInstanceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.scheduledCache[instanceID]
	if state == nil || state.Status.IsZero() {
		return nil
	}
	cp := state.Status
	return &cp
}

func (s *deploymentStore) configSnapshotLocked(predicate storage.DeploymentPredicate) []apigen.DeploymentConfig {
	out := make([]apigen.DeploymentConfig, 0, len(s.configCache))
	for _, cfg := range s.configCache {
		if cfg.Deleted || (predicate != nil && !predicate(*cfg)) {
			continue
		}
		out = append(out, *cfg)
	}
	return out
}

func (s *deploymentStore) instanceSnapshotLocked(predicate storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState {
	out := make([]apigen.ScheduledInstanceState, 0, len(s.scheduledCache))
	for id, state := range s.scheduledCache {
		if state.Instance.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
			continue
		}
		item := s.instanceStateLocked(id)
		if predicate != nil && !predicate(item) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (s *deploymentStore) instanceStateLocked(id int32) apigen.ScheduledInstanceState {
	state := s.scheduledCache[id]
	if state == nil {
		return apigen.ScheduledInstanceState{}
	}
	out := *state
	if out.Config.ID != 0 {
		out.Status = withRunningVersion(&out.Config, out.Status)
	}
	return out
}

// configForInstanceLocked resolves a pinned config version for primary load/create.
// Secondaries should rely on config already embedded in scheduledCache entries.
func (s *deploymentStore) configForInstanceLocked(inst *apigen.ScheduledInstance) *apigen.DeploymentConfig {
	if inst == nil {
		return nil
	}
	if cfg := s.configCache[inst.DeploymentID]; cfg != nil && cfg.Version == inst.DeploymentVersion {
		return cfg
	}
	return s.loadConfigVersionLocked(inst.DeploymentID, inst.DeploymentVersion)
}

func (s *deploymentStore) loadConfigVersionLocked(deploymentID, version int32) *apigen.DeploymentConfig {
	row, err := s.q.GetDeploymentConfigHistoryVersion(context.Background(), GetDeploymentConfigHistoryVersionParams{
		DeploymentID: int64(deploymentID),
		Version:      int64(version),
	})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("load config history version", "deployment_id", deploymentID, "version", version, "err", err)
		}
		return nil
	}
	var identity apigen.DeploymentIdentity
	var createdAt time.Time
	if cfg := s.configCache[deploymentID]; cfg != nil {
		identity = cfg.Identity
		createdAt = cfg.CreatedAt
	}
	return configHistoryRowToFullProto(row, identity, createdAt)
}

func (s *deploymentStore) notifyConfigLocked(id int32) {
	cfg := s.configCache[id]
	if cfg == nil {
		return
	}
	s.configSubs.Notify(*cfg)
}

func (s *deploymentStore) notifyInstanceLocked(id int32) {
	state := s.instanceStateLocked(id)
	if state.Instance.ID == 0 {
		return
	}
	name := ""
	if !state.Config.Identity.IsZero() {
		name = fmt.Sprintf("%d:%d:%s", state.Config.Identity.SpaceID, state.Config.NodeID, state.Config.Identity.Name)
	}
	slog.Info("store: notify scheduled instance",
		"scheduled_instance", id,
		"deployment", state.Instance.DeploymentID,
		"name", name,
		"configVersion", state.Instance.DeploymentVersion,
		"targetState", state.Instance.State,
		"hasPreparer", !state.Status.Preparer.IsZero(),
		"hasRunner", !state.Status.Runner.IsZero(),
	)
	s.instanceSubs.Notify(state)
}

func configFilter(predicate storage.DeploymentPredicate) func(apigen.DeploymentConfig, apigen.DeploymentConfig) bool {
	if predicate == nil {
		return nil
	}
	return func(_, cfg apigen.DeploymentConfig) bool {
		return predicate(cfg)
	}
}

func instanceFilter(predicate storage.ScheduledInstancePredicate) func(apigen.ScheduledInstanceState, apigen.ScheduledInstanceState) bool {
	if predicate == nil {
		return nil
	}
	return func(_, state apigen.ScheduledInstanceState) bool {
		return predicate(state)
	}
}

func withRunningVersion(cfg *apigen.DeploymentConfig, st apigen.ScheduledInstanceStatus) apigen.ScheduledInstanceStatus {
	if st.Runner.IsZero() || cfg == nil {
		return st
	}
	ver := st.Runner.DeploymentConfigVersion
	if ver == 0 {
		return st
	}
	if ver == cfg.Version {
		st.Runner.RunningVersion = cfg.WorkloadVersion()
	}
	return st
}
