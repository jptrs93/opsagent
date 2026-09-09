package assets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/ptru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

var (
	ErrDirectoryNotFound    = errors.New("asset directory not found")
	ErrDirectoryNotEmpty    = errors.New("asset directory is not empty")
	ErrDirectoryCycle       = errors.New("asset directory cannot be moved inside itself")
	ErrSpaceMoveUnsupported = errors.New("moving between spaces is not supported")
)

func getAssetDirectory(ctx context.Context, q *pq.Queries, id int64) (apigen.AssetDirectory, error) {
	d, err := q.GetAssetDirectoryByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.AssetDirectory{}, ErrDirectoryNotFound
	}
	return d, err
}

func CreateDirectory(store *state.Service, spaceID, parentID int32, key string, author int32) (apigen.AssetDirectory, error) {
	if !ValidAssetKey(key) {
		return apigen.AssetDirectory{}, ErrAssetKeyInvalid
	}
	ctx := context.Background()
	space := int64(nodes.NormalizedUserSpaceID(spaceID))
	parent := int64(parentID)
	var d apigen.AssetDirectory
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		if parent != 0 {
			p, err := getAssetDirectory(ctx, q, parent)
			if err != nil {
				return nil, err
			}
			if int64(p.SpaceID) != space {
				return nil, ErrDirectoryNotFound
			}
		}
		if assetSiblingKeyTaken(ctx, q, space, parent, key, 0, 0) {
			return nil, ErrAssetAlreadyExists
		}
		var err error
		d, err = q.InsertAssetDirectory(ctx, pq.InsertAssetDirectoryParams{
			SpaceID:   space,
			Key:       key,
			ParentID:  parent,
			CreatedAt: time.Now().UnixMilli(),
			Author:    int64(author),
		})
		if err != nil {
			return nil, err
		}
		return &state.Update{AssetDirectories: []*apigen.AssetDirectory{ptru.To(d)}}, nil
	})
	if err != nil {
		return apigen.AssetDirectory{}, err
	}
	return d, nil
}

func resolveAssetDirectory(ctx context.Context, q *pq.Queries, spaceID int64, directoryID int32) (int64, error) {
	dirID := int64(directoryID)
	if dirID == 0 {
		return 0, nil
	}
	d, err := getAssetDirectory(ctx, q, dirID)
	if err != nil {
		return 0, err
	}
	if int64(d.SpaceID) != spaceID {
		return 0, ErrDirectoryNotFound
	}
	return dirID, nil
}

func ListAssetDirectories(q *pq.Queries) []*apigen.AssetDirectory {
	rows := erru.Must(q.ListAssetDirectories(context.Background()))
	out := make([]*apigen.AssetDirectory, 0, len(rows))
	for _, row := range rows {
		out = append(out, ptru.To(row))
	}
	return out
}

func GetAssetDirectoryMeta(q *pq.Queries, directoryID int32) (*apigen.AssetDirectory, bool) {
	row, err := q.GetAssetDirectoryByID(context.Background(), int64(directoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetDirectoryByID: %v", err))
	}
	return ptru.To(row), true
}

func RenameDirectory(store *state.Service, directoryID int32, newKey string) (apigen.AssetDirectory, error) {
	if !ValidAssetKey(newKey) {
		return apigen.AssetDirectory{}, ErrAssetKeyInvalid
	}
	ctx := context.Background()
	var d apigen.AssetDirectory
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		var err error
		d, err = getAssetDirectory(ctx, q, int64(directoryID))
		if err != nil {
			return nil, err
		}
		if d.Key == newKey {
			return nil, nil
		}
		if assetSiblingKeyTaken(ctx, q, int64(d.SpaceID), int64(d.ParentID), newKey, 0, int64(d.ID)) {
			return nil, ErrAssetAlreadyExists
		}
		if err := q.SetAssetDirectoryKey(ctx, pq.SetAssetDirectoryKeyParams{Key: newKey, ID: int64(d.ID)}); err != nil {
			return nil, err
		}
		d, err = q.GetAssetDirectoryByID(ctx, int64(d.ID))
		if err != nil {
			return nil, err
		}
		return &state.Update{AssetDirectories: []*apigen.AssetDirectory{ptru.To(d)}}, nil
	})
	if err != nil {
		return apigen.AssetDirectory{}, err
	}
	return d, nil
}

func MoveDirectorySpace(q *pq.Queries, directoryID, newSpaceID int32) error {
	d, err := getAssetDirectory(context.Background(), q, int64(directoryID))
	if err != nil {
		return err
	}
	if nodes.NormalizedUserSpaceID(newSpaceID) == d.SpaceID {
		return nil
	}
	return ErrSpaceMoveUnsupported
}

func MoveDirectory(store *state.Service, directoryID, newParentID int32) (apigen.AssetDirectory, error) {
	ctx := context.Background()
	parent := int64(newParentID)
	var d apigen.AssetDirectory
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		var err error
		d, err = getAssetDirectory(ctx, q, int64(directoryID))
		if err != nil {
			return nil, err
		}
		if int64(d.ParentID) == parent {
			return nil, nil
		}
		for cur := parent; cur != 0; {
			if cur == int64(d.ID) {
				return nil, ErrDirectoryCycle
			}
			p, err := getAssetDirectory(ctx, q, cur)
			if err != nil {
				return nil, err
			}
			if p.SpaceID != d.SpaceID {
				return nil, ErrSpaceMoveUnsupported
			}
			cur = int64(p.ParentID)
		}
		if assetSiblingKeyTaken(ctx, q, int64(d.SpaceID), parent, d.Key, 0, int64(d.ID)) {
			return nil, ErrAssetAlreadyExists
		}
		if err := q.SetAssetDirectoryParent(ctx, pq.SetAssetDirectoryParentParams{ParentID: parent, ID: int64(d.ID)}); err != nil {
			return nil, err
		}
		d, err = q.GetAssetDirectoryByID(ctx, int64(d.ID))
		if err != nil {
			return nil, err
		}
		return &state.Update{AssetDirectories: []*apigen.AssetDirectory{ptru.To(d)}}, nil
	})
	if err != nil {
		return apigen.AssetDirectory{}, err
	}
	return d, nil
}

func DeleteDirectory(store *state.Service, directoryID int32) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		d, err := getAssetDirectory(ctx, q, int64(directoryID))
		if err != nil {
			return nil, err
		}
		assets, err := q.CountAssetsInDirectory(ctx, int64(d.ID))
		if err != nil {
			return nil, err
		}
		children, err := q.CountChildAssetDirectories(ctx, int64(d.ID))
		if err != nil {
			return nil, err
		}
		if assets > 0 || children > 0 {
			return nil, ErrDirectoryNotEmpty
		}
		if err := q.DeleteAssetDirectory(ctx, int64(d.ID)); err != nil {
			return nil, err
		}
		tombstone := ptru.To(d)
		tombstone.Deleted = true
		return &state.Update{AssetDirectories: []*apigen.AssetDirectory{tombstone}}, nil
	})
}
