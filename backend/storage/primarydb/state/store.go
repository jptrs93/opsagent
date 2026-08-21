package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/instancecache"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
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

func (s *Service) loadCache() {
	ctx := context.Background()
	rows, err := s.q.ListAllDeployments(ctx)
	if err != nil {
		panic(fmt.Sprintf("loadCache: ListAllDeployments: %v", err))
	}
	for _, row := range rows {
		id := int32(row.DeploymentID)
		s.configCache[id] = configRowToProto(row)
	}
	spaceRows, err := s.q.ListCurrentDeploymentSpaceVersions(ctx)
	if err != nil {
		panic(fmt.Sprintf("loadCache: ListCurrentDeploymentSpaceVersions: %v", err))
	}
	for _, row := range spaceRows {
		s.spaceVersionRowIDs[int32(row.DeploymentID)] = row.ID
	}

	instances, err := s.q.ListNonFinalScheduledInstances(ctx)
	if err != nil {
		panic(fmt.Sprintf("loadCache: ListNonFinalScheduledInstances: %v", err))
	}
	byID := make(map[int32]*apigen.ScheduledInstanceState, len(instances))
	for _, row := range instances {
		inst := scheduledInstanceRowToProto(row)
		state := s.instanceStateLocked(inst)
		s.Scheduled[inst.ID] = state
		byID[inst.ID] = state
	}

	// Rebuild the retained view. Only the newest incarnation of an ordinal is a
	// candidate, and only while it is finalized: anything live is already in
	// the live cache and speaks for the ordinal itself.
	latest, err := s.q.ListLatestScheduledInstancePerOrdinal(ctx)
	if err != nil {
		panic(fmt.Sprintf("loadCache: ListLatestScheduledInstancePerOrdinal: %v", err))
	}
	for _, row := range latest {
		inst := scheduledInstanceRowToProto(row)
		if !inst.State.IsFinal() {
			continue
		}
		state := s.instanceStateLocked(inst)
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

func (s *Service) FetchDeploymentSnapshot(predicate storage.DeploymentPredicate) []apigen.DeploymentConfig {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.configSnapshotLocked(predicate)
}

// FetchDeletedDeploymentSnapshot returns the tombstone config of the most
// recently deleted deployments, newest first, capped at limit. Deletion writes a
// config version rather than removing the row, so these stay cached alongside
// live ones; every other snapshot filters them out because a deleted deployment
// schedules nothing. Deletion order is the config's update time, which for a
// tombstone is when the delete was written.
func (s *Service) FetchDeletedDeploymentSnapshot(predicate storage.DeploymentPredicate, limit int) []apigen.DeploymentConfig {
	s.Mu.Lock()
	defer s.Mu.Unlock()
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
func (s *Service) FetchDeploymentConfig(deploymentID int32) *apigen.DeploymentConfig {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	cfg := s.configCache[deploymentID]
	if cfg == nil {
		return nil
	}
	cp := *cfg
	return &cp
}

func (s *Service) MustFetchDeploymentConfigSnapshotAndSubscribe(predicate storage.DeploymentPredicate) ([]apigen.DeploymentConfig, chan apigen.DeploymentConfig, func()) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	snapshot := s.configSnapshotLocked(predicate)
	sub := s.configSubs.Subscribe(configFilter(predicate))
	return snapshot, sub.Ch, sub.Unsubscribe
}

// FetchScheduledSnapshotWithLatestFinal is the display view: every live instance
// plus, for each ordinal that has none, the finalized instance that ran last.
// Reconciliation and routing must not use it — a finalized placement owns
// nothing and must not be acted on.
func (s *Service) FetchScheduledSnapshotWithLatestFinal(predicate storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.instanceSnapshotWithLatestFinalLocked(predicate)
}

// MustFetchScheduledSnapshotWithLatestFinalAndSubscribe pairs the display view
// with the unfiltered update stream. Finalization is published before the
// instance leaves the live cache, so a subscriber that starts from this snapshot
// sees every subsequent transition, including the one that retires an ordinal.
func (s *Service) MustFetchScheduledSnapshotWithLatestFinalAndSubscribe(predicate storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan apigen.ScheduledInstanceState, func()) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	snapshot := s.instanceSnapshotWithLatestFinalLocked(predicate)
	sub := s.Subs.Subscribe(instancecache.InstanceFilter(predicate))
	return snapshot, sub.Ch, sub.Unsubscribe
}

func (s *Service) configSnapshotLocked(predicate storage.DeploymentPredicate) []apigen.DeploymentConfig {
	out := make([]apigen.DeploymentConfig, 0, len(s.configCache))
	for _, cfg := range s.configCache {
		if cfg.Deleted || (predicate != nil && !predicate(*cfg)) {
			continue
		}
		out = append(out, *cfg)
	}
	return out
}

func (s *Service) instanceSnapshotWithLatestFinalLocked(predicate storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState {
	out := s.SnapshotLocked(predicate)
	for _, state := range s.latestFinalCache {
		item := *state
		if item.Config.ID != 0 {
			item.Status = instancecache.WithRunningVersion(&item.Config, item.Status)
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
func (s *Service) retainFinalizedLocked(state *apigen.ScheduledInstanceState) {
	inst := &state.Instance
	key := ordinalKeyOf(inst)
	for _, live := range s.Scheduled {
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

func (s *Service) instanceStateLocked(inst *apigen.ScheduledInstance) *apigen.ScheduledInstanceState {
	state := &apigen.ScheduledInstanceState{Instance: *inst}
	if cfg := s.configForInstanceLocked(inst); cfg != nil {
		state.Config = *cfg
		state.Config.SpaceID = inst.SpaceID
	}
	return state
}

// configForInstanceLocked resolves a pinned config version for primary
// load/create paths.
func (s *Service) configForInstanceLocked(inst *apigen.ScheduledInstance) *apigen.DeploymentConfig {
	if inst == nil {
		return nil
	}
	if cfg := s.configCache[inst.DeploymentID]; cfg != nil && cfg.Version == inst.DeploymentVersion {
		return cfg
	}
	return s.loadConfigVersionLocked(inst.DeploymentID, inst.DeploymentVersion)
}

func (s *Service) loadConfigVersionLocked(deploymentID, version int32) *apigen.DeploymentConfig {
	row, err := s.q.GetDeploymentVersion(context.Background(), pq.GetDeploymentVersionParams{
		DeploymentID: int64(deploymentID),
		Version:      int64(version),
	})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("load config version", "deployment_id", deploymentID, "version", version, "err", err)
		}
		return nil
	}
	return configVersionRowToProto(row, s.configCache[deploymentID])
}

func (s *Service) notifyConfigLocked(id int32) {
	cfg := s.configCache[id]
	if cfg == nil {
		return
	}
	s.configSubs.Notify(*cfg)
}

func configFilter(predicate storage.DeploymentPredicate) func(apigen.DeploymentConfig, apigen.DeploymentConfig) bool {
	if predicate == nil {
		return nil
	}
	return func(_, cfg apigen.DeploymentConfig) bool {
		return predicate(cfg)
	}
}
