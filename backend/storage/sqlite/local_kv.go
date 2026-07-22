package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	// LocalKVClusterNetwork caches the legacy cluster network parameters on a
	// worker during rolling upgrades.
	LocalKVClusterNetwork = "cluster_network"
	// LocalKVPrimaryClusterNetMap stores the primary's latest target-neutral
	// publication, including generation and sequence.
	LocalKVPrimaryClusterNetMap = "primary_cluster_net_map"
	// LocalKVWorkerClusterNetMap stores the worker's last accepted full map.
	LocalKVWorkerClusterNetMap = "worker_cluster_net_map"
	// LocalKVWorkerRetiredNetMapGenerations prevents returning to a superseded
	// control-plane history after accepting a new generation.
	LocalKVWorkerRetiredNetMapGenerations = "worker_retired_net_map_generations"
)

func (s *deploymentStore) MustSetLocalKV(key string, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.q.UpsertLocalKV(context.Background(), UpsertLocalKVParams{Key: key, Value: value}); err != nil {
		panic(fmt.Sprintf("UpsertLocalKV %s: %v", key, err))
	}
}

func (s *deploymentStore) FetchLocalKV(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := s.q.GetLocalKV(context.Background(), key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetLocalKV %s: %v", key, err))
	}
	return value, true
}

func (s *deploymentStore) DeleteLocalKV(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(context.Background(), `DELETE FROM local_kv WHERE key = ?`, key)
	return err
}

// MustSetLocalKVs atomically updates related machine-local state.
func (s *deploymentStore) MustSetLocalKVs(values map[string][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
