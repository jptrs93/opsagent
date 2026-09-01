package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/sq"
)

// Runtime input kinds as persisted in local_runtime_inputs.kind. The numeric
// values are on disk, so they are fixed.
const (
	LocalRuntimeInputKindSecret    int64 = 1
	LocalRuntimeInputKindConfig    int64 = 2
	LocalRuntimeInputKindIssuedTLS int64 = 3
)

// LocalRuntimeInput is the sealed runtime-input row; see the passthrough note
// below.
type LocalRuntimeInput = sq.LocalRuntimeInput

func (s *Service) MustSetLocalKV(key string, value []byte) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := s.q.UpsertLocalKV(context.Background(), sq.UpsertLocalKVParams{Key: key, Value: value}); err != nil {
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
	if err := s.q.Tx(context.Background(), func(q *sq.Queries) error {
		for key, value := range values {
			if err := q.UpsertLocalKV(context.Background(), sq.UpsertLocalKVParams{Key: key, Value: value}); err != nil {
				return fmt.Errorf("key %s: %w", key, err)
			}
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("local KV transaction: %v", err))
	}
}

// The local_runtime_inputs methods are a DB passthrough: the ciphertext arrives
// already sealed. Encryption lives in lib/localinputs, which owns the machine
// key, so the storage layer never sees a plaintext runtime input.

func (s *Service) ListLocalRuntimeInputs() []LocalRuntimeInput {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	rows := erru.Must(s.q.ListLocalRuntimeInputs(context.Background()))
	return rows
}

func (s *Service) UpsertLocalRuntimeInput(row LocalRuntimeInput) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := s.q.UpsertLocalRuntimeInput(context.Background(), sq.UpsertLocalRuntimeInputParams{
		Kind:       row.Kind,
		RefID:      row.RefID,
		Ciphertext: row.Ciphertext,
		Nonce:      row.Nonce,
		FetchedAt:  row.FetchedAt,
	}); err != nil {
		panic(fmt.Sprintf("UpsertLocalRuntimeInput kind=%d ref=%d: %v", row.Kind, row.RefID, err))
	}
}

func (s *Service) DeleteLocalRuntimeInput(kind, refID int64) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := s.q.DeleteLocalRuntimeInput(context.Background(), sq.DeleteLocalRuntimeInputParams{
		Kind:  kind,
		RefID: refID,
	}); err != nil {
		panic(fmt.Sprintf("DeleteLocalRuntimeInput kind=%d ref=%d: %v", kind, refID, err))
	}
}
