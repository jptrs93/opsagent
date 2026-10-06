package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

const defaultInstanceOrdinal uint32 = 0
const drainTimeout = 30 * time.Second
const drainPollInterval = 2 * time.Second

type routeBarrier interface {
	DecisionInForce(seq int64) bool
	AckUpdates() <-chan struct{}
}

type Scheduler struct {
	store    *state.Service
	barrier  routeBarrier
	now      func() time.Time
	bootSeq  int64
	bootTime time.Time
}

func New(store *state.Service, barrier routeBarrier) *Scheduler {
	return &Scheduler{store: store, barrier: barrier, now: time.Now}
}

func (s *Scheduler) Start(ctx context.Context) error {
	s.store.RegisterUpdateTrigger(func(ctx context.Context, q *pq.Queries, update *state.WriteUpdate) error {
		return s.reconcile(ctx, q, update, nil)
	})
	return s.store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		s.bootSeq, s.bootTime = seq-1, s.now()
		update := &state.WriteUpdate{Seq: seq}
		err := s.reconcile(ctx, q, update, func() ([]uint64, error) {
			rows, err := q.ListActiveDeployments(ctx)
			if err != nil {
				return nil, err
			}
			instances, err := q.ListNonFinalScheduledInstances(ctx)
			if err != nil {
				return nil, err
			}
			seen := map[uint64]bool{}
			ids := make([]uint64, 0, len(rows))
			for _, row := range rows {
				seen[row.Deployment.ID] = true
				ids = append(ids, row.Deployment.ID)
			}
			for _, inst := range instances {
				id := inst.Value.Deployment.DeploymentID
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
			return ids, nil
		})
		return update, err
	})
}

func (s *Scheduler) Sweep(ctx context.Context) error {
	return s.store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		update := &state.WriteUpdate{Seq: seq}
		err := s.reconcile(ctx, q, update, func() ([]uint64, error) { return q.ListDrainingDeploymentIDs(ctx) })
		return update, err
	})
}

func (s *Scheduler) Run(ctx context.Context) {
	var acks <-chan struct{}
	if s.barrier != nil {
		acks = s.barrier.AckUpdates()
	}
	poll := time.NewTicker(drainPollInterval)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-acks:
		case <-poll.C:
		}
		if err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "scheduler reconciliation failed", "err", err)
		}
	}
}

func (s *Scheduler) reconcile(ctx context.Context, q *pq.Queries, update *state.WriteUpdate, scope func() ([]uint64, error)) error {
	affected := map[uint64]bool{}
	for _, m := range update.Mutations {
		switch m.Type() {
		case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:
			affected[m.EntityID()] = true
		case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:
			if e := m.Entity(); e != nil && e.Value.ScheduledInstance != nil {
				affected[e.Value.ScheduledInstance.Deployment.DeploymentID] = true
			}
		case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS:
			inst, err := q.GetScheduledInstance(ctx, m.EntityID())
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if inst != nil {
				affected[inst.Value.Deployment.DeploymentID] = true
			}
		}
	}
	if scope != nil {
		ids, err := scope()
		if err != nil {
			return err
		}
		for _, id := range ids {
			affected[id] = true
		}
	}
	ids := make([]uint64, 0, len(affected))
	for id := range affected {
		if id != 0 {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	if len(ids) == 0 {
		return nil
	}
	evicted, err := evictedNodeIDs(ctx, q)
	if err != nil {
		return err
	}
	// Rows this trigger appends share the commit's time with the rows the
	// mutate function wrote.
	now := s.now()
	if len(update.Mutations) > 0 && update.Time != 0 {
		now = time.UnixMilli(update.Time)
	}
	for _, id := range ids {
		cfg, instances, err := readSchedulingState(ctx, q, id)
		if err != nil {
			return err
		}
		limit := len(instances)*3 + 8
		for pass := 0; ; pass++ {
			tx := transaction{ctx: ctx, q: q, seq: update.Seq, update: update, now: now, scheduler: s, evicted: evicted}
			if err := tx.step(cfg, instances); err != nil {
				return err
			}
			if !tx.changed {
				break
			}
			if pass >= limit {
				return fmt.Errorf("scheduler did not stabilize deployment %d", id)
			}
			cfg, instances, err = readSchedulingState(ctx, q, id)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

type transaction struct {
	ctx       context.Context
	q         *pq.Queries
	seq       int64
	update    *state.WriteUpdate
	now       time.Time
	scheduler *Scheduler
	evicted   map[uint64]bool
	changed   bool
}

func evictedNodeIDs(ctx context.Context, q *pq.Queries) (map[uint64]bool, error) {
	rows, err := q.ListNodeRows(ctx, []int64{int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_EVICTED)})
	if err != nil {
		return nil, err
	}
	out := make(map[uint64]bool, len(rows))
	for _, row := range rows {
		out[row.Event.NodeID] = true
	}
	return out, nil
}

func (tx *transaction) publish(verb apigen.AuthzVerb, event *pq.ScheduledInstanceEvent) error {
	tx.changed = true
	return tx.q.Apply(tx.ctx, tx.update, pq.ScheduledInstanceMutation(verb, event))
}

func (tx *transaction) set(instance *schedulingInstance, target apigen.ScheduledInstanceTarget) error {
	if instance.Event.Value.State == target {
		return nil
	}
	event := pq.ScheduledInstanceTransition(tx.seq, &instance.Event, target, tx.now)
	instance.Event = *event
	return tx.publish(apigen.AuthzVerb_AUTHZ_VERB_UPDATE, event)
}

func (tx *transaction) step(cfg *apigen.DeploymentRecord, instances []schedulingInstance) error {
	running := cfg != nil && !cfg.Deleted() && cfg.WorkloadRunning()
	for i := range instances {
		entry := &instances[i]
		inst := entry.Event.Value
		obsolete := cfg != nil && inst.Deployment.Version != cfg.Meta.Version && inst.InstanceOrdinal == defaultInstanceOrdinal
		retire := !running || obsolete && (cfg.EffectiveUpgradeStrategy() == apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE || inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY)
		if retire && inst.State.WantsRunning() {
			if err := tx.set(entry, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE); err != nil {
				return err
			}
		}
		if err := tx.finalizeStopped(entry); err != nil {
			return err
		}
	}
	if running && cfg.Deployment.PlacementNodeID() > 0 && !tx.evicted[cfg.Deployment.PlacementNodeID()] {
		var exact *schedulingInstance
		serving, blocked := false, false
		for i := range instances {
			entry := &instances[i]
			inst := entry.Event.Value
			if inst.InstanceOrdinal != defaultInstanceOrdinal {
				continue
			}
			if inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
				serving = true
			}
			if inst.Deployment.Version == cfg.Meta.Version && inst.State.WantsRunning() {
				exact = entry
			}
			if cfg.EffectiveUpgradeStrategy() == apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE && inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
				blocked = true
			}
		}
		if exact == nil && !blocked {
			target := apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING
			if serving {
				target = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY
			}
			event, err := newInstance(tx.ctx, tx.q, tx.seq, cfg, defaultInstanceOrdinal, target, tx.now)
			if err != nil {
				return err
			}
			if err := tx.publish(apigen.AuthzVerb_AUTHZ_VERB_CREATE, event); err != nil {
				return err
			}
		}
		if exact != nil {
			target := exact.Event.Value.State
			ready := exact.Status.Runner.Present && exact.Status.Runner.Value.Status == apigen.RunningStatus_RUNNING_STATUS_RUNNING && exact.Status.Runner.Value.DeploymentSpecVersion == exact.Config.Meta.SpecVersion
			if ready && (target == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY || target == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING) {
				if err := tx.promote(exact, instances); err != nil {
					return err
				}
			} else if target == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY {
				for i := range instances {
					old := &instances[i]
					if old.Event.Value.ID < exact.Event.Value.ID && old.Event.Value.InstanceOrdinal == exact.Event.Value.InstanceOrdinal && old.Event.Value.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING && terminalRunnerStatus(old.Status.Runner) {
						if err := tx.promote(exact, instances); err != nil {
							return err
						}
						break
					}
				}
			}
		}
	}
	for i := range instances {
		entry := &instances[i]
		if entry.Event.Value.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
			continue
		}
		if entry.Event.Seq >= tx.seq {
			continue
		}
		adopted := entry.Event.Seq <= tx.scheduler.bootSeq
		deadline := time.UnixMilli(entry.Event.EventTime).Add(drainTimeout)
		if adopted {
			deadline = tx.scheduler.bootTime.Add(drainTimeout)
		}
		applied := tx.scheduler.barrier == nil || !adopted && tx.scheduler.barrier.DecisionInForce(entry.Event.Seq)
		if !applied && tx.now.Before(deadline) {
			continue
		}
		if err := tx.set(entry, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE); err != nil {
			return err
		}
		if err := tx.finalizeStopped(entry); err != nil {
			return err
		}
	}
	return nil
}

func (tx *transaction) promote(current *schedulingInstance, instances []schedulingInstance) error {
	for i := range instances {
		old := &instances[i]
		inst := old.Event.Value
		if inst.ID >= current.Event.Value.ID || inst.InstanceOrdinal != current.Event.Value.InstanceOrdinal || !inst.State.WantsRunning() || inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
			continue
		}
		if err := tx.set(old, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING); err != nil {
			return err
		}
	}
	return tx.set(current, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
}

func terminalRunnerStatus(runner apigen.Maybe[apigen.RunnerStatus]) bool {
	if !runner.Present {
		return false
	}
	status := runner.Value.Status
	return status == apigen.RunningStatus_RUNNING_STATUS_STOPPED || status == apigen.RunningStatus_RUNNING_STATUS_NO_DEPLOYMENT
}

func (tx *transaction) finalizeStopped(instance *schedulingInstance) error {
	if instance.Event.Value.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE && terminalRunnerStatus(instance.Status.Runner) {
		return tx.set(instance, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	}
	return nil
}
