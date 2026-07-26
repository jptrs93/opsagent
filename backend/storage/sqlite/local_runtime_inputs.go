package sqlite

import (
	"context"
	"fmt"
)

// Runtime input kinds as persisted in local_runtime_inputs.kind. The numeric
// values are on disk, so they are fixed.
const (
	LocalRuntimeInputKindSecret int64 = 1
	LocalRuntimeInputKindConfig int64 = 2
)

// The local_runtime_inputs methods are a DB passthrough: the ciphertext arrives
// already sealed. Encryption lives in lib/localinputs, which owns the machine
// key, so the storage layer never sees a plaintext runtime input.

func (s *deploymentStore) ListLocalRuntimeInputs() []LocalRuntimeInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.q.ListLocalRuntimeInputs(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListLocalRuntimeInputs: %v", err))
	}
	return rows
}

func (s *deploymentStore) UpsertLocalRuntimeInput(row LocalRuntimeInput) {
	s.mu.Lock()
	defer s.mu.Unlock()
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

func (s *deploymentStore) DeleteLocalRuntimeInput(kind, refID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.q.DeleteLocalRuntimeInput(context.Background(), DeleteLocalRuntimeInputParams{
		Kind:  kind,
		RefID: refID,
	}); err != nil {
		panic(fmt.Sprintf("DeleteLocalRuntimeInput kind=%d ref=%d: %v", kind, refID, err))
	}
}
