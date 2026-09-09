package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

const defaultInstanceOrdinal int32 = 0
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
	s.store.RegisterUpdateTrigger(func(ctx context.Context, q *pq.Queries, update *state.Update) error {
		return s.reconcile(ctx, q, update, nil)
	})
	return s.store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		s.bootSeq, s.bootTime = seq-1, s.now()
		update := &state.Update{Seq: seq}
		err := s.reconcile(ctx, q, update, func() ([]int32, error) {
			rows, err := q.ListLatestDeploymentEvents(ctx)
			if err != nil {
				return nil, err
			}
			ids := make([]int32, 0, len(rows))
			for _, row := range rows {
				ids = append(ids, row.DeploymentID)
			}
			return ids, nil
		})
		return update, err
	})
}

func (s *Scheduler) Sweep(ctx context.Context) error {
	return s.store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		update := &state.Update{Seq: seq}
		err := s.reconcile(ctx, q, update, func() ([]int32, error) { return q.ListDrainingDeploymentIDs(ctx) })
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

func (s *Scheduler) reconcile(ctx context.Context, q *pq.Queries, update *state.Update, scope func() ([]int32, error)) error {
	affected := map[int32]bool{}
	for _, event := range update.DeploymentEvents {
		affected[event.DeploymentID] = true
	}
	for _, event := range update.ScheduledInstanceEvents {
		affected[event.Value.DeploymentID] = true
	}
	for _, status := range update.InstanceStatuses {
		affected[status.DeploymentID] = true
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
	ids := make([]int32, 0, len(affected))
	for id := range affected {
		if id != 0 {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	now := s.now()
	for _, id := range ids {
		cfg, instances, err := readSchedulingState(ctx, q, id)
		if err != nil {
			return err
		}
		limit := len(instances)*3 + 8
		for pass := 0; ; pass++ {
			tx := transaction{ctx: ctx, q: q, seq: update.Seq, update: update, now: now, scheduler: s}
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
	update    *state.Update
	now       time.Time
	scheduler *Scheduler
	changed   bool
}

func (tx *transaction) publish(event *apigen.ScheduledInstanceEvent) {
	tx.update.ScheduledInstanceEvents = append(tx.update.ScheduledInstanceEvents, event)
	tx.changed = true
}

func (tx *transaction) set(instance *schedulingInstance, target apigen.ScheduledInstanceTarget) error {
	if instance.Event.Value.State == target {
		return nil
	}
	event, err := tx.q.AppendScheduledInstanceEvent(tx.ctx, tx.seq, &instance.Event.Value, target, tx.now)
	if err != nil {
		return err
	}
	instance.Event = *event
	tx.publish(event)
	return nil
}

func (tx *transaction) step(cfg *apigen.DeploymentEvent, instances []schedulingInstance) error {
	running := cfg != nil && !cfg.Deleted() && cfg.WorkloadRunning()
	for i := range instances {
		entry := &instances[i]
		inst := entry.Event.Value
		obsolete := cfg != nil && inst.DeploymentVersion != cfg.Version && inst.InstanceOrdinal == defaultInstanceOrdinal
		retire := !running || obsolete && (cfg.EffectiveUpgradeStrategy() == apigen.ContainerUpgradeStrategy_RECREATE || inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY)
		if retire && inst.State.WantsRunning() {
			if err := tx.set(entry, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE); err != nil {
				return err
			}
		}
		if err := tx.finalizeStopped(entry); err != nil {
			return err
		}
	}
	if running && cfg.Value.NodeID > 0 {
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
			if inst.DeploymentVersion == cfg.Version && inst.State.WantsRunning() {
				exact = entry
			}
			if cfg.EffectiveUpgradeStrategy() == apigen.ContainerUpgradeStrategy_RECREATE && inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
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
			tx.publish(event)
		}
		if exact != nil {
			target := exact.Event.Value.State
			ready := exact.Status.Runner.Status == apigen.RunningStatus_RUNNING && exact.Status.Runner.DeploymentSpecVersion == exact.Config.SpecVersion
			if ready && (target == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY || target == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING) {
				if err := tx.promote(exact, instances); err != nil {
					return err
				}
			} else if target == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY {
				for i := range instances {
					old := &instances[i]
					if old.Event.Value.ID < exact.Event.Value.ID && old.Event.Value.InstanceOrdinal == exact.Event.Value.InstanceOrdinal && old.Event.Value.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING && terminalRunnerStatus(old.Status.Runner.Status) {
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

func terminalRunnerStatus(status apigen.RunningStatus) bool {
	return status == apigen.RunningStatus_STOPPED || status == apigen.RunningStatus_NO_DEPLOYMENT
}

func (tx *transaction) finalizeStopped(instance *schedulingInstance) error {
	if instance.Event.Value.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE && terminalRunnerStatus(instance.Status.Runner.Status) {
		return tx.set(instance, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	}
	return nil
}
