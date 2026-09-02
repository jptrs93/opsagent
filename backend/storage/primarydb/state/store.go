package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jptrs93/goutil/erru"
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
	events := erru.Must(s.q.ListLatestDeploymentEvents(ctx))
	for _, e := range events {
		if e.EventType == pq.DeploymentEventDelete {
			continue
		}
		cfg := deploymentFromRow(e)
		s.deploymentCache[cfg.ID] = cfg
		s.latestEvents[cfg.ID] = e
	}

	instances := erru.Must(s.q.ListNonFinalScheduledInstances(ctx))
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
	latest := erru.Must(s.q.ListLatestScheduledInstancePerOrdinal(ctx))
	for _, row := range latest {
		inst := scheduledInstanceRowToProto(row)
		if !inst.State.IsFinal() {
			continue
		}
		state := s.instanceStateLocked(inst)
		s.latestFinalCache[ordinalKeyOf(inst)] = state
		byID[inst.ID] = state
	}

	statuses := erru.Must(s.q.ListLatestScheduledInstanceStatuses(ctx))
	for _, row := range statuses {
		st := scheduledInstanceStatusRowToProto(row)
		if state, ok := byID[st.ScheduledInstanceID]; ok {
			state.Status = *st
		}
	}
}

func (s *Service) FetchDeploymentSnapshot(predicate storage.DeploymentPredicate) []apigen.Deployment {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.configSnapshotLocked(predicate)
}

// FetchDeletedDeploymentSnapshot returns the tombstone config of the most
// recently deleted deployments, newest first, capped at limit. A delete event
// is terminal — every write path refuses a deleted deployment — so the delete
// rows are the tombstones, read straight from the event log. The limit is
// applied after the caller's predicate, which filters on the decoded config,
// so the query itself cannot be capped.
func (s *Service) FetchDeletedDeploymentSnapshot(predicate storage.DeploymentPredicate, limit int) []apigen.Deployment {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	events := erru.Must(s.q.ListDeletedDeploymentEvents(context.Background()))
	out := make([]apigen.Deployment, 0, limit)
	for _, e := range events {
		cfg := deploymentFromRow(e)
		if predicate != nil && !predicate(*cfg) {
			continue
		}
		out = append(out, *cfg)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// FetchDeployment returns the latest desired config. The cache holds live
// deployments only, so a miss falls back to the event log: deleted deployments
// resolve to their delete tombstone — which the scheduler reconciles instance
// teardown against — and nil means the deployment never existed.
func (s *Service) FetchDeployment(deploymentID int32) *apigen.Deployment {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if cfg := s.deploymentCache[deploymentID]; cfg != nil {
		cp := *cfg
		return &cp
	}
	event, err := s.q.GetLatestDeploymentEvent(context.Background(), int64(deploymentID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		panic(fmt.Sprintf("fetch deployment %d: %v", deploymentID, err))
	}
	return deploymentFromRow(event)
}

func (s *Service) MustFetchDeploymentSnapshotAndSubscribe(predicate storage.DeploymentPredicate) ([]apigen.Deployment, chan apigen.Deployment, func()) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	snapshot := s.configSnapshotLocked(predicate)
	sub := s.deploymentSubs.Subscribe(deploymentFilter(predicate))
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

func (s *Service) configSnapshotLocked(predicate storage.DeploymentPredicate) []apigen.Deployment {
	out := make([]apigen.Deployment, 0, len(s.deploymentCache))
	for _, cfg := range s.deploymentCache {
		if predicate != nil && !predicate(*cfg) {
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
		item.Status = instancecache.WithRunningVersion(&item.Config, item.Status)
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

// instanceStateLocked assembles the state an instance runs against. Config is
// the deployment as of the event-log version the instance was scheduled
// against — a complete, immutable def snapshot, so no field needs merging from
// anywhere else. A deployment change never rewrites existing instance states;
// it rolls new instances instead.
func (s *Service) instanceStateLocked(inst *apigen.ScheduledInstance) *apigen.ScheduledInstanceState {
	return &apigen.ScheduledInstanceState{
		Instance: *inst,
		Config:   *s.configForVersionLocked(inst.DeploymentID, inst.DeploymentVersion),
	}
}

// configForVersionLocked resolves a pinned deployment version. Resolution
// cannot fail: instances are only created against versions read from the log,
// and the log is append-only — deletion is an event row, never a removal.
func (s *Service) configForVersionLocked(deploymentID, deploymentVersion int32) *apigen.Deployment {
	if cfg := s.deploymentCache[deploymentID]; cfg != nil && cfg.Version == deploymentVersion {
		return cfg
	}
	event, err := s.q.GetDeploymentEventByVersion(context.Background(), pq.GetDeploymentEventByVersionParams{
		DeploymentID: int64(deploymentID),
		Version:      int64(deploymentVersion),
	})
	if err != nil {
		panic(fmt.Sprintf("resolve deployment %d version %d: %v", deploymentID, deploymentVersion, err))
	}
	return deploymentFromRow(event)
}

func (s *Service) notifyDeploymentLocked(cfg apigen.Deployment) {
	s.deploymentSubs.Notify(cfg)
}

func deploymentFilter(predicate storage.DeploymentPredicate) func(apigen.Deployment, apigen.Deployment) bool {
	if predicate == nil {
		return nil
	}
	return func(_, cfg apigen.Deployment) bool {
		return predicate(cfg)
	}
}
