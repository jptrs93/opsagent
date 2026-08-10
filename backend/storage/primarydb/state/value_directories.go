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
	ErrValueDirectoryNotFound = errors.New("value directory not found")
	ErrValueDirectoryNotEmpty = errors.New("value directory is not empty")
	ErrValueDirectoryCycle    = errors.New("value directory cannot be moved inside itself")
)

// CreateValueDirectory creates a directory under parentID (0 = the space root)
// in the shared secrets/configs tree of spaceID. Directories share the sibling
// namespace with secrets and configs, so the name must be free in all three
// tables.
func (s *Service) CreateValueDirectory(spaceID, parentID int32, name string, createdBy int32) (ValueDirectory, error) {
	if !ValidValueName(name) {
		return ValueDirectory{}, ErrValueNameInvalid
	}
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	space := int64(normalizedUserSpaceID(spaceID))
	parent := int64(parentID)
	if parent != 0 {
		p, err := s.q.GetValueDirectoryByID(ctx, parent)
		if errors.Is(err, sql.ErrNoRows) {
			return ValueDirectory{}, ErrValueDirectoryNotFound
		}
		if err != nil {
			panic(fmt.Sprintf("GetValueDirectoryByID: %v", err))
		}
		// A parent in another space does not exist from this space's viewpoint.
		if p.SpaceID != space {
			return ValueDirectory{}, ErrValueDirectoryNotFound
		}
	}
	if s.valueSiblingNameTakenLocked(ctx, s.q, space, parent, name, 0, 0, 0) {
		return ValueDirectory{}, ErrValueAlreadyExists
	}
	d, err := s.q.InsertValueDirectory(ctx, pq.InsertValueDirectoryParams{
		SpaceID:   space,
		Name:      name,
		ParentID:  parent,
		CreatedAt: time.Now().UnixMilli(),
		CreatedBy: int64(createdBy),
	})
	if err != nil {
		panic(fmt.Sprintf("InsertValueDirectory: %v", err))
	}
	return d, nil
}

// MoveValueDirectory reparents a directory (0 = the space root). Children
// reference their parent by id, so the whole subtree moves with it and nothing
// else changes.
func (s *Service) MoveValueDirectory(directoryID, newParentID int32) (ValueDirectory, error) {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	d, err := s.q.GetValueDirectoryByID(ctx, int64(directoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return ValueDirectory{}, ErrValueDirectoryNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetValueDirectoryByID: %v", err))
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
			return ValueDirectory{}, ErrValueDirectoryCycle
		}
		p, err := s.q.GetValueDirectoryByID(ctx, cur)
		if errors.Is(err, sql.ErrNoRows) {
			return ValueDirectory{}, ErrValueDirectoryNotFound
		}
		if err != nil {
			panic(fmt.Sprintf("GetValueDirectoryByID: %v", err))
		}
		if p.SpaceID != d.SpaceID {
			return ValueDirectory{}, ErrSpaceMoveUnsupported
		}
		cur = p.ParentID
	}
	if s.valueSiblingNameTakenLocked(ctx, s.q, d.SpaceID, parent, d.Name, 0, 0, d.ID) {
		return ValueDirectory{}, ErrValueAlreadyExists
	}
	if err := s.q.SetValueDirectoryParent(ctx, pq.SetValueDirectoryParentParams{ParentID: parent, ID: d.ID}); err != nil {
		panic(fmt.Sprintf("SetValueDirectoryParent: %v", err))
	}
	d.ParentID = parent
	return d, nil
}

// DeleteValueDirectory removes an empty directory. Non-empty directories are
// rejected rather than cascaded so namespace edits can never take secret or
// config content with them.
func (s *Service) DeleteValueDirectory(directoryID int32) error {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	d, err := s.q.GetValueDirectoryByID(ctx, int64(directoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrValueDirectoryNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetValueDirectoryByID: %v", err))
	}
	secretCount, err := s.q.CountSecretsInDirectory(ctx, d.ID)
	if err != nil {
		panic(fmt.Sprintf("CountSecretsInDirectory: %v", err))
	}
	configCount, err := s.q.CountConfigsInDirectory(ctx, d.ID)
	if err != nil {
		panic(fmt.Sprintf("CountConfigsInDirectory: %v", err))
	}
	children, err := s.q.CountChildValueDirectories(ctx, d.ID)
	if err != nil {
		panic(fmt.Sprintf("CountChildValueDirectories: %v", err))
	}
	if secretCount > 0 || configCount > 0 || children > 0 {
		return ErrValueDirectoryNotEmpty
	}
	if err := s.q.DeleteValueDirectory(ctx, d.ID); err != nil {
		panic(fmt.Sprintf("DeleteValueDirectory: %v", err))
	}
	return nil
}
