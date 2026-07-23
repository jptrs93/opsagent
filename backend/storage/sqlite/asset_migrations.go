package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrAssetMigrationInProgress = errors.New("asset migration is in progress")

func (s *PrimaryStorage) GetUnfinishedAssetMigration() (AssetMigration, bool) {
	migration, err := s.q.GetUnfinishedAssetMigration(context.Background())
	if errors.Is(err, sql.ErrNoRows) {
		return AssetMigration{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetUnfinishedAssetMigration: %v", err))
	}
	return migration, true
}

func (s *PrimaryStorage) StartAssetMigration(id int64) AssetMigration {
	now := time.Now().UnixMilli()
	migration, err := s.q.StartAssetMigration(context.Background(), StartAssetMigrationParams{
		StartedAt:     now,
		LastAttemptAt: now,
		ID:            id,
	})
	if err != nil {
		panic(fmt.Sprintf("StartAssetMigration: %v", err))
	}
	return migration
}

func (s *PrimaryStorage) RecordAssetMigrationError(id int64, migrationErr error) AssetMigration {
	migration, err := s.q.RecordAssetMigrationError(context.Background(), RecordAssetMigrationErrorParams{
		LastAttemptAt: time.Now().UnixMilli(),
		LastError:     migrationErr.Error(),
		ID:            id,
	})
	if err != nil {
		panic(fmt.Sprintf("RecordAssetMigrationError: %v", err))
	}
	return migration
}

func (s *PrimaryStorage) FinishAssetMigration(id int64) AssetMigration {
	migration, err := s.q.FinishAssetMigration(context.Background(), FinishAssetMigrationParams{
		FinishedAt: time.Now().UnixMilli(),
		ID:         id,
	})
	if err != nil {
		panic(fmt.Sprintf("FinishAssetMigration: %v", err))
	}
	return migration
}

func (s *PrimaryStorage) FetchOpenDeployConfigByID(id int64) (SystemConfigRevision, error) {
	config, err := s.q.GetConfigByID(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return SystemConfigRevision{}, ErrNotFound
	}
	return config, err
}

// AppendOpenDeploySettingsWithAssetMigration commits the new configuration and
// its migration intent together so startup can always recover the transition.
func (s *PrimaryStorage) AppendOpenDeploySettingsWithAssetMigration(blob []byte, createMigration bool) (int64, *AssetMigration, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)
	if _, err := q.GetUnfinishedAssetMigration(ctx); err == nil {
		return 0, nil, ErrAssetMigrationInProgress
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, nil, err
	}
	oldConfig, err := q.GetLatestConfig(ctx)
	if err != nil {
		return 0, nil, err
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO system_config_revisions (updated_at, config_blob) VALUES (?, ?)
`, time.Now().UnixMilli(), blob)
	if err != nil {
		return 0, nil, err
	}
	newConfigID, err := result.LastInsertId()
	if err != nil {
		return 0, nil, err
	}
	var migration *AssetMigration
	if createMigration {
		row, insertErr := q.InsertAssetMigration(ctx, InsertAssetMigrationParams{
			OldConfigVersionID: oldConfig.ID,
			NewConfigVersionID: newConfigID,
			CreatedAt:          time.Now().UnixMilli(),
		})
		if insertErr != nil {
			return 0, nil, insertErr
		}
		migration = &row
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return newConfigID, migration, nil
}
