package assets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/ptru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var (
	ErrDirectoryNotFound    = errors.New("asset directory not found")
	ErrDirectoryNotEmpty    = errors.New("asset directory is not empty")
	ErrDirectoryCycle       = errors.New("asset directory cannot be moved inside itself")
	ErrSpaceMoveUnsupported = errors.New("moving between spaces is not supported")
)

func getAssetDirectory(ctx context.Context, q *pq.Queries, id uint64) (apigen.AssetDirectory, error) {
	d, err := q.GetAssetDirectoryByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.AssetDirectory{}, ErrDirectoryNotFound
	}
	return d, err
}

const assetDirectoryType = apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY

func directoryMeta(seq int64, author int64, eventType apigen.AuthzVerb) pq.EventMeta {
	return pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), Author: author, EventType: eventType}
}

func directoryUpdate(meta pq.EventMeta, d *apigen.AssetDirectory) *state.WriteUpdate {
	return pq.NewUpdate(pq.AssetDirectoryMutation(meta, *d))
}

func CreateDirectory(store *state.Service, spaceID, parentID uint64, key string, author int64) (apigen.AssetDirectory, error) {
	if !ValidAssetKey(key) {
		return apigen.AssetDirectory{}, ErrAssetKeyInvalid
	}
	ctx := context.Background()
	space := nodes.NormalizedUserSpaceID(spaceID)
	parent := parentID
	var d apigen.AssetDirectory
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if parent != 0 {
			p, err := getAssetDirectory(ctx, q, parent)
			if err != nil {
				return nil, err
			}
			if p.SpaceID != space {
				return nil, ErrDirectoryNotFound
			}
		}
		if assetSiblingKeyTaken(ctx, q, space, parent, key, 0, 0) {
			return nil, ErrAssetAlreadyExists
		}
		id, err := q.NextEntityID(ctx, assetDirectoryType)
		if err != nil {
			return nil, err
		}
		meta := directoryMeta(seq, author, apigen.AuthzVerb_AUTHZ_VERB_CREATE)
		d = apigen.AssetDirectory{ID: id, SpaceID: space, Key: key, ParentID: directoryRef(parent)}
		return directoryUpdate(meta, &d), nil
	})
	if err != nil {
		return apigen.AssetDirectory{}, err
	}
	return d, nil
}

func resolveAssetDirectory(ctx context.Context, q *pq.Queries, spaceID, directoryID uint64) (uint64, error) {
	dirID := directoryID
	if dirID == 0 {
		return 0, nil
	}
	d, err := getAssetDirectory(ctx, q, dirID)
	if err != nil {
		return 0, err
	}
	if d.SpaceID != spaceID {
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

func GetAssetDirectoryMeta(q *pq.Queries, directoryID uint64) (*apigen.AssetDirectory, bool) {
	row, err := q.GetAssetDirectoryByID(context.Background(), directoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetDirectoryByID: %v", err))
	}
	return ptru.To(row), true
}

func RenameDirectory(store *state.Service, directoryID uint64, newKey string, author int64) (apigen.AssetDirectory, error) {
	if !ValidAssetKey(newKey) {
		return apigen.AssetDirectory{}, ErrAssetKeyInvalid
	}
	ctx := context.Background()
	var d apigen.AssetDirectory
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		var err error
		d, err = getAssetDirectory(ctx, q, directoryID)
		if err != nil {
			return nil, err
		}
		if d.Key == newKey {
			return nil, nil
		}
		if assetSiblingKeyTaken(ctx, q, d.SpaceID, d.ParentID.Value, newKey, assetDirectoryType, d.ID) {
			return nil, ErrAssetAlreadyExists
		}
		d.Key = newKey
		return directoryUpdate(directoryMeta(seq, author, apigen.AuthzVerb_AUTHZ_VERB_UPDATE), &d), nil
	})
	if err != nil {
		return apigen.AssetDirectory{}, err
	}
	return d, nil
}

func MoveDirectorySpace(q *pq.Queries, directoryID, newSpaceID uint64) error {
	d, err := getAssetDirectory(context.Background(), q, directoryID)
	if err != nil {
		return err
	}
	if nodes.NormalizedUserSpaceID(newSpaceID) == d.SpaceID {
		return nil
	}
	return ErrSpaceMoveUnsupported
}

func MoveDirectory(store *state.Service, directoryID, newParentID uint64, author int64) (apigen.AssetDirectory, error) {
	ctx := context.Background()
	parent := newParentID
	var d apigen.AssetDirectory
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		var err error
		d, err = getAssetDirectory(ctx, q, directoryID)
		if err != nil {
			return nil, err
		}
		if d.ParentID.Value == parent {
			return nil, nil
		}
		for cur := parent; cur != 0; {
			if cur == d.ID {
				return nil, ErrDirectoryCycle
			}
			p, err := getAssetDirectory(ctx, q, cur)
			if err != nil {
				return nil, err
			}
			if p.SpaceID != d.SpaceID {
				return nil, ErrSpaceMoveUnsupported
			}
			cur = p.ParentID.Value
		}
		if assetSiblingKeyTaken(ctx, q, d.SpaceID, parent, d.Key, assetDirectoryType, d.ID) {
			return nil, ErrAssetAlreadyExists
		}
		d.ParentID = directoryRef(parent)
		return directoryUpdate(directoryMeta(seq, author, apigen.AuthzVerb_AUTHZ_VERB_UPDATE), &d), nil
	})
	if err != nil {
		return apigen.AssetDirectory{}, err
	}
	return d, nil
}

func DeleteDirectory(store *state.Service, directoryID uint64, author int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		d, err := getAssetDirectory(ctx, q, directoryID)
		if err != nil {
			return nil, err
		}
		children, err := q.CountAssetKeysUnder(ctx, d.ID)
		if err != nil {
			return nil, err
		}
		if children > 0 {
			return nil, ErrDirectoryNotEmpty
		}
		return pq.NewUpdate(pq.DeleteMutation(directoryMeta(seq, author, apigen.AuthzVerb_AUTHZ_VERB_DELETE), assetDirectoryType, d.ID)), nil
	})
}
