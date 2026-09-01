package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

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
	rows, err := s.q.ListAssetRows(ctx)
	if err != nil {
		panic(fmt.Sprintf("ListAssetRows: %v", err))
	}
	joined, err := s.q.ListAssetVersionsJoined(ctx)
	if err != nil {
		panic(fmt.Sprintf("ListAssetVersionsJoined: %v", err))
	}
	spaceRows, err := s.q.ListAssetSpaceRows(ctx)
	if err != nil {
		panic(fmt.Sprintf("ListAssetSpaceRows: %v", err))
	}
	versions := make(map[int64][]pq.AssetVersionJoined, len(rows))
	for _, v := range joined {
		versions[v.Version.AssetID] = append(versions[v.Version.AssetID], v)
	}
	spaces := make(map[int64][]pq.AssetSpace, len(rows))
	for _, sp := range spaceRows {
		spaces[sp.AssetID] = append(spaces[sp.AssetID], sp)
	}
	out := make([]*apigen.Asset, 0, len(rows))
	for _, r := range rows {
		vs := versions[r.ID]
		if len(vs) == 0 {
			continue
		}
		out = append(out, assetFromParts(r, spaces[r.ID], vs))
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

// GetAssetRow returns the stable asset identity row.
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
	versions, err := s.q.ListAssetVersionsOfAsset(context.Background(), a.ID)
	if err != nil {
		panic(fmt.Sprintf("ListAssetVersionsOfAsset: %v", err))
	}
	if len(versions) == 0 {
		return nil, false
	}
	spaces, err := s.q.ListAssetSpaceRowsByAssetID(context.Background(), a.ID)
	if err != nil {
		panic(fmt.Sprintf("ListAssetSpaceRowsByAssetID: %v", err))
	}
	return assetFromParts(a, spaces, versions), true
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
	rows, err := s.q.ListAssetVersionsOfAsset(context.Background(), int64(assetID))
	if err != nil {
		panic(fmt.Sprintf("ListAssetVersionsOfAsset: %v", err))
	}
	return rows
}

// assetSiblingKeyTakenLocked reports whether key is already used by another
// asset or a directory under (spaceID, directoryID). Caller must hold s.Mu:
// path uniqueness spans two tables, so only the mutex makes the check-and-write
// atomic. excludeAssetID/excludeDirectoryID exempt the row being renamed or
// moved (0 = exclude nothing).
func (s *Service) assetSiblingKeyTakenLocked(ctx context.Context, q *pq.Queries, spaceID, directoryID int64, key string, excludeAssetID, excludeDirectoryID int64) bool {
	assets, err := q.CountAssetSiblingsWithKey(ctx, pq.CountAssetSiblingsWithKeyParams{
		SpaceID:          spaceID,
		AssetDirectoryID: directoryID,
		Key:              key,
		ID:               excludeAssetID,
	})
	if err != nil {
		panic(fmt.Sprintf("CountAssetSiblingsWithKey: %v", err))
	}
	if assets > 0 {
		return true
	}
	dirs, err := q.CountDirectorySiblingsWithKey(ctx, pq.CountDirectorySiblingsWithKeyParams{
		SpaceID:  spaceID,
		ParentID: directoryID,
		Key:      key,
		ID:       excludeDirectoryID,
	})
	if err != nil {
		panic(fmt.Sprintf("CountDirectorySiblingsWithKey: %v", err))
	}
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
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			panic(fmt.Sprintf("NextGlobalSeq: %v", err))
		}
		id, err := q.InsertAssetRow(ctx, pq.InsertAssetRowParams{
			Key:              key,
			AssetDirectoryID: dirID,
			CreatedAt:        now,
		})
		if err != nil {
			panic(fmt.Sprintf("InsertAssetRow: %v", err))
		}
		if err := q.InsertAssetSpaceRow(ctx, pq.InsertAssetSpaceRowParams{
			AssetID:   id,
			Author:    int64(author),
			CreatedAt: now,
			SpaceID:   space,
			GlobalSeq: seq,
		}); err != nil {
			panic(fmt.Sprintf("InsertAssetSpaceRow: %v", err))
		}
		assetID = id
		if _, err := q.InsertAssetVersion(ctx, pq.InsertAssetVersionParams{
			AssetID:   id,
			Version:   1,
			CreatedAt: now,
			Author:    int64(author),
			SizeBytes: sizeBytes,
			Sha256:    sha256,
			GlobalSeq: seq,
		}); err != nil {
			panic(fmt.Sprintf("InsertAssetVersion: %v", err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("asset create tx: %v", err))
	}
	asset, ok := s.GetAsset(int32(assetID))
	if !ok {
		panic(fmt.Sprintf("created asset %d not readable", assetID))
	}
	return asset, nil
}

// AppendAssetVersion appends the next version of an existing asset. The asset
// identity — key, space, directory — is untouched. The content must already be
// in the asset store under sha256.
func (s *Service) AppendAssetVersion(assetID, author int32, sha256 string, sizeBytes int64) (*apigen.Asset, error) {
	ctx := context.Background()
	now := time.Now().UnixMilli()

	s.Mu.Lock()
	defer s.Mu.Unlock()

	if _, err := s.assetStoreRefBySha(ctx, sha256); err != nil {
		return nil, err
	}
	a, err := s.q.GetAssetByID(ctx, int64(assetID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAssetNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetByID: %v", err))
	}
	version, err := s.q.GetNextAssetVersionNumber(ctx, a.ID)
	if err != nil {
		panic(fmt.Sprintf("GetNextAssetVersionNumber: %v", err))
	}
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		_, err = q.InsertAssetVersion(ctx, pq.InsertAssetVersionParams{
			AssetID:   a.ID,
			Version:   version,
			CreatedAt: now,
			Author:    int64(author),
			SizeBytes: sizeBytes,
			Sha256:    sha256,
			GlobalSeq: seq,
		})
		return err
	}); err != nil {
		panic(fmt.Sprintf("InsertAssetVersion: %v", err))
	}
	asset, ok := s.GetAsset(assetID)
	if !ok {
		panic(fmt.Sprintf("appended asset %d not readable", assetID))
	}
	return asset, nil
}

// RenameAssetKey renames an asset in place. Version rows, ids, and content are
// untouched; deployment configs keep working because they pin version row ids.
func (s *Service) RenameAssetKey(assetID int32, newKey string) (*apigen.Asset, error) {
	if !ValidAssetKey(newKey) {
		return nil, ErrAssetKeyInvalid
	}
	ctx := context.Background()

	s.Mu.Lock()
	defer s.Mu.Unlock()

	a, err := s.q.GetAssetByID(ctx, int64(assetID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAssetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load asset for rename: %w", err)
	}
	if a.Key != newKey {
		if s.assetSiblingKeyTakenLocked(ctx, s.q, a.SpaceID, a.AssetDirectoryID, newKey, a.ID, 0) {
			return nil, ErrAssetAlreadyExists
		}
		if err := s.q.RenameAssetKey(ctx, pq.RenameAssetKeyParams{Key: newKey, ID: a.ID}); err != nil {
			return nil, fmt.Errorf("rename asset: %w", err)
		}
		a.Key = newKey
	}
	asset, ok := s.GetAsset(assetID)
	if !ok {
		return nil, ErrAssetNotFound
	}
	return asset, nil
}

// MoveAssetDirectory moves an asset to another directory (0 = the space root)
// in its own space. Version rows, ids, and content are untouched.
func (s *Service) MoveAssetDirectory(assetID, newDirectoryID int32) (Asset, error) {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	a, err := s.q.GetAssetByID(ctx, int64(assetID))
	if errors.Is(err, sql.ErrNoRows) {
		return Asset{}, ErrAssetNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetByID: %v", err))
	}
	dirID := int64(newDirectoryID)
	if a.AssetDirectoryID == dirID {
		return a, nil
	}
	if dirID != 0 {
		dir, err := s.q.GetAssetDirectoryByID(ctx, dirID)
		if errors.Is(err, sql.ErrNoRows) {
			return Asset{}, ErrDirectoryNotFound
		}
		if err != nil {
			panic(fmt.Sprintf("GetAssetDirectoryByID: %v", err))
		}
		if dir.SpaceID != a.SpaceID {
			return Asset{}, ErrSpaceMoveUnsupported
		}
	}
	if s.assetSiblingKeyTakenLocked(ctx, s.q, a.SpaceID, dirID, a.Key, a.ID, 0) {
		return Asset{}, ErrAssetAlreadyExists
	}
	if err := s.q.SetAssetDirectoryID(ctx, pq.SetAssetDirectoryIDParams{AssetDirectoryID: dirID, ID: a.ID}); err != nil {
		panic(fmt.Sprintf("SetAssetDirectoryID: %v", err))
	}
	a.AssetDirectoryID = dirID
	return a, nil
}

// MoveAssetSpace moves an asset to another space, landing it in
// newDirectoryID there (0 = the destination space's root). Version rows, ids,
// and content are untouched, so every pinned mount and reference survives.
// A space change appends to the asset_spaces log with author as the acting
// user. Reference locality is the caller's law — the handler refuses the move
// while anything outside the destination space references the asset.
func (s *Service) MoveAssetSpace(assetID, newSpaceID, newDirectoryID, author int32) error {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	a, err := s.q.GetAssetByID(ctx, int64(assetID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAssetNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetByID: %v", err))
	}
	spaceID := int64(normalizedUserSpaceID(newSpaceID))
	dirID := int64(newDirectoryID)
	if spaceID == a.SpaceID && dirID == a.AssetDirectoryID {
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
	if s.assetSiblingKeyTakenLocked(ctx, s.q, spaceID, dirID, a.Key, a.ID, 0) {
		return ErrAssetAlreadyExists
	}
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		if spaceID != a.SpaceID {
			seq, err := q.NextGlobalSeq(ctx)
			if err != nil {
				panic(fmt.Sprintf("NextGlobalSeq: %v", err))
			}
			if err := q.InsertAssetSpaceRow(ctx, pq.InsertAssetSpaceRowParams{
				AssetID:   a.ID,
				Author:    int64(author),
				CreatedAt: time.Now().UnixMilli(),
				SpaceID:   spaceID,
				GlobalSeq: seq,
			}); err != nil {
				panic(fmt.Sprintf("InsertAssetSpaceRow: %v", err))
			}
		}
		if err := q.SetAssetDirectoryID(ctx, pq.SetAssetDirectoryIDParams{AssetDirectoryID: dirID, ID: a.ID}); err != nil {
			panic(fmt.Sprintf("SetAssetDirectoryID: %v", err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("asset space move tx: %v", err))
	}
	return nil
}

// DeleteAsset soft-deletes the asset identity. Version rows, space history,
// and content stay in place, so the delete is recoverable at the DB level;
// reads exclude the asset from here on.
func (s *Service) DeleteAsset(assetID int32) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := s.q.SoftDeleteAssetRow(context.Background(), pq.SoftDeleteAssetRowParams{
		DeletedAt: time.Now().UnixMilli(),
		ID:        int64(assetID),
	}); err != nil {
		panic(fmt.Sprintf("SoftDeleteAssetRow: %v", err))
	}
}
