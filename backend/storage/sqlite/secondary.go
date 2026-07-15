package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
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

func (s *SecondaryStorage) MustWriteDeploymentConfig(cfg *apigen.DeploymentConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	id := cfg.ID
	stored := *cfg
	stored.NodeID = normalizeDeploymentNodeID(stored.NodeID)
	_, exists := s.configCache[id]

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

	if err := q.UpsertDeploymentConfig(ctx, configProtoToUpsertParams(&stored)); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentConfig: %v", err))
	}
	if !exists {
		s.insertDefaultStatus(q, int64(id))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
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
