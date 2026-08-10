package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

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
