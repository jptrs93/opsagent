package primarydb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (s *Storage) MustSetLocalKV(key string, value []byte) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := s.q.UpsertLocalKV(context.Background(), UpsertLocalKVParams{Key: key, Value: value}); err != nil {
		panic(fmt.Sprintf("UpsertLocalKV %s: %v", key, err))
	}
}

func (s *Storage) FetchLocalKV(key string) ([]byte, bool) {
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

func (s *Storage) DeleteLocalKV(key string) error {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	_, err := s.db.ExecContext(context.Background(), `DELETE FROM local_kv WHERE key = ?`, key)
	return err
}

// MustSetLocalKVs atomically updates related machine-local state.
func (s *Storage) MustSetLocalKVs(values map[string][]byte) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		panic(fmt.Sprintf("begin local KV transaction: %v", err))
	}
	defer tx.Rollback()
	for key, value := range values {
		if _, err := tx.ExecContext(context.Background(), `
			INSERT INTO local_kv (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			panic(fmt.Sprintf("UpsertLocalKV %s: %v", key, err))
		}
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit local KV transaction: %v", err))
	}
}
