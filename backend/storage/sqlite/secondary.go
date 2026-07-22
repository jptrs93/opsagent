package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

// SecondaryStorage is the storage layer for secondary (primary) nodes.
type SecondaryStorage struct {
	*deploymentStore
}

func NewSecondaryStorage(dbPath string) *SecondaryStorage {
	db := mustInitSecondary(dbPath)
	return &SecondaryStorage{
		deploymentStore: newDeploymentStore(db),
	}
}

func (s *SecondaryStorage) Close() error {
	return s.db.Close()
}

func (s *SecondaryStorage) MustWriteDeploymentConfig(cfg *apigen.DeploymentConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	id := cfg.ID
	stored := *cfg
	if stored.NodeID <= 0 {
		panic("deployment node ID must be positive")
	}
	_, exists := s.configCache[id]
	retiredIDs := make([]int32, 0)
	if !stored.Deleted {
		for existingID, existing := range s.configCache {
			if existingID != id && !existing.Deleted && storage.DeploymentKeyMatches(*existing, stored.NodeID, stored.Identity) {
				retiredIDs = append(retiredIDs, existingID)
			}
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

	if !stored.Deleted {
		if err := q.RetireOtherActiveDeploymentConfigsWithKey(ctx, RetireOtherActiveDeploymentConfigsWithKeyParams{
			DeploymentID: int64(id),
			NodeID:       int64(stored.NodeID),
			SpaceID:      int64(stored.Identity.SpaceID),
			Name:         stored.Identity.Name,
		}); err != nil {
			panic(fmt.Sprintf("RetireOtherActiveDeploymentConfigsWithKey: %v", err))
		}
	}
	if err := q.UpsertDeploymentConfig(ctx, configProtoToUpsertParams(&stored)); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentConfig: %v", err))
	}
	if !exists {
		s.insertDefaultStatus(q, int64(id))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	for _, retiredID := range retiredIDs {
		retired := *s.configCache[retiredID]
		retired.Deleted = true
		retired.DesiredState.Running = false
		s.configCache[retiredID] = &retired
		s.notifyFromCache(retiredID)
	}
	s.configCache[id] = &stored
	s.notifyFromCache(id)
}

func (s *SecondaryStorage) FetchDeploymentStatusHistorySince(deploymentID int32, since time.Time) []*apigen.DeploymentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.q.ListDeploymentStatusHistorySince(context.Background(), ListDeploymentStatusHistorySinceParams{
		DeploymentID: int64(deploymentID),
		UpdatedAt:    clockToNanos(since),
	})
	if err != nil {
		panic(fmt.Sprintf("FetchDeploymentStatusHistorySince: %v", err))
	}
	out := make([]*apigen.DeploymentStatus, 0, len(rows))
	for _, r := range rows {
		out = append(out, statusRowToProto(r.DeploymentID, r))
	}
	return out
}
