package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/goutil/pubsubu"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

// instanceOrdinalKey identifies the logical slot a scheduled instance is an
// incarnation of. Instances come and go; the ordinal is what the UI shows a row
// for.
type instanceOrdinalKey struct {
	deploymentID int32
	ordinal      int32
}

func ordinalKeyOf(inst *apigen.ScheduledInstance) instanceOrdinalKey {
	return instanceOrdinalKey{deploymentID: inst.DeploymentID, ordinal: inst.InstanceOrdinal}
}

type deploymentStore struct {
	db *sql.DB
	q  *Queries

	mu sync.Mutex

	// configCache holds the latest desired DeploymentConfig per deployment id.
	// Used by primary scheduler/APIs only — never as the pinned config source for
	// a scheduled-instance snapshot.
	configCache map[int32]*apigen.DeploymentConfig
	// scheduledCache holds the authoritative runtime view per scheduled instance
	// id: assignment row, pinned config version, and latest status. Live
	// instances only — a finalized instance is removed, and every consumer that
	// reconciles or routes depends on that.
	scheduledCache map[int32]*apigen.ScheduledInstanceState
	// latestFinalCache retains the last incarnation of an ordinal after it is
	// finalized, so a stopped deployment can still show how its final run ended.
	// At most one entry per ordinal, and only while no live instance supersedes
	// it, so it never competes with scheduledCache for the same ordinal.
	latestFinalCache map[instanceOrdinalKey]*apigen.ScheduledInstanceState

	configSubs   *pubsubu.PubSub[apigen.DeploymentConfig]
	instanceSubs *pubsubu.PubSub[apigen.ScheduledInstanceState]
}

func newDeploymentStore(db *sql.DB) *deploymentStore {
	s := &deploymentStore{
		db:               db,
		q:                New(db),
		configCache:      make(map[int32]*apigen.DeploymentConfig),
		scheduledCache:   make(map[int32]*apigen.ScheduledInstanceState),
		latestFinalCache: make(map[instanceOrdinalKey]*apigen.ScheduledInstanceState),
		configSubs:       &pubsubu.PubSub[apigen.DeploymentConfig]{},
		instanceSubs:     &pubsubu.PubSub[apigen.ScheduledInstanceState]{},
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
	byID := make(map[int32]*apigen.ScheduledInstanceState, len(instances))
	for _, row := range instances {
		inst := scheduledInstanceRowToProto(row)
		state := &apigen.ScheduledInstanceState{Instance: *inst}
		if cfg := s.configForInstanceLocked(inst); cfg != nil {
			state.Config = *cfg
		}
		s.scheduledCache[inst.ID] = state
		byID[inst.ID] = state
	}

	// Rebuild the retained view. Only the newest incarnation of an ordinal is a
	// candidate, and only while it is finalized: anything live is already in
	// scheduledCache and speaks for the ordinal itself.
	latest, err := s.q.ListLatestScheduledInstancePerOrdinal(ctx)
	if err != nil {
		panic(fmt.Sprintf("loadCache: ListLatestScheduledInstancePerOrdinal: %v", err))
	}
	for _, row := range latest {
		inst := scheduledInstanceRowToProto(row)
		if !inst.State.IsFinal() {
			continue
		}
		state := &apigen.ScheduledInstanceState{Instance: *inst}
		if cfg := s.configForInstanceLocked(inst); cfg != nil {
			state.Config = *cfg
		}
		s.latestFinalCache[ordinalKeyOf(inst)] = state
		byID[inst.ID] = state
	}

	statuses, err := s.q.ListLatestScheduledInstanceStatuses(ctx)
	if err != nil {
		panic(fmt.Sprintf("loadCache: ListLatestScheduledInstanceStatuses: %v", err))
	}
	for _, row := range statuses {
		st := scheduledInstanceStatusRowToProto(row)
		if state, ok := byID[st.ScheduledInstanceID]; ok {
			state.Status = *st
		}
	}
}

func (s *deploymentStore) FetchDeploymentSnapshot(predicate storage.DeploymentPredicate) []apigen.DeploymentConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.configSnapshotLocked(predicate)
}

// FetchDeletedDeploymentSnapshot returns the tombstone config of the most
// recently deleted deployments, newest first, capped at limit. Deletion writes a
// config version rather than removing the row, so these stay cached alongside
// live ones; every other snapshot filters them out because a deleted deployment
// schedules nothing. Deletion order is the config's update time, which for a
// tombstone is when the delete was written.
func (s *deploymentStore) FetchDeletedDeploymentSnapshot(predicate storage.DeploymentPredicate, limit int) []apigen.DeploymentConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]apigen.DeploymentConfig, 0, limit)
	for _, cfg := range s.configCache {
		if !cfg.Deleted || (predicate != nil && !predicate(*cfg)) {
			continue
		}
		out = append(out, *cfg)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		// Deletions written in the same millisecond still need a total order, or
		// the caller's cap would pick arbitrarily between them across calls.
		return out[i].ID > out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
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

// FetchScheduledSnapshotWithLatestFinal is the display view: every live instance
// plus, for each ordinal that has none, the finalized instance that ran last.
// Reconciliation and routing must not use it — a finalized placement owns
// nothing and must not be acted on.
func (s *deploymentStore) FetchScheduledSnapshotWithLatestFinal(predicate storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.instanceSnapshotWithLatestFinalLocked(predicate)
}

// MustFetchScheduledSnapshotWithLatestFinalAndSubscribe pairs the display view
// with the unfiltered update stream. Finalization is published before the
// instance leaves scheduledCache, so a subscriber that starts from this snapshot
// sees every subsequent transition, including the one that retires an ordinal.
func (s *deploymentStore) MustFetchScheduledSnapshotWithLatestFinalAndSubscribe(predicate storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan apigen.ScheduledInstanceState, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := s.instanceSnapshotWithLatestFinalLocked(predicate)
	sub := s.instanceSubs.Subscribe(instanceFilter(predicate))
	return snapshot, sub.Ch, sub.UnsubscribeFunc
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
		"preparerStatus", current.Preparer.Rollup(),
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

func (s *deploymentStore) instanceSnapshotWithLatestFinalLocked(predicate storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState {
	out := s.instanceSnapshotLocked(predicate)
	for _, state := range s.latestFinalCache {
		item := *state
		if item.Config.ID != 0 {
			item.Status = withRunningVersion(&item.Config, item.Status)
		}
		if predicate != nil && !predicate(item) {
			continue
		}
		out = append(out, item)
	}
	return out
}

// retainFinalizedLocked keeps a just-finalized instance as the ordinal's last
// known runtime. A newer instance for the same ordinal already speaks for it, so
// an out-of-order finalization of an older incarnation is dropped rather than
// overwriting the live one's slot.
func (s *deploymentStore) retainFinalizedLocked(state *apigen.ScheduledInstanceState) {
	inst := &state.Instance
	key := ordinalKeyOf(inst)
	for _, live := range s.scheduledCache {
		if ordinalKeyOf(&live.Instance) == key && live.Instance.ID > inst.ID {
			return
		}
	}
	if existing := s.latestFinalCache[key]; existing != nil && existing.Instance.ID > inst.ID {
		return
	}
	cp := *state
	s.latestFinalCache[key] = &cp
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
