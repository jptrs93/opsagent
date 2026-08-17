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

func (s *Service) ListAssets() []*apigen.AssetMeta {
	rows, err := s.q.ListAssetRows(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListAssetRows: %v", err))
	}
	joined, err := s.q.ListAssetVersionsJoined(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListAssetVersionsJoined: %v", err))
	}
	// The query orders by version ASC, so each asset's slice reverses to newest
	// first.
	versionRefs := make(map[int64][]*apigen.AssetVersionMeta, len(rows))
	for _, v := range joined {
		versionRefs[v.Version.AssetID] = append(versionRefs[v.Version.AssetID], assetVersionMetaFromJoined(v))
	}
	out := make([]*apigen.AssetMeta, 0, len(rows))
	for _, r := range rows {
		refs := versionRefs[r.ID]
		if len(refs) == 0 {
			continue
		}
		for i, j := 0, len(refs)-1; i < j; i, j = i+1, j-1 {
			refs[i], refs[j] = refs[j], refs[i]
		}
		out = append(out, assetMetaFromRow(r, refs))
	}
	return out
}

// ListAllAssetVersions returns every version row across all assets, joined
// with its owning asset for display fields. Inline blobs are not loaded.
func (s *Service) ListAllAssetVersions() []*apigen.AssetVersion {
	rows, err := s.q.ListAssetVersionsJoined(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListAssetVersionsJoined: %v", err))
	}
	out := []*apigen.AssetVersion{}
	for _, r := range rows {
		out = append(out, assetVersionFromJoined(r.Asset, r))
	}
	return out
}

func (s *Service) NotifyAssetUpdate(meta *apigen.AssetMeta) {
	if meta == nil || (meta.ID == 0 && meta.Key == "") {
		return
	}
	s.assetSubs.Notify(*meta)
}

func (s *Service) NotifyAssetDeleted(meta *apigen.AssetMeta) {
	if meta == nil || (meta.ID == 0 && meta.Key == "") {
		return
	}
	cp := *meta
	cp.Deleted = true
	s.assetSubs.Notify(cp)
}

func (s *Service) SubscribeAssetUpdates() (*pubsubu.Sub[apigen.AssetMeta], func()) {
	sub := s.assetSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
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

// GetAssetMeta returns the asset with its version index, or false when the
// asset does not exist or has no version.
func (s *Service) GetAssetMeta(assetID int32) (*apigen.AssetMeta, bool) {
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
	refs := make([]*apigen.AssetVersionMeta, 0, len(versions))
	for i := len(versions) - 1; i >= 0; i-- { // query is oldest first; refs are newest first
		refs = append(refs, assetVersionMetaFromJoined(versions[i]))
	}
	return assetMetaFromRow(a, refs), true
}

// GetAssetInRootByKey resolves an asset by key in a space's implicit root
// directory.
func (s *Service) GetAssetInRootByKey(spaceID int32, key string) (Asset, bool) {
	return s.GetAssetInDirectory(spaceID, 0, key)
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

// GetAssetVersion returns one version of an asset; version 0 means latest.
func (s *Service) GetAssetVersion(assetID, version int32) (*apigen.AssetVersion, bool) {
	a, ok := s.GetAssetRow(assetID)
	if !ok {
		return nil, false
	}
	v, err := s.q.GetAssetVersionJoinedByNumber(context.Background(), a.ID, int64(version))
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetVersionJoinedByNumber: %v", err))
	}
	return assetVersionFromJoined(a, v), true
}

// GetAssetVersionByID resolves a version row id — the id deployment configs
// pin and workers fetch by.
func (s *Service) GetAssetVersionByID(assetVersionID int32) (*apigen.AssetVersion, bool) {
	r, ok := s.GetAssetVersionJoined(assetVersionID)
	if !ok {
		return nil, false
	}
	return assetVersionFromJoined(r.Asset, r), true
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

// AssetVersionFromJoined converts a raw joined row to the wire shape.
func AssetVersionFromJoined(a Asset, r AssetVersionJoined) *apigen.AssetVersion {
	return assetVersionFromJoined(a, r)
}

// ListAssetVersions returns every version of one asset, oldest first.
func (s *Service) ListAssetVersions(assetID int32) []*apigen.AssetVersion {
	a, ok := s.GetAssetRow(assetID)
	if !ok {
		return nil
	}
	rows, err := s.q.ListAssetVersionsOfAsset(context.Background(), a.ID)
	if err != nil {
		panic(fmt.Sprintf("ListAssetVersionsOfAsset: %v", err))
	}
	out := make([]*apigen.AssetVersion, 0, len(rows))
	for _, v := range rows {
		out = append(out, assetVersionFromJoined(a, v))
	}
	return out
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
func (s *Service) CreateAssetWithVersion(key string, spaceID, directoryID, createdBy int32, sha256 string, sizeBytes int64) (*apigen.AssetVersion, error) {
	if !ValidAssetKey(key) {
		return nil, ErrAssetKeyInvalid
	}
	ctx := context.Background()
	now := time.Now().UnixMilli()
	space := int64(normalizedUserSpaceID(spaceID))

	s.Mu.Lock()
	defer s.Mu.Unlock()

	store, err := s.assetStoreRefBySha(ctx, sha256)
	if err != nil {
		return nil, err
	}
	dirID, err := s.resolveAssetDirectoryLocked(ctx, space, directoryID)
	if err != nil {
		return nil, err
	}
	if s.assetSiblingKeyTakenLocked(ctx, s.q, space, dirID, key, 0, 0) {
		return nil, ErrAssetAlreadyExists
	}

	var a Asset
	var v pq.AssetVersion
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		var err error
		a, err = q.InsertAssetRow(ctx, pq.InsertAssetRowParams{
			SpaceID:          space,
			Key:              key,
			AssetDirectoryID: dirID,
			CreatedAt:        now,
			CreatedBy:        int64(createdBy),
		})
		if err != nil {
			panic(fmt.Sprintf("InsertAssetRow: %v", err))
		}
		v, err = q.InsertAssetVersion(ctx, pq.InsertAssetVersionParams{
			AssetID:   a.ID,
			Version:   1,
			CreatedAt: now,
			CreatedBy: int64(createdBy),
			SizeBytes: sizeBytes,
			Sha256:    sha256,
		})
		if err != nil {
			panic(fmt.Sprintf("InsertAssetVersion: %v", err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("asset create tx: %v", err))
	}
	return assetVersionFromJoined(a, pq.AssetVersionJoined{Version: v, Store: store}), nil
}

// AppendAssetVersion appends the next version of an existing asset. The asset
// identity — key, space, directory — is untouched. The content must already be
// in the asset store under sha256.
func (s *Service) AppendAssetVersion(assetID, createdBy int32, sha256 string, sizeBytes int64) (*apigen.AssetVersion, error) {
	ctx := context.Background()
	now := time.Now().UnixMilli()

	s.Mu.Lock()
	defer s.Mu.Unlock()

	store, err := s.assetStoreRefBySha(ctx, sha256)
	if err != nil {
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
	v, err := s.q.InsertAssetVersion(ctx, pq.InsertAssetVersionParams{
		AssetID:   a.ID,
		Version:   version,
		CreatedAt: now,
		CreatedBy: int64(createdBy),
		SizeBytes: sizeBytes,
		Sha256:    sha256,
	})
	if err != nil {
		panic(fmt.Sprintf("InsertAssetVersion: %v", err))
	}
	return assetVersionFromJoined(a, pq.AssetVersionJoined{Version: v, Store: store}), nil
}

// RenameAssetKey renames an asset in place. Version rows, ids, and content are
// untouched; deployment configs keep working because they pin version row ids.
func (s *Service) RenameAssetKey(assetID int32, newKey string) (*apigen.AssetMeta, error) {
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
	versions, err := s.q.ListAssetVersionsOfAsset(ctx, a.ID)
	if err != nil {
		return nil, fmt.Errorf("load renamed asset versions: %w", err)
	}
	if len(versions) == 0 {
		return nil, ErrAssetNotFound
	}
	refs := make([]*apigen.AssetVersionMeta, 0, len(versions))
	for i := len(versions) - 1; i >= 0; i-- { // query is oldest first; refs are newest first
		refs = append(refs, assetVersionMetaFromJoined(versions[i]))
	}
	return assetMetaFromRow(a, refs), nil
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
// Reference locality is the caller's law — the handler refuses the move while
// anything outside the destination space references the asset.
func (s *Service) MoveAssetSpace(assetID, newSpaceID, newDirectoryID int32) error {
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
	if err := s.q.SetAssetSpace(ctx, pq.SetAssetSpaceParams{SpaceID: spaceID, AssetDirectoryID: dirID, ID: a.ID}); err != nil {
		panic(fmt.Sprintf("SetAssetSpace: %v", err))
	}
	return nil
}

// DeleteAsset removes the asset identity and every version row. Content-store
// rows are the caller's to reclaim — other assets may share them by sha.
func (s *Service) DeleteAsset(assetID int32) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := context.Background()
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		if err := q.DeleteAssetVersionsByAssetID(ctx, int64(assetID)); err != nil {
			panic(fmt.Sprintf("DeleteAssetVersionsByAssetID: %v", err))
		}
		if err := q.DeleteAssetRow(ctx, int64(assetID)); err != nil {
			panic(fmt.Sprintf("DeleteAssetRow: %v", err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("asset delete tx: %v", err))
	}
}
