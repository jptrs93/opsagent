package state

import (
	"context"
	"log/slog"
	"slices"
	"sync"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

const SubscriberBuffer = 1_000

type updateHandler struct {
	notify func(context.Context, Update)
	drop   func()
}

func Subscribe[S, T any](s *Service, read func() S, project func(Update) (T, bool)) (S, chan T, func()) {
	ch := make(chan T, SubscriberBuffer)
	handler := &updateHandler{}
	handler.drop = sync.OnceFunc(func() {
		s.onUpdate = slices.DeleteFunc(s.onUpdate, func(h *updateHandler) bool { return h == handler })
		close(ch)
	})
	handler.notify = func(ctx context.Context, u Update) {
		item, send := project(u)
		if !send {
			return
		}
		select {
		case ch <- item:
		default:
			slog.ErrorContext(ctx, "state subscriber overflowed; closing its channel")
			handler.drop()
		}
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()
	snapshot := read()
	if !s.closed {
		s.onUpdate = append(s.onUpdate, handler)
	}
	return snapshot, ch, func() {
		s.Mu.Lock()
		defer s.Mu.Unlock()
		handler.drop()
	}
}

func (s *Service) SubscribeUpdates() (chan Update, func()) {
	_, ch, unsubscribe := Subscribe(s, func() struct{} { return struct{}{} }, func(u Update) (Update, bool) { return u, true })
	return ch, unsubscribe
}

func (s *Service) MustFetchScheduledSnapshotAndSubscribe(predicate storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan []apigen.ScheduledInstanceState, func()) {
	ctx := context.Background()
	q := s.q
	return Subscribe(s, func() []apigen.ScheduledInstanceState {
		return liveScheduledStates(q, predicate)
	}, func(u Update) ([]apigen.ScheduledInstanceState, bool) {
		var out []apigen.ScheduledInstanceState
		for _, id := range affectedInstanceIDs(u) {
			state, err := q.GetScheduledInstanceState(ctx, id)
			if err != nil {
				continue
			}
			if predicate == nil || predicate(*state) {
				out = append(out, *state)
			}
		}
		return out, len(out) > 0
	})
}

func (s *Service) FetchScheduledSnapshot(predicate storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState {
	return liveScheduledStates(s.q, predicate)
}

func (s *Service) notifyLocked(ctx context.Context, update Update) {
	for _, handler := range slices.Clone(s.onUpdate) {
		handler.notify(ctx, update)
	}
}

func affectedInstanceIDs(u Update) []int32 {
	seen := map[int32]bool{}
	var ids []int32
	add := func(id int32) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, event := range u.ScheduledInstanceEvents {
		add(event.ScheduledInstanceID)
	}
	for _, status := range u.InstanceStatuses {
		add(status.ScheduledInstanceID)
	}
	slices.Sort(ids)
	return ids
}

func liveScheduledStates(q *pq.Queries, predicate storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState {
	states, err := q.ListLiveScheduledInstanceStates(context.Background())
	if err != nil {
		panic(err)
	}
	out := make([]apigen.ScheduledInstanceState, 0, len(states))
	for _, state := range states {
		if predicate == nil || predicate(state) {
			out = append(out, state)
		}
	}
	return out
}
