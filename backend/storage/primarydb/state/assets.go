package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/apigen"
)

var (
	ErrAssetNotFound       = errors.New("asset not found")
	ErrAssetAlreadyExists  = errors.New("asset already exists")
	ErrAssetKeyInvalid     = errors.New("asset key is not a valid file name")
	ErrAssetContentMissing = errors.New("asset content is not in the store")
)

// ValidAssetKey reports whether key can be a file name in the asset namespace.
// Path separators are excluded because the full asset path is the join of the
// directory ancestry and the key.
func ValidAssetKey(key string) bool {
	if key == "" || key == "." || key == ".." || len(key) > 255 {
		return false
	}
	return !strings.ContainsAny(key, "/\\\x00")
}

func (s *Service) ListAssets() []*apigen.Asset {
	ctx := context.Background()
	rows := erru.Must(s.q.ListAssetRows(ctx))
	joined := erru.Must(s.q.ListAssetVersionsJoined(ctx))
	events := erru.Must(s.q.ListAllAssetEvents(ctx))
	versions := make(map[int64][]pq.AssetVersionJoined, len(rows))
	for _, v := range joined {
		versions[v.Version.AssetID] = append(versions[v.Version.AssetID], v)
	}
	eventsByAsset := make(map[int64][]pq.AssetEvent, len(rows))
	for _, e := range events {
		eventsByAsset[e.AssetID] = append(eventsByAsset[e.AssetID], e)
	}
	out := make([]*apigen.Asset, 0, len(rows))
	for _, r := range rows {
		vs := versions[r.ID]
		if len(vs) == 0 {
			continue
		}
		out = append(out, assetFromParts(r, eventsByAsset[r.ID], vs))
	}
	return out
}

func (s *Service) NotifyAssetUpdate(a *apigen.Asset) {
	if a == nil || a.ID == 0 {
		return
	}
	s.assetSubs.Notify(*a)
}

// NotifyAssetDeleted publishes a tombstone: the same asset stamped deleted now.
func (s *Service) NotifyAssetDeleted(a *apigen.Asset) {
	if a == nil || a.ID == 0 {
		return
	}
	cp := *a
	cp.DeletedAt = time.Now()
	s.assetSubs.Notify(cp)
}

func (s *Service) SubscribeAssetUpdates() (*pubsubu.Sub[apigen.Asset], func()) {
	sub := s.assetSubs.Subscribe(nil)
	return sub, sub.Unsubscribe
}

// GetAssetRow returns the asset's current identity facets.
func (s *Service) GetAssetRow(assetID int32) (Asset, bool) {
	r, err := s.q.GetAssetByID(context.Background(), int64(assetID))
	if errors.Is(err, sql.ErrNoRows) {
		return Asset{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetByID: %v", err))
	}
	return r, true
}

// GetAsset returns the asset with its space and content logs, or false when
// the asset does not exist or has no version.
func (s *Service) GetAsset(assetID int32) (*apigen.Asset, bool) {
	a, ok := s.GetAssetRow(assetID)
	if !ok {
		return nil, false
	}
	versions := erru.Must(s.q.ListAssetVersionsOfAsset(context.Background(), a.ID))
	if len(versions) == 0 {
		return nil, false
	}
	events := erru.Must(s.q.ListAssetEvents(context.Background(), a.ID))
	return assetFromParts(a, events, versions), true
}

// GetAssetInDirectory resolves an asset by key inside one directory of a
// space (0 = the implicit root).
func (s *Service) GetAssetInDirectory(spaceID, directoryID int32, key string) (Asset, bool) {
	r, err := s.q.GetAssetInDirectoryByKey(context.Background(), pq.GetAssetInDirectoryByKeyParams{
		SpaceID:          int64(normalizedUserSpaceID(spaceID)),
		AssetDirectoryID: int64(directoryID),
		Key:              key,
	})
	if err == sql.ErrNoRows {
		return Asset{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetInDirectoryByKey: %v", err))
	}
	return r, true
}

// AssetVersionRef resolves a pinned content version row id — the id
// deployment configs pin and secondaries fetch by — to its owning asset's facts.
type AssetVersionRef struct {
	VersionID int32
	AssetID   int32
	Key       string
	SpaceID   int32
}

func (s *Service) GetAssetVersionRef(assetVersionID int32) (AssetVersionRef, bool) {
	r, ok := s.GetAssetVersionJoined(assetVersionID)
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

// GetAssetVersionJoined resolves a version row id to the raw joined row,
// including the content-store fields and inline blob.
func (s *Service) GetAssetVersionJoined(assetVersionID int32) (AssetVersionJoined, bool) {
	r, err := s.q.GetAssetVersionJoinedByID(context.Background(), int64(assetVersionID))
	if err == sql.ErrNoRows {
		return AssetVersionJoined{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetVersionJoinedByID: %v", err))
	}
	return r, true
}

// ListAssetVersionsJoinedOfAsset returns the raw joined version rows of one
// asset, oldest first.
func (s *Service) ListAssetVersionsJoinedOfAsset(assetID int32) []AssetVersionJoined {
	rows := erru.Must(s.q.ListAssetVersionsOfAsset(context.Background(), int64(assetID)))
	return rows
}

// assetSiblingKeyTakenLocked reports whether key is already used by another
// asset or a directory under (spaceID, directoryID). Caller must hold s.Mu:
// path uniqueness spans the event log and asset_directories, so only the mutex
// makes the check-and-write atomic. excludeAssetID/excludeDirectoryID exempt
// the row being renamed or moved (0 = exclude nothing).
func (s *Service) assetSiblingKeyTakenLocked(ctx context.Context, q *pq.Queries, spaceID, directoryID int64, key string, excludeAssetID, excludeDirectoryID int64) bool {
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

func (s *Service) assetStoreRefBySha(ctx context.Context, sha256 string) (pq.AssetStoreRef, error) {
	if sha256 == "" {
		return pq.AssetStoreRef{}, ErrAssetContentMissing
	}
	row, err := s.q.GetAssetStoreRowBySha(ctx, sha256)
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

func nextAssetEvent(prev pq.AssetEvent, author int32, eventType int64) pq.AssetEvent {
	return pq.AssetEvent{
		EventTime:        time.Now().UnixMilli(),
		CreatedTime:      prev.CreatedTime,
		Author:           int64(author),
		AssetID:          prev.AssetID,
		Version:          prev.Version + 1,
		ValueVersion:     prev.ValueVersion,
		SpaceVersion:     prev.SpaceVersion,
		Key:              prev.Key,
		AssetDirectoryID: prev.AssetDirectoryID,
		SpaceID:          prev.SpaceID,
		SizeBytes:        prev.SizeBytes,
		Sha256:           prev.Sha256,
		EventType:        eventType,
	}
}

func (s *Service) appendAssetEventLocked(ctx context.Context, event pq.AssetEvent) {
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		event.GlobalSeq = seq
		return q.InsertAssetEvent(ctx, event)
	}); err != nil {
		panic(fmt.Sprintf("append asset event: %v", err))
	}
}

func (s *Service) mustLatestAssetEventLocked(ctx context.Context, assetID int32) (pq.AssetEvent, bool) {
	e, err := s.q.GetLatestAssetEvent(ctx, int64(assetID))
	if errors.Is(err, sql.ErrNoRows) {
		return pq.AssetEvent{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetLatestAssetEvent: %v", err))
	}
	return e, e.EventType != pq.EventDelete
}

// CreateAssetWithVersion creates a new asset in directoryID (0 = the space
// root) of spaceID with its first version. The content must already be in the
// asset store under sha256.
func (s *Service) CreateAssetWithVersion(key string, spaceID, directoryID, author int32, sha256 string, sizeBytes int64) (*apigen.Asset, error) {
	if !ValidAssetKey(key) {
		return nil, ErrAssetKeyInvalid
	}
	ctx := context.Background()
	now := time.Now().UnixMilli()
	space := int64(normalizedUserSpaceID(spaceID))

	s.Mu.Lock()
	defer s.Mu.Unlock()

	if _, err := s.assetStoreRefBySha(ctx, sha256); err != nil {
		return nil, err
	}
	dirID, err := s.resolveAssetDirectoryLocked(ctx, space, directoryID)
	if err != nil {
		return nil, err
	}
	if s.assetSiblingKeyTakenLocked(ctx, s.q, space, dirID, key, 0, 0) {
		return nil, ErrAssetAlreadyExists
	}

	var assetID int64
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		seq := erru.Must(q.NextGlobalSeq(ctx))
		id := erru.Must(q.NextAssetID(ctx))
		assetID = id
		return q.InsertAssetEvent(ctx, pq.AssetEvent{
			GlobalSeq:        seq,
			EventTime:        now,
			CreatedTime:      now,
			Author:           int64(author),
			AssetID:          id,
			Version:          1,
			ValueVersion:     1,
			SpaceVersion:     1,
			ValueChanged:     1,
			SpaceChanged:     1,
			Key:              key,
			AssetDirectoryID: dirID,
			SpaceID:          space,
			SizeBytes:        sizeBytes,
			Sha256:           sha256,
			EventType:        pq.EventCreate,
		})
	}); err != nil {
		panic(fmt.Sprintf("asset create tx: %v", err))
	}
	asset, ok := s.GetAsset(int32(assetID))
	if !ok {
		panic(fmt.Sprintf("created asset %d not readable", assetID))
	}
	return asset, nil
}

// AppendAssetVersion appends the next content version of an existing asset.
// The asset identity — key, space, directory — is untouched. The content must
// already be in the asset store under sha256.
func (s *Service) AppendAssetVersion(assetID, author int32, sha256 string, sizeBytes int64) (*apigen.Asset, error) {
	ctx := context.Background()

	s.Mu.Lock()
	defer s.Mu.Unlock()

	if _, err := s.assetStoreRefBySha(ctx, sha256); err != nil {
		return nil, err
	}
	prev, ok := s.mustLatestAssetEventLocked(ctx, assetID)
	if !ok {
		return nil, ErrAssetNotFound
	}
	event := nextAssetEvent(prev, author, pq.EventUpdate)
	event.ValueVersion = prev.ValueVersion + 1
	event.ValueChanged = 1
	event.SizeBytes = sizeBytes
	event.Sha256 = sha256
	s.appendAssetEventLocked(ctx, event)
	asset, ok := s.GetAsset(assetID)
	if !ok {
		panic(fmt.Sprintf("appended asset %d not readable", assetID))
	}
	return asset, nil
}

// RenameAssetKey renames an asset as an event. Content version rows, ids, and
// content are untouched; deployment configs keep working because they pin
// version row ids.
func (s *Service) RenameAssetKey(assetID int32, newKey string) (*apigen.Asset, error) {
	if !ValidAssetKey(newKey) {
		return nil, ErrAssetKeyInvalid
	}
	ctx := context.Background()

	s.Mu.Lock()
	defer s.Mu.Unlock()

	prev, ok := s.mustLatestAssetEventLocked(ctx, assetID)
	if !ok {
		return nil, ErrAssetNotFound
	}
	if prev.Key != newKey {
		if s.assetSiblingKeyTakenLocked(ctx, s.q, prev.SpaceID, prev.AssetDirectoryID, newKey, prev.AssetID, 0) {
			return nil, ErrAssetAlreadyExists
		}
		event := nextAssetEvent(prev, 0, pq.EventUpdate)
		event.Key = newKey
		s.appendAssetEventLocked(ctx, event)
	}
	asset, ok := s.GetAsset(assetID)
	if !ok {
		return nil, ErrAssetNotFound
	}
	return asset, nil
}

// MoveAssetDirectory moves an asset to another directory (0 = the space root)
// in its own space. Content version rows, ids, and content are untouched.
func (s *Service) MoveAssetDirectory(assetID, newDirectoryID int32) (Asset, error) {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	prev, ok := s.mustLatestAssetEventLocked(ctx, assetID)
	if !ok {
		return Asset{}, ErrAssetNotFound
	}
	dirID := int64(newDirectoryID)
	current := Asset{ID: prev.AssetID, Key: prev.Key, SpaceID: prev.SpaceID, AssetDirectoryID: prev.AssetDirectoryID, CreatedAt: prev.CreatedTime}
	if prev.AssetDirectoryID == dirID {
		return current, nil
	}
	if dirID != 0 {
		dir, err := s.q.GetAssetDirectoryByID(ctx, dirID)
		if errors.Is(err, sql.ErrNoRows) {
			return Asset{}, ErrDirectoryNotFound
		}
		if err != nil {
			panic(fmt.Sprintf("GetAssetDirectoryByID: %v", err))
		}
		if dir.SpaceID != prev.SpaceID {
			return Asset{}, ErrSpaceMoveUnsupported
		}
	}
	if s.assetSiblingKeyTakenLocked(ctx, s.q, prev.SpaceID, dirID, prev.Key, prev.AssetID, 0) {
		return Asset{}, ErrAssetAlreadyExists
	}
	event := nextAssetEvent(prev, 0, pq.EventUpdate)
	event.AssetDirectoryID = dirID
	s.appendAssetEventLocked(ctx, event)
	current.AssetDirectoryID = dirID
	return current, nil
}

// MoveAssetSpace moves an asset to another space, landing it in
// newDirectoryID there (0 = the destination space's root). Content version
// rows, ids, and content are untouched, so every pinned mount and reference
// survives. A space change bumps the space facet with author as the acting
// user; a directory-only call appends no space history. Reference locality is
// the caller's law — the handler refuses the move while anything outside the
// destination space references the asset.
func (s *Service) MoveAssetSpaceLocked(assetID, newSpaceID, newDirectoryID, author int32) error {
	ctx := context.Background()

	prev, ok := s.mustLatestAssetEventLocked(ctx, assetID)
	if !ok {
		return ErrAssetNotFound
	}
	spaceID := int64(normalizedUserSpaceID(newSpaceID))
	dirID := int64(newDirectoryID)
	if spaceID == prev.SpaceID && dirID == prev.AssetDirectoryID {
		return nil
	}
	if dirID != 0 {
		dir, err := s.q.GetAssetDirectoryByID(ctx, dirID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDirectoryNotFound
		}
		if err != nil {
			panic(fmt.Sprintf("GetAssetDirectoryByID: %v", err))
		}
		// A directory in any space but the destination reads as absent, matching
		// the create path's treatment of foreign-space directories.
		if dir.SpaceID != spaceID {
			return ErrDirectoryNotFound
		}
	}
	if s.assetSiblingKeyTakenLocked(ctx, s.q, spaceID, dirID, prev.Key, prev.AssetID, 0) {
		return ErrAssetAlreadyExists
	}
	event := nextAssetEvent(prev, author, pq.EventUpdate)
	event.AssetDirectoryID = dirID
	if spaceID != prev.SpaceID {
		event.SpaceID = spaceID
		event.SpaceVersion = prev.SpaceVersion + 1
		event.SpaceChanged = 1
	}
	s.appendAssetEventLocked(ctx, event)
	return nil
}

// DeleteAsset appends the terminal delete event. Content version rows, space
// history, and content stay in the log, so pinned references keep resolving;
// current-state reads exclude the asset from here on and the key is freed.
func (s *Service) DeleteAssetLocked(assetID int32) {
	ctx := context.Background()
	prev, ok := s.mustLatestAssetEventLocked(ctx, assetID)
	if !ok {
		return
	}
	s.appendAssetEventLocked(ctx, nextAssetEvent(prev, 0, pq.EventDelete))
}
