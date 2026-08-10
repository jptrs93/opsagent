package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

var (
	ErrDirectoryNotFound    = errors.New("asset directory not found")
	ErrDirectoryNotEmpty    = errors.New("asset directory is not empty")
	ErrDirectoryCycle       = errors.New("asset directory cannot be moved inside itself")
	ErrSpaceMoveUnsupported = errors.New("moving between spaces is not supported")
)

// CreateDirectory creates a directory under parentID (0 = the space root) in
// spaceID. Directories share the sibling namespace with assets, so the key must
// be free in both tables.
func (s *Service) CreateDirectory(spaceID, parentID int32, key string, createdBy int32) (AssetDirectory, error) {
	if !ValidAssetKey(key) {
		return AssetDirectory{}, ErrAssetKeyInvalid
	}
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	space := int64(normalizedUserSpaceID(spaceID))
	parent := int64(parentID)
	if parent != 0 {
		p, err := s.q.GetAssetDirectoryByID(ctx, parent)
		if errors.Is(err, sql.ErrNoRows) {
			return AssetDirectory{}, ErrDirectoryNotFound
		}
		if err != nil {
			panic(fmt.Sprintf("GetAssetDirectoryByID: %v", err))
		}
		// A parent in another space does not exist from this space's viewpoint.
		if p.SpaceID != space {
			return AssetDirectory{}, ErrDirectoryNotFound
		}
	}
	if s.assetSiblingKeyTakenLocked(ctx, s.q, space, parent, key, 0, 0) {
		return AssetDirectory{}, ErrAssetAlreadyExists
	}
	d, err := s.q.InsertAssetDirectory(ctx, pq.InsertAssetDirectoryParams{
		SpaceID:   space,
		Key:       key,
		ParentID:  parent,
		CreatedAt: time.Now().UnixMilli(),
		CreatedBy: int64(createdBy),
	})
	if err != nil {
		panic(fmt.Sprintf("InsertAssetDirectory: %v", err))
	}
	return d, nil
}

// resolveAssetDirectoryLocked validates a caller-supplied target directory for
// a create (0 = the space root). A directory in another space does not exist
// from this space's viewpoint. Caller must hold s.Mu.
func (s *Service) resolveAssetDirectoryLocked(ctx context.Context, spaceID int64, directoryID int32) (int64, error) {
	dirID := int64(directoryID)
	if dirID == 0 {
		return 0, nil
	}
	d, err := s.q.GetAssetDirectoryByID(ctx, dirID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrDirectoryNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetDirectoryByID: %v", err))
	}
	if d.SpaceID != spaceID {
		return 0, ErrDirectoryNotFound
	}
	return dirID, nil
}

// assetDirectoryToProto builds the wire form of a directory row.
func assetDirectoryToProto(d AssetDirectory) *apigen.AssetDirectory {
	return &apigen.AssetDirectory{
		ID:        int32(d.ID),
		SpaceID:   int32(d.SpaceID),
		Key:       d.Key,
		ParentID:  int32(d.ParentID),
		CreatedAt: time.UnixMilli(d.CreatedAt),
		CreatedBy: int32(d.CreatedBy),
	}
}

// ListAssetDirectories returns every directory across all spaces, ordered by
// space, parent, then key.
func (s *Service) ListAssetDirectories() []*apigen.AssetDirectory {
	rows, err := s.q.ListAssetDirectories(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListAssetDirectories: %v", err))
	}
	out := make([]*apigen.AssetDirectory, 0, len(rows))
	for _, row := range rows {
		out = append(out, assetDirectoryToProto(row))
	}
	return out
}

// GetAssetDirectoryMeta returns one directory in wire form.
func (s *Service) GetAssetDirectoryMeta(directoryID int32) (*apigen.AssetDirectory, bool) {
	row, err := s.q.GetAssetDirectoryByID(context.Background(), int64(directoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetDirectoryByID: %v", err))
	}
	return assetDirectoryToProto(row), true
}

// RenameDirectory renames a directory in place. The contents keep their
// directory id, so nothing else moves; only sibling uniqueness is at stake.
func (s *Service) RenameDirectory(directoryID int32, newKey string) (AssetDirectory, error) {
	if !ValidAssetKey(newKey) {
		return AssetDirectory{}, ErrAssetKeyInvalid
	}
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	d, err := s.q.GetAssetDirectoryByID(ctx, int64(directoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return AssetDirectory{}, ErrDirectoryNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetDirectoryByID: %v", err))
	}
	if d.Key == newKey {
		return d, nil
	}
	if s.assetSiblingKeyTakenLocked(ctx, s.q, d.SpaceID, d.ParentID, newKey, 0, d.ID) {
		return AssetDirectory{}, ErrAssetAlreadyExists
	}
	if err := s.q.SetAssetDirectoryKey(ctx, pq.SetAssetDirectoryKeyParams{Key: newKey, ID: d.ID}); err != nil {
		panic(fmt.Sprintf("SetAssetDirectoryKey: %v", err))
	}
	d.Key = newKey
	return d, nil
}

// MoveDirectory reparents a directory (0 = the space root). Children reference
// their parent by id, so the whole subtree moves with it and nothing else
// changes.
func (s *Service) MoveDirectory(directoryID, newParentID int32) (AssetDirectory, error) {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	d, err := s.q.GetAssetDirectoryByID(ctx, int64(directoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return AssetDirectory{}, ErrDirectoryNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetDirectoryByID: %v", err))
	}
	parent := int64(newParentID)
	if d.ParentID == parent {
		return d, nil
	}
	// Walk the destination's ancestry to the root: hitting the directory itself
	// means the destination is inside its own subtree, which would detach the
	// subtree into a cycle.
	for cur := parent; cur != 0; {
		if cur == d.ID {
			return AssetDirectory{}, ErrDirectoryCycle
		}
		p, err := s.q.GetAssetDirectoryByID(ctx, cur)
		if errors.Is(err, sql.ErrNoRows) {
			return AssetDirectory{}, ErrDirectoryNotFound
		}
		if err != nil {
			panic(fmt.Sprintf("GetAssetDirectoryByID: %v", err))
		}
		if p.SpaceID != d.SpaceID {
			return AssetDirectory{}, ErrSpaceMoveUnsupported
		}
		cur = p.ParentID
	}
	if s.assetSiblingKeyTakenLocked(ctx, s.q, d.SpaceID, parent, d.Key, 0, d.ID) {
		return AssetDirectory{}, ErrAssetAlreadyExists
	}
	if err := s.q.SetAssetDirectoryParent(ctx, pq.SetAssetDirectoryParentParams{ParentID: parent, ID: d.ID}); err != nil {
		panic(fmt.Sprintf("SetAssetDirectoryParent: %v", err))
	}
	d.ParentID = parent
	return d, nil
}

// DeleteDirectory removes an empty directory. Non-empty directories are
// rejected rather than cascaded so namespace edits can never take asset
// content with them.
func (s *Service) DeleteDirectory(directoryID int32) error {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	d, err := s.q.GetAssetDirectoryByID(ctx, int64(directoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDirectoryNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetDirectoryByID: %v", err))
	}
	assets, err := s.q.CountAssetsInDirectory(ctx, d.ID)
	if err != nil {
		panic(fmt.Sprintf("CountAssetsInDirectory: %v", err))
	}
	children, err := s.q.CountChildAssetDirectories(ctx, d.ID)
	if err != nil {
		panic(fmt.Sprintf("CountChildAssetDirectories: %v", err))
	}
	if assets > 0 || children > 0 {
		return ErrDirectoryNotEmpty
	}
	if err := s.q.DeleteAssetDirectory(ctx, d.ID); err != nil {
		panic(fmt.Sprintf("DeleteAssetDirectory: %v", err))
	}
	return nil
}
