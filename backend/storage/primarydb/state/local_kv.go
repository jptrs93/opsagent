package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func (s *Service) MustSetLocalKV(key string, value []byte) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := s.q.UpsertLocalKV(context.Background(), pq.UpsertLocalKVParams{Key: key, Value: value}); err != nil {
		panic(fmt.Sprintf("UpsertLocalKV %s: %v", key, err))
	}
}

func (s *Service) FetchLocalKV(key string) ([]byte, bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	value, err := s.q.GetLocalKV(context.Background(), key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetLocalKV %s: %v", key, err))
	}
	return value, true
}

func (s *Service) DeleteLocalKV(key string) error {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.q.DeleteLocalKV(context.Background(), key)
}

// MustSetLocalKVs atomically updates related machine-local state.
func (s *Service) MustSetLocalKVs(values map[string][]byte) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := s.q.Tx(context.Background(), func(q *pq.Queries) error {
		for key, value := range values {
			if err := q.UpsertLocalKV(context.Background(), pq.UpsertLocalKVParams{Key: key, Value: value}); err != nil {
				panic(fmt.Sprintf("UpsertLocalKV %s: %v", key, err))
			}
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("local KV transaction: %v", err))
	}
}
