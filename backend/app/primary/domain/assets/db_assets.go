package assets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var (
	ErrAssetNotFound       = errors.New("asset not found")
	ErrAssetAlreadyExists  = errors.New("asset already exists")
	ErrAssetKeyInvalid     = errors.New("asset key is not a valid file name")
	ErrAssetContentMissing = errors.New("asset content is not in the store")
)

func ValidAssetKey(key string) bool {
	if key == "" || key == "." || key == ".." || len(key) > 255 {
		return false
	}
	return !strings.ContainsAny(key, "/\\\x00")
}

const assetType = apigen.CoreEntityType_CORE_ENTITY_ASSET

func ListAssets(q *pq.Queries) []*pq.AssetEvent {
	return erru.Must(q.ListAssetEvents(context.Background()))
}

func GetAsset(q *pq.Queries, assetID int32) (*pq.AssetEvent, bool) {
	row, err := q.GetAssetEvent(context.Background(), int64(assetID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(err)
	}
	return row, true
}

func GetAssetInDirectory(q *pq.Queries, spaceID, directoryID int32, key string) (pq.AssetRow, bool) {
	ctx := context.Background()
	k, err := q.LookupAssetKey(ctx, int64(nodes.NormalizedUserSpaceID(spaceID)), int64(directoryID), key)
	if errors.Is(err, sql.ErrNoRows) {
		return pq.AssetRow{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("LookupAssetKey: %v", err))
	}
	if k.Kind != assetType {
		return pq.AssetRow{}, false
	}
	r, err := q.GetAssetRowByID(ctx, k.ID)
	if err != nil {
		panic(fmt.Sprintf("GetAssetRowByID: %v", err))
	}
	return r, true
}

type AssetVersionRef struct {
	Ref     apigen.ValueRef
	Key     string
	SpaceID int32
}

func GetAssetVersionRef(q *pq.Queries, ref apigen.ValueRef) (AssetVersionRef, bool) {
	r, ok := GetAssetValueJoined(q, ref)
	if !ok {
		return AssetVersionRef{}, false
	}
	return AssetVersionRef{Ref: ref, Key: r.Asset.Key, SpaceID: int32(r.Asset.SpaceID)}, true
}

// GetAssetValueJoined resolves a pinned asset value reference.
func GetAssetValueJoined(q *pq.Queries, ref apigen.ValueRef) (pq.AssetVersionJoined, bool) {
	r, err := q.GetAssetVersionJoined(context.Background(), ref)
	if errors.Is(err, sql.ErrNoRows) {
		return pq.AssetVersionJoined{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetVersionJoined: %v", err))
	}
	return r, true
}

func assetSiblingKeyTaken(ctx context.Context, q *pq.Queries, spaceID, directoryID int64, key string, self apigen.CoreEntityType, selfID int64) bool {
	return erru.Must(q.AssetKeyTaken(ctx, spaceID, directoryID, key, self, selfID))
}

// requireStoredContent checks that storageKey names a completed content row
// holding sha256, so an identity never points at content that is not there.
func requireStoredContent(ctx context.Context, q *pq.Queries, sha256, storageKey string) error {
	if sha256 == "" || storageKey == "" {
		return ErrAssetContentMissing
	}
	row, err := q.GetAssetStoreRowByID(ctx, storageKey)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAssetContentMissing
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetStoreRowByID: %v", err))
	}
	if row.Sha256 != sha256 {
		return ErrAssetContentMissing
	}
	return nil
}

type currentAsset struct {
	Asset   pq.AssetRow
	Version pq.AssetVersionRow
}

func (c currentAsset) entity() apigen.Asset { return pq.AssetEntity(c.Asset, c.Version) }

func latestAsset(ctx context.Context, q *pq.Queries, assetID int32) (currentAsset, bool) {
	a, err := q.GetAssetRowByID(ctx, int64(assetID))
	if errors.Is(err, sql.ErrNoRows) {
		return currentAsset{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetRowByID: %v", err))
	}
	v, err := q.GetAssetVersion(ctx, apigen.ValueRef{ID: int32(a.ID), Version: int32(a.ValueVersion)})
	if err != nil {
		panic(fmt.Sprintf("GetAssetVersion: %v", err))
	}
	return currentAsset{Asset: a, Version: v}, true
}

type assetWrite struct {
	verb   apigen.AuthzVerb
	author int32
	id     int64
	asset  apigen.Asset
}

// commitAssetWrite commits the mutation build returns; build receives the
// commit time in unix milliseconds and returns nil to write nothing.
func commitAssetWrite(store *state.Service, ctx context.Context, inlockValidate func(*pq.Queries) error, build func(q *pq.Queries, now int64) (*assetWrite, error)) error {
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		now := time.Now().UnixMilli()
		w, err := build(q, now)
		if err != nil || w == nil {
			return nil, err
		}
		if inlockValidate != nil {
			if err := inlockValidate(q); err != nil {
				return nil, err
			}
		}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: int64(w.author), EventType: w.verb}
		if w.verb == apigen.AuthzVerb_AUTHZ_VERB_DELETE {
			return pq.NewUpdate(pq.DeleteMutation(meta, assetType, w.id)), nil
		}
		return pq.NewUpdate(pq.AssetMutation(meta, w.id, w.asset)), nil
	})
}

func CreateAssetWithVersion(store *state.Service, key string, spaceID, directoryID, author int32, sha256, storageKey string, sizeBytes int64) (*pq.AssetEvent, error) {
	if !ValidAssetKey(key) {
		return nil, ErrAssetKeyInvalid
	}
	ctx := context.Background()
	space := int64(nodes.NormalizedUserSpaceID(spaceID))
	var assetID int64
	err := commitAssetWrite(store, ctx, nil, func(q *pq.Queries, now int64) (*assetWrite, error) {
		if err := requireStoredContent(ctx, q, sha256, storageKey); err != nil {
			return nil, err
		}
		dirID, err := resolveAssetDirectory(ctx, q, space, directoryID)
		if err != nil {
			return nil, err
		}
		if assetSiblingKeyTaken(ctx, q, space, dirID, key, 0, 0) {
			return nil, ErrAssetAlreadyExists
		}
		assetID, err = q.NextEntityID(ctx, assetType)
		if err != nil {
			return nil, err
		}
		return &assetWrite{verb: apigen.AuthzVerb_AUTHZ_VERB_CREATE, author: author, id: assetID, asset: apigen.Asset{
			Fs: &apigen.AssetFs{Key: key, DirectoryID: int32(dirID)}, SpaceID: int32(space), SizeBytes: sizeBytes, Sha256: sha256, StorageKey: storageKey,
		}}, nil
	})
	if err != nil {
		return nil, err
	}
	asset, ok := GetAsset(store.Queries(), int32(assetID))
	if !ok {
		panic(fmt.Sprintf("created asset %d not readable", assetID))
	}
	return asset, nil
}

func AppendAssetVersion(store *state.Service, assetID, author int32, sha256, storageKey string, sizeBytes int64) (*pq.AssetEvent, error) {
	ctx := context.Background()
	err := commitAssetWrite(store, ctx, nil, func(q *pq.Queries, now int64) (*assetWrite, error) {
		if err := requireStoredContent(ctx, q, sha256, storageKey); err != nil {
			return nil, err
		}
		prev, ok := latestAsset(ctx, q, assetID)
		if !ok {
			return nil, ErrAssetNotFound
		}
		if prev.Version.Sha256 == sha256 && prev.Version.StorageKey == storageKey {
			return nil, nil
		}
		next := prev.entity()
		next.SizeBytes, next.Sha256, next.StorageKey = sizeBytes, sha256, storageKey
		return &assetWrite{verb: apigen.AuthzVerb_AUTHZ_VERB_UPDATE, author: author, id: prev.Asset.ID, asset: next}, nil
	})
	if err != nil {
		return nil, err
	}
	asset, ok := GetAsset(store.Queries(), assetID)
	if !ok {
		panic(fmt.Sprintf("appended asset %d not readable", assetID))
	}
	return asset, nil
}

func RenameAssetKey(store *state.Service, assetID int32, newKey string) (*pq.AssetEvent, error) {
	if !ValidAssetKey(newKey) {
		return nil, ErrAssetKeyInvalid
	}
	ctx := context.Background()
	err := commitAssetWrite(store, ctx, nil, func(q *pq.Queries, now int64) (*assetWrite, error) {
		prev, ok := latestAsset(ctx, q, assetID)
		if !ok {
			return nil, ErrAssetNotFound
		}
		if prev.Asset.Key == newKey {
			return nil, nil
		}
		if assetSiblingKeyTaken(ctx, q, prev.Asset.SpaceID, prev.Asset.DirectoryID, newKey, assetType, prev.Asset.ID) {
			return nil, ErrAssetAlreadyExists
		}
		next := prev.entity()
		next.Fs.Key = newKey
		return &assetWrite{verb: apigen.AuthzVerb_AUTHZ_VERB_UPDATE, id: prev.Asset.ID, asset: next}, nil
	})
	if err != nil {
		return nil, err
	}
	asset, ok := GetAsset(store.Queries(), assetID)
	if !ok {
		return nil, ErrAssetNotFound
	}
	return asset, nil
}

func MoveAssetDirectory(store *state.Service, assetID, newDirectoryID int32) error {
	ctx := context.Background()
	dirID := int64(newDirectoryID)
	return commitAssetWrite(store, ctx, nil, func(q *pq.Queries, now int64) (*assetWrite, error) {
		prev, ok := latestAsset(ctx, q, assetID)
		if !ok {
			return nil, ErrAssetNotFound
		}
		if prev.Asset.DirectoryID == dirID {
			return nil, nil
		}
		if dirID != 0 {
			dir, err := getAssetDirectory(ctx, q, dirID)
			if err != nil {
				return nil, err
			}
			if int64(dir.SpaceID) != prev.Asset.SpaceID {
				return nil, ErrSpaceMoveUnsupported
			}
		}
		if assetSiblingKeyTaken(ctx, q, prev.Asset.SpaceID, dirID, prev.Asset.Key, assetType, prev.Asset.ID) {
			return nil, ErrAssetAlreadyExists
		}
		next := prev.entity()
		next.Fs.DirectoryID = int32(dirID)
		return &assetWrite{verb: apigen.AuthzVerb_AUTHZ_VERB_UPDATE, id: prev.Asset.ID, asset: next}, nil
	})
}

func MoveAssetSpace(store *state.Service, assetID, newSpaceID, newDirectoryID, author int32, inlockValidate func(*pq.Queries) error) error {
	ctx := context.Background()
	spaceID := int64(nodes.NormalizedUserSpaceID(newSpaceID))
	dirID := int64(newDirectoryID)
	return commitAssetWrite(store, ctx, inlockValidate, func(q *pq.Queries, now int64) (*assetWrite, error) {
		prev, ok := latestAsset(ctx, q, assetID)
		if !ok {
			return nil, ErrAssetNotFound
		}
		if spaceID == prev.Asset.SpaceID && dirID == prev.Asset.DirectoryID {
			return nil, nil
		}
		if dirID != 0 {
			dir, err := getAssetDirectory(ctx, q, dirID)
			if err != nil {
				return nil, err
			}
			if int64(dir.SpaceID) != spaceID {
				return nil, ErrDirectoryNotFound
			}
		}
		if assetSiblingKeyTaken(ctx, q, spaceID, dirID, prev.Asset.Key, assetType, prev.Asset.ID) {
			return nil, ErrAssetAlreadyExists
		}
		next := prev.entity()
		next.Fs.DirectoryID, next.SpaceID = int32(dirID), int32(spaceID)
		return &assetWrite{verb: apigen.AuthzVerb_AUTHZ_VERB_UPDATE, author: author, id: prev.Asset.ID, asset: next}, nil
	})
}

func DeleteAsset(store *state.Service, assetID int32, inlockValidate func(*pq.Queries) error) error {
	ctx := context.Background()
	return commitAssetWrite(store, ctx, inlockValidate, func(q *pq.Queries, now int64) (*assetWrite, error) {
		prev, ok := latestAsset(ctx, q, assetID)
		if !ok {
			return nil, nil
		}
		return &assetWrite{verb: apigen.AuthzVerb_AUTHZ_VERB_DELETE, id: prev.Asset.ID}, nil
	})
}
