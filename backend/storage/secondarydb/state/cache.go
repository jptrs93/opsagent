package state

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jptrs93/goutil/logu"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

func (s *Service) FetchScheduledSnapshot(predicate storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked(predicate)
}

const subscriberBuffer = 1_000

type subscriber struct {
	predicate storage.ScheduledInstancePredicate
	ch        chan []apigen.ScheduledInstanceState
}

func (s *Service) MustFetchScheduledSnapshotAndSubscribe(predicate storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan []apigen.ScheduledInstanceState, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := s.snapshotLocked(predicate)
	sub := &subscriber{predicate: predicate, ch: make(chan []apigen.ScheduledInstanceState, subscriberBuffer)}
	if !s.closed {
		s.subscribers = append(s.subscribers, sub)
	}
	return snapshot, sub.ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, current := range s.subscribers {
			if current == sub {
				s.subscribers = append(s.subscribers[:i], s.subscribers[i+1:]...)
				close(sub.ch)
				return
			}
		}
	}
}

func (s *Service) MustWriteScheduledInstanceStatus(instanceID int32, f func(*apigen.ScheduledInstanceStatus) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := logu.AddTag(context.Background(), "Store")
	ctx = logu.AddKV(ctx, "scheduled_instance", instanceID)

	state := s.scheduled[instanceID]
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

	s.persistStatus(ctx, &current)

	state.Status = current
	slog.InfoContext(ctx, fmt.Sprintf("scheduled instance status published updatedAt=%v preparerStatus=%v runnerStatus=%v",
		current.UpdatedAt, current.Preparer.Rollup(), current.Runner.Status))
	s.notifyInstanceLocked(instanceID)
}

// FetchScheduledInstance returns the assignment alone. Callers reconciling a
// decision made earlier use it to confirm the placement still exists and is
// still in the state they left it in.
func (s *Service) FetchScheduledInstance(instanceID int32) *apigen.ScheduledInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.scheduled[instanceID]
	if state == nil {
		return nil
	}
	cp := state.Instance
	return &cp
}

func (s *Service) snapshotLocked(predicate storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState {
	out := make([]apigen.ScheduledInstanceState, 0, len(s.scheduled))
	for id, state := range s.scheduled {
		if state.Instance.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
			continue
		}
		item := s.stateLocked(id)
		if predicate != nil && !predicate(item) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (s *Service) stateLocked(id int32) apigen.ScheduledInstanceState {
	state := s.scheduled[id]
	if state == nil {
		return apigen.ScheduledInstanceState{}
	}
	out := *state
	if out.Config.DeploymentID != 0 {
		out.Status = apigen.WithRunningVersion(&out.Config, out.Status)
	}
	return out
}

func (s *Service) notifyInstanceLocked(id int32) {
	state := s.stateLocked(id)
	if state.Instance.ID == 0 {
		return
	}
	name := ""
	if state.Config.Value.Name != "" {
		name = fmt.Sprintf("%d:%d:%s", state.Instance.SpaceID, state.Instance.NodeID, state.Config.Value.Name)
	}
	ctx := logu.AddTag(context.Background(), "Store")
	slog.InfoContext(ctx, fmt.Sprintf("store: notify scheduled instance name=%s configVersion=%d targetState=%v hasPreparer=%t hasRunner=%t",
		name, state.Instance.DeploymentSpecVersion, state.Instance.State,
		!state.Status.Preparer.IsZero(), !state.Status.Runner.IsZero()),
		"scheduled_instance", id,
		"dep", state.Instance.DeploymentID,
	)
	kept := s.subscribers[:0]
	for _, sub := range s.subscribers {
		if sub.predicate != nil && !sub.predicate(state) {
			kept = append(kept, sub)
			continue
		}
		select {
		case sub.ch <- []apigen.ScheduledInstanceState{state}:
			kept = append(kept, sub)
		default:
			slog.ErrorContext(ctx, "scheduled instance subscriber overflowed; closing its channel")
			close(sub.ch)
		}
	}
	for i := len(kept); i < len(s.subscribers); i++ {
		s.subscribers[i] = nil
	}
	s.subscribers = kept
}
