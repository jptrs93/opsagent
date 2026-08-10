package secondarydb

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
)

//go:embed sql/schema.sql
var schemaFiles embed.FS

//go:embed sql/migrations.sql
var migrations string

// Runtime input kinds as persisted in local_runtime_inputs.kind. The numeric
// values are on disk, so they are fixed.
const (
	LocalRuntimeInputKindSecret int64 = 1
	LocalRuntimeInputKindConfig int64 = 2
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

// The local_runtime_inputs methods are a DB passthrough: the ciphertext arrives
// already sealed. Encryption lives in lib/localinputs, which owns the machine
// key, so the storage layer never sees a plaintext runtime input.

func (s *Storage) ListLocalRuntimeInputs() []LocalRuntimeInput {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	rows, err := s.q.ListLocalRuntimeInputs(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListLocalRuntimeInputs: %v", err))
	}
	return rows
}

func (s *Storage) UpsertLocalRuntimeInput(row LocalRuntimeInput) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := s.q.UpsertLocalRuntimeInput(context.Background(), UpsertLocalRuntimeInputParams{
		Kind:       row.Kind,
		RefID:      row.RefID,
		Ciphertext: row.Ciphertext,
		Nonce:      row.Nonce,
		FetchedAt:  row.FetchedAt,
	}); err != nil {
		panic(fmt.Sprintf("UpsertLocalRuntimeInput kind=%d ref=%d: %v", row.Kind, row.RefID, err))
	}
}

func (s *Storage) DeleteLocalRuntimeInput(kind, refID int64) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := s.q.DeleteLocalRuntimeInput(context.Background(), DeleteLocalRuntimeInputParams{
		Kind:  kind,
		RefID: refID,
	}); err != nil {
		panic(fmt.Sprintf("DeleteLocalRuntimeInput kind=%d ref=%d: %v", kind, refID, err))
	}
}
