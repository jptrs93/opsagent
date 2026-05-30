package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// SecondaryStorage is the storage layer for secondary (slave) nodes.
// It uses the primary's integer ID directly and fully owns deployment status.
// It shares the primary's schema (schema.sql) and generated queries; the
// deployment_config_history, users, and public_keys tables exist but are never
// written to on a secondary.
type SecondaryStorage struct {
	*deploymentStore
}

func NewSecondaryStorageAdapter(dbPath string) *SecondaryStorage {
	db := mustInitSecondary(dbPath)
	return &SecondaryStorage{
		deploymentStore: newDeploymentStore(db),
	}
}

// MustWriteDeploymentConfig persists a full DeploymentConfig received from the primary.
func (s *SecondaryStorage) MustWriteDeploymentConfig(ctx context.Context, cfg *apigen.DeploymentConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := cfg.ID
	_, exists := s.configCache[id]

	if err := s.q.UpsertDeploymentConfig(ctx, configProtoToUpsertParams(cfg)); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentConfig: %v", err))
	}

	s.configCache[id] = cfg

	if !exists {
		s.insertDefaultStatus(ctx, s.q, int64(id))
	}

	s.notifyFromCache(id)
}

// FetchDeploymentStatusHistorySince returns history rows with
// updated_at > since, in ascending order. Used on reconnect to
// replay any history the primary is missing.
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
