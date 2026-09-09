package assets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"strings"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/ptru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
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

func ListAssets(q *pq.Queries) []*apigen.AssetEvent {
	rows := erru.Must(q.ListLatestLiveAssetEvents(context.Background()))
	out := make([]*apigen.AssetEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, ptru.To(row))
	}
	return out
}
func GetAsset(q *pq.Queries, assetID int32) (*apigen.AssetEvent, bool) {
	row, err := q.GetLatestAssetEvent(context.Background(), int64(assetID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(err)
	}
	if row.EventType == apigen.EventType_EVENT_TYPE_DELETE {
		return nil, false
	}
	return ptru.To(row), true
}
func GetAssetInDirectory(q *pq.Queries, spaceID, directoryID int32, key string) (pq.AssetRow, bool) {
	r, err := q.GetAssetInDirectoryByKey(context.Background(), pq.GetAssetInDirectoryByKeyParams{
		SpaceID:          int64(nodes.NormalizedUserSpaceID(spaceID)),
		AssetDirectoryID: int64(directoryID),
		Key:              key,
	})
	if err == sql.ErrNoRows {
		return pq.AssetRow{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetInDirectoryByKey: %v", err))
	}
	return r, true
}

type AssetVersionRef struct {
	VersionID int32
	AssetID   int32
	Key       string
	SpaceID   int32
}

func GetAssetVersionRef(q *pq.Queries, assetVersionID int32) (AssetVersionRef, bool) {
	r, ok := GetAssetVersionJoined(q, assetVersionID)
	if !ok {
		return AssetVersionRef{}, false
	}
	return AssetVersionRef{
		VersionID: int32(r.Version.ID),
		AssetID:   int32(r.Asset.ID),
		Key:       r.Asset.Key,
		SpaceID:   int32(r.Asset.SpaceID),
	}, true
}
func GetAssetVersionJoined(q *pq.Queries, assetVersionID int32) (pq.AssetVersionJoined, bool) {
	r, err := q.GetAssetVersionJoinedByID(context.Background(), int64(assetVersionID))
	if err == sql.ErrNoRows {
		return pq.AssetVersionJoined{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetVersionJoinedByID: %v", err))
	}
	return r, true
}
func AssetVersionIDs(q *pq.Queries, assetID int32) []int32 {
	rows := erru.Must(q.ListAssetVersionIDsByAssetID(context.Background(), int64(assetID)))
	ids := make([]int32, 0, len(rows))
	for _, id := range rows {
		ids = append(ids, int32(id))
	}
	return ids
}
func assetSiblingKeyTaken(ctx context.Context, q *pq.Queries, spaceID, directoryID int64, key string, excludeAssetID, excludeDirectoryID int64) bool {
	assets := erru.Must(q.CountAssetSiblingsWithKey(ctx, pq.CountAssetSiblingsWithKeyParams{
		SpaceID:          spaceID,
		AssetDirectoryID: directoryID,
		Key:              key,
		ID:               excludeAssetID,
	}))
	if assets > 0 {
		return true
	}
	dirs := erru.Must(q.CountDirectorySiblingsWithKey(ctx, pq.CountDirectorySiblingsWithKeyParams{
		SpaceID:  spaceID,
		ParentID: directoryID,
		Key:      key,
		ID:       excludeDirectoryID,
	}))
	return dirs > 0
}

func assetStoreRefBySha(ctx context.Context, q *pq.Queries, sha256 string) (pq.AssetStoreRef, error) {
	if sha256 == "" {
		return pq.AssetStoreRef{}, ErrAssetContentMissing
	}
	row, err := q.GetAssetStoreRowBySha(ctx, sha256)
	if errors.Is(err, sql.ErrNoRows) {
		return pq.AssetStoreRef{}, ErrAssetContentMissing
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetStoreRowBySha: %v", err))
	}
	return pq.AssetStoreRef{
		ID:           row.ID,
		LocalStatus:  row.LocalStatus,
		RemoteStatus: row.RemoteStatus,
		InlineSize:   int64(len(row.InlineBlob)),
		InlineBlob:   row.InlineBlob,
	}, nil
}

func nextAssetEvent(prev apigen.AssetEvent, author int32, eventType apigen.EventType) apigen.AssetEvent {
	event := prev
	event.EventID, event.Seq = 0, 0
	event.EventTime = time.Now().UnixMilli()
	event.Author, event.EventType = author, eventType
	event.Version++
	event.Value.Fs = ptru.To(*prev.Value.Fs)
	return event
}

func appendAssetEvent(store *state.Service, ctx context.Context, inlockValidate pq.Validator, event apigen.AssetEvent) error {
	return store.Commit(ctx, inlockValidate, func(q *pq.Queries, seq int64) (*state.Update, error) {
		event.Seq = seq
		err := q.InsertAssetEvent(ctx, &event)
		if err != nil {
			return nil, err
		}
		return &state.Update{AssetEvents: []*apigen.AssetEvent{ptru.To(event)}}, nil
	})
}

func latestAssetEvent(ctx context.Context, q *pq.Queries, assetID int32) (apigen.AssetEvent,
	bool) {
	e, err := q.GetLatestAssetEvent(ctx, int64(assetID))
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.AssetEvent{},
			false
	}
	if err != nil {
		panic(fmt.Sprintf("GetLatestAssetEvent: %v", err))
	}
	return e, e.EventType != apigen.EventType_EVENT_TYPE_DELETE
}
func CreateAssetWithVersion(store *state.Service, key string, spaceID, directoryID, author int32, sha256 string, sizeBytes int64) (*apigen.AssetEvent, error) {
	if !ValidAssetKey(key) {
		return nil, ErrAssetKeyInvalid
	}
	ctx := context.Background()
	now := time.Now().UnixMilli()
	space := int64(nodes.NormalizedUserSpaceID(spaceID))

	if _, err := assetStoreRefBySha(ctx, store.Queries(), sha256); err != nil {
		return nil, err
	}
	dirID, err := resolveAssetDirectory(ctx, store.Queries(), space, directoryID)
	if err != nil {
		return nil, err
	}
	if assetSiblingKeyTaken(ctx, store.Queries(), space, dirID, key, 0, 0) {
		return nil, ErrAssetAlreadyExists
	}

	var assetID int64
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		id := erru.Must(q.NextAssetID(ctx))
		assetID = id
		event := apigen.AssetEvent{
			Seq:          seq,
			EventTime:    now,
			CreatedTime:  now,
			Author:       author,
			AssetID:      int32(id),
			Version:      1,
			ValueVersion: 1,
			SpaceVersion: 1,
			Value:        apigen.Asset{Fs: &apigen.AssetFs{Key: key, DirectoryID: int32(dirID)}, SpaceID: int32(space), SizeBytes: sizeBytes, Sha256: sha256},
			EventType:    apigen.EventType_EVENT_TYPE_CREATE,
		}

		err := q.InsertAssetEvent(ctx, &event)
		if err != nil {
			return nil, err
		}
		return &state.Update{AssetEvents: []*apigen.AssetEvent{ptru.To(event)}}, nil
	}); err != nil {
		panic(fmt.Sprintf("asset create tx: %v", err))
	}
	asset, ok := GetAsset(store.Queries(), int32(assetID))
	if !ok {
		panic(fmt.Sprintf("created asset %d not readable", assetID))
	}
	return asset, nil
}
func AppendAssetVersion(store *state.Service, assetID, author int32, sha256 string, sizeBytes int64) (*apigen.AssetEvent, error) {
	ctx := context.Background()

	if _, err := assetStoreRefBySha(ctx, store.Queries(), sha256); err != nil {
		return nil, err
	}
	prev, ok := latestAssetEvent(ctx, store.Queries(), assetID)
	if !ok {
		return nil, ErrAssetNotFound
	}
	event := nextAssetEvent(prev, author, apigen.EventType_EVENT_TYPE_UPDATE)
	event.ValueVersion = prev.ValueVersion + 1
	event.Value.SizeBytes = sizeBytes
	event.Value.Sha256 = sha256
	erru.Must(0, appendAssetEvent(store, ctx, nil, event))
	asset, ok := GetAsset(store.Queries(), assetID)
	if !ok {
		panic(fmt.Sprintf("appended asset %d not readable", assetID))
	}
	return asset, nil
}
func RenameAssetKey(store *state.Service, assetID int32, newKey string) (*apigen.AssetEvent, error) {
	if !ValidAssetKey(newKey) {
		return nil, ErrAssetKeyInvalid
	}
	ctx := context.Background()

	prev, ok := latestAssetEvent(ctx, store.Queries(), assetID)
	if !ok {
		return nil, ErrAssetNotFound
	}
	if prev.Value.Fs.Key != newKey {
		if assetSiblingKeyTaken(ctx, store.Queries(), int64(prev.Value.SpaceID), int64(prev.Value.Fs.DirectoryID), newKey, int64(prev.AssetID), 0) {
			return nil, ErrAssetAlreadyExists
		}
		event := nextAssetEvent(prev, 0, apigen.EventType_EVENT_TYPE_UPDATE)
		event.Value.Fs.Key = newKey
		erru.Must(0, appendAssetEvent(store, ctx, nil, event))
	}
	asset, ok := GetAsset(store.Queries(), assetID)
	if !ok {
		return nil, ErrAssetNotFound
	}
	return asset, nil
}
func MoveAssetDirectory(store *state.Service, assetID, newDirectoryID int32) (pq.AssetRow, error) {
	ctx := context.Background()
	prev, ok := latestAssetEvent(ctx, store.Queries(), assetID)
	if !ok {
		return pq.AssetRow{}, ErrAssetNotFound
	}
	dirID := int64(newDirectoryID)
	current := pq.AssetRow{ID: int64(prev.AssetID), Key: prev.Value.Fs.Key, SpaceID: int64(prev.Value.SpaceID), AssetDirectoryID: int64(prev.Value.Fs.DirectoryID), CreatedAt: prev.CreatedTime}
	if int64(prev.Value.Fs.DirectoryID) == dirID {
		return current, nil
	}
	if dirID != 0 {
		dir, err := store.Queries().GetAssetDirectoryByID(ctx, dirID)
		if errors.Is(err, sql.ErrNoRows) {
			return pq.AssetRow{}, ErrDirectoryNotFound
		}
		if err != nil {
			panic(fmt.Sprintf("GetAssetDirectoryByID: %v", err))
		}
		if dir.SpaceID != prev.Value.SpaceID {
			return pq.AssetRow{}, ErrSpaceMoveUnsupported
		}
	}
	if assetSiblingKeyTaken(ctx, store.Queries(), int64(prev.Value.SpaceID), dirID, prev.Value.Fs.Key, int64(prev.AssetID), 0) {
		return pq.AssetRow{}, ErrAssetAlreadyExists
	}
	event := nextAssetEvent(prev, 0, apigen.EventType_EVENT_TYPE_UPDATE)
	event.Value.Fs.DirectoryID = int32(dirID)
	erru.Must(0, appendAssetEvent(store, ctx, nil, event))
	current.AssetDirectoryID = dirID
	return current, nil
}
func MoveAssetSpace(store *state.Service, assetID, newSpaceID, newDirectoryID, author int32, inlockValidate pq.Validator) error {
	ctx := context.Background()

	prev, ok := latestAssetEvent(ctx, store.Queries(), assetID)
	if !ok {
		return ErrAssetNotFound
	}
	spaceID := int64(nodes.NormalizedUserSpaceID(newSpaceID))
	dirID := int64(newDirectoryID)
	if spaceID == int64(prev.Value.SpaceID) &&
		dirID == int64(prev.Value.Fs.DirectoryID) {
		return nil
	}
	if dirID != 0 {
		dir, err := store.Queries().GetAssetDirectoryByID(ctx, dirID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDirectoryNotFound
		}
		if err != nil {
			panic(fmt.Sprintf("GetAssetDirectoryByID: %v", err))
		}
		if int64(dir.SpaceID) != spaceID {
			return ErrDirectoryNotFound
		}
	}
	if assetSiblingKeyTaken(ctx, store.Queries(), spaceID, dirID, prev.Value.Fs.Key, int64(prev.AssetID), 0) {
		return ErrAssetAlreadyExists
	}
	event := nextAssetEvent(prev, author, apigen.EventType_EVENT_TYPE_UPDATE)
	event.Value.Fs.DirectoryID = int32(dirID)
	if spaceID != int64(prev.Value.SpaceID) {
		event.Value.SpaceID = int32(spaceID)
		event.SpaceVersion = prev.SpaceVersion + 1

	}
	return appendAssetEvent(store, ctx, inlockValidate, event)
}
func DeleteAsset(store *state.Service, assetID int32, inlockValidate pq.Validator) error {
	ctx := context.Background()
	prev, ok := latestAssetEvent(ctx, store.Queries(), assetID)
	if !ok {
		return nil
	}
	return appendAssetEvent(store, ctx, inlockValidate, nextAssetEvent(prev, 0, apigen.EventType_EVENT_TYPE_DELETE))
}
