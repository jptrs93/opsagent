package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// LocalKVClusterNetwork caches the cluster network parameters
// (apigen.ClusterNetworkInfo) on a worker so the network can be programmed on
// boot before the primary is reachable.
const LocalKVClusterNetwork = "cluster_network"

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
