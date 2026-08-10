package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

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
	migration, err := s.q.StartAssetMigration(context.Background(), pq.StartAssetMigrationParams{
		StartedAt:     now,
		LastAttemptAt: now,
		ID:            id,
	})
	if err != nil {
		panic(fmt.Sprintf("StartAssetMigration: %v", err))
	}
	return migration
}

func (s *Service) RecordAssetMigrationError(id int64, migrationErr error) AssetMigration {
	migration, err := s.q.RecordAssetMigrationError(context.Background(), pq.RecordAssetMigrationErrorParams{
		LastAttemptAt: time.Now().UnixMilli(),
		LastError:     migrationErr.Error(),
		ID:            id,
	})
	if err != nil {
		panic(fmt.Sprintf("RecordAssetMigrationError: %v", err))
	}
	return migration
}

func (s *Service) FinishAssetMigration(id int64) AssetMigration {
	migration, err := s.q.FinishAssetMigration(context.Background(), pq.FinishAssetMigrationParams{
		FinishedAt: time.Now().UnixMilli(),
		ID:         id,
	})
	if err != nil {
		panic(fmt.Sprintf("FinishAssetMigration: %v", err))
	}
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
func (s *Service) AppendOpenDeploySettingsWithAssetMigration(blob []byte, createMigration bool) (int64, *AssetMigration, error) {
	ctx := context.Background()
	var newConfigID int64
	var migration *AssetMigration
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		if _, err := q.GetUnfinishedAssetMigration(ctx); err == nil {
			return ErrAssetMigrationInProgress
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		oldConfig, err := q.GetLatestConfig(ctx)
		if err != nil {
			return err
		}
		newConfigID, err = q.InsertSystemConfigRevision(ctx, time.Now().UnixMilli(), blob)
		if err != nil {
			return err
		}
		if createMigration {
			row, insertErr := q.InsertAssetMigration(ctx, pq.InsertAssetMigrationParams{
				OldConfigVersionID: oldConfig.ID,
				NewConfigVersionID: newConfigID,
				CreatedAt:          time.Now().UnixMilli(),
			})
			if insertErr != nil {
				return insertErr
			}
			migration = &row
		}
		return nil
	}); err != nil {
		return 0, nil, err
	}
	return newConfigID, migration, nil
}
