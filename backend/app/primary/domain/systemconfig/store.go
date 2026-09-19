package systemconfig

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var ErrAssetMigrationInProgress = errors.New("asset migration is in progress")

func LatestRevision(q *pq.Queries) (pq.SystemConfigRevision, error) {
	return q.GetLatestConfig(context.Background())
}

func UnfinishedAssetMigration(q *pq.Queries) (pq.AssetMigration, bool) {
	migration, err := q.GetUnfinishedAssetMigration(context.Background())
	if errors.Is(err, sql.ErrNoRows) {
		return pq.AssetMigration{}, false
	}
	if err != nil {
		panic(err)
	}
	return migration, true
}

func AppendRevision(store *state.Service, blob []byte) (int64, error) {
	ctx := context.Background()
	var id int64
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		var err error
		id, err = q.InsertSystemConfigRevision(ctx, time.Now().UnixMilli(), blob)
		if err != nil {
			return nil, err
		}
		row, err := q.GetSystemConfigByID(ctx, id)
		if err != nil {
			return nil, err
		}
		return &apigen.CoreUpdate{SystemConfig: row}, nil
	})
	return id, err
}

func AppendRevisionWithAssetMigration(store *state.Service, blob []byte, createMigration bool, inlockValidate func(*pq.Queries) error) (int64, *pq.AssetMigration, error) {
	ctx := context.Background()
	now := time.Now().UnixMilli()
	var newConfigID int64
	var migration *pq.AssetMigration
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		if _, err := q.GetUnfinishedAssetMigration(ctx); err == nil {
			return nil, ErrAssetMigrationInProgress
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if inlockValidate != nil {
			if err := inlockValidate(q); err != nil {
				return nil, err
			}
		}
		var oldConfigID int64
		if createMigration {
			oldConfig, err := q.GetLatestConfig(ctx)
			if err != nil {
				return nil, err
			}
			oldConfigID = oldConfig.ID
		}
		var err error
		newConfigID, err = q.InsertSystemConfigRevision(ctx, now, blob)
		if err != nil {
			return nil, err
		}
		if createMigration {
			row, err := q.InsertAssetMigration(ctx, pq.InsertAssetMigrationParams{OldConfigVersionID: oldConfigID, NewConfigVersionID: newConfigID, CreatedAt: now})
			if err != nil {
				return nil, err
			}
			migration = &row
		}
		config, err := q.GetSystemConfigByID(ctx, newConfigID)
		if err != nil {
			return nil, err
		}
		return &apigen.CoreUpdate{SystemConfig: config}, nil
	}); err != nil {
		return 0, nil, err
	}
	return newConfigID, migration, nil
}
