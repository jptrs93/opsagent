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
	_, exists := s.configCache[id]

	tx, beginErr := s.db.BeginTx(ctx, nil)
	if beginErr != nil {
		panic(fmt.Sprintf("begin tx: %v", beginErr))
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

	if upsertErr := q.UpsertDeploymentConfig(ctx, configProtoToUpsertParams(cfg)); upsertErr != nil {
		panic(fmt.Sprintf("UpsertDeploymentConfig: %v", upsertErr))
	}
	if !exists {
		s.insertDefaultStatus(q, int64(id))
	}
	if commitErr := tx.Commit(); commitErr != nil {
		panic(fmt.Sprintf("commit: %v", commitErr))
	}

	s.configCache[id] = cfg
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
