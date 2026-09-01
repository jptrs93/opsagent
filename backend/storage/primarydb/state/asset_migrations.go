package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

var ErrAssetMigrationInProgress = errors.New("asset migration is in progress")

func (s *Service) GetUnfinishedAssetMigration() (AssetMigration, bool) {
	migration, err := s.q.GetUnfinishedAssetMigration(context.Background())
	if errors.Is(err, sql.ErrNoRows) {
		return AssetMigration{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetUnfinishedAssetMigration: %v", err))
	}
	return migration, true
}

func (s *Service) StartAssetMigration(id int64) AssetMigration {
	now := time.Now().UnixMilli()
	migration := erru.Must(s.q.StartAssetMigration(context.Background(), pq.StartAssetMigrationParams{
		StartedAt:     now,
		LastAttemptAt: now,
		ID:            id,
	}))
	return migration
}

func (s *Service) RecordAssetMigrationError(id int64, migrationErr error) AssetMigration {
	migration := erru.Must(s.q.RecordAssetMigrationError(context.Background(), pq.RecordAssetMigrationErrorParams{
		LastAttemptAt: time.Now().UnixMilli(),
		LastError:     migrationErr.Error(),
		ID:            id,
	}))
	return migration
}

func (s *Service) FinishAssetMigration(id int64) AssetMigration {
	migration := erru.Must(s.q.FinishAssetMigration(context.Background(), pq.FinishAssetMigrationParams{
		FinishedAt: time.Now().UnixMilli(),
		ID:         id,
	}))
	return migration
}

func (s *Service) FetchOpenDeployConfigByID(id int64) (SystemConfigRevision, error) {
	config, err := s.q.GetConfigByID(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return SystemConfigRevision{}, ErrNotFound
	}
	return config, err
}

// AppendOpenDeploySettingsWithAssetMigration commits the new configuration and
// its migration intent together so startup can always recover the transition.
func (s *Service) AppendOpenDeploySettingsWithAssetMigrationLocked(blob []byte, createMigration bool) (int64, *AssetMigration, error) {
	ctx := context.Background()

	if _, err := s.q.GetUnfinishedAssetMigration(ctx); err == nil {
		return 0, nil, ErrAssetMigrationInProgress
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, nil, err
	}
	now := time.Now().UnixMilli()
	if !createMigration {
		newConfigID, err := s.q.InsertSystemConfigRevision(ctx, now, blob)
		return newConfigID, nil, err
	}
	oldConfig, err := s.q.GetLatestConfig(ctx)
	if err != nil {
		return 0, nil, err
	}
	var newConfigID int64
	var migration *AssetMigration
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		var err error
		newConfigID, err = q.InsertSystemConfigRevision(ctx, now, blob)
		if err != nil {
			return err
		}
		row, err := q.InsertAssetMigration(ctx, pq.InsertAssetMigrationParams{
			OldConfigVersionID: oldConfig.ID,
			NewConfigVersionID: newConfigID,
			CreatedAt:          now,
		})
		if err != nil {
			return err
		}
		migration = &row
		return nil
	}); err != nil {
		return 0, nil, err
	}
	return newConfigID, migration, nil
}
