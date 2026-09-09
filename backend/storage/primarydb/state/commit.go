package state

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

type Update = apigen.CoreUpdate

type UpdateTrigger func(ctx context.Context, q *pq.Queries, update *Update) error

func (s *Service) RegisterUpdateTrigger(trigger UpdateTrigger) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.updateTriggers = append(s.updateTriggers, trigger)
}

func (s *Service) Commit(ctx context.Context, inlockValidate pq.Validator, mutate func(*pq.Queries, int64) (*Update, error)) error {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	var update *Update
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		previous, err := q.GetGlobalSeq(ctx)
		if err != nil {
			return err
		}
		seq := previous + 1
		if inlockValidate != nil {
			if err := inlockValidate(q); err != nil {
				return err
			}
		}
		update, err = mutate(q, seq)
		if err != nil {
			return err
		}
		if update == nil {
			update = &Update{}
		}
		update.Seq = seq
		for _, trigger := range s.updateTriggers {
			if err := trigger(ctx, q, update); err != nil {
				return err
			}
		}
		if update.IsEmpty() {
			update = nil
			return nil
		}
		return q.SetGlobalSeq(ctx, seq)
	})
	if err != nil {
		return err
	}
	if update != nil {
		s.notifyLocked(ctx, *update)
	}
	return nil
}
