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
	ErrValueDirectoryNotFound = errors.New("value directory not found")
	ErrValueDirectoryNotEmpty = errors.New("value directory is not empty")
	ErrValueDirectoryCycle    = errors.New("value directory cannot be moved inside itself")
)

// CreateValueDirectory creates a directory under parentID (0 = the space root)
// in the shared secrets/configs tree of spaceID. Directories share the sibling
// namespace with secrets and configs, so the name must be free in all three
// tables.
func (s *Service) CreateValueDirectory(spaceID, parentID int32, name string, author int32) (ValueDirectory, error) {
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
		Author:    int64(author),
	})
	if err != nil {
		panic(fmt.Sprintf("InsertValueDirectory: %v", err))
	}
	return d, nil
}

// resolveValueDirectoryLocked validates a caller-supplied target directory for
// a create (0 = the space root). A directory in another space does not exist
// from this space's viewpoint. Caller must hold s.Mu.
func (s *Service) resolveValueDirectoryLocked(ctx context.Context, spaceID int64, directoryID int32) (int64, error) {
	dirID := int64(directoryID)
	if dirID == 0 {
		return 0, nil
	}
	d, err := s.q.GetValueDirectoryByID(ctx, dirID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrValueDirectoryNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetValueDirectoryByID: %v", err))
	}
	if d.SpaceID != spaceID {
		return 0, ErrValueDirectoryNotFound
	}
	return dirID, nil
}

// valueDirectoryToProto builds the wire form of a directory row.
func valueDirectoryToProto(d ValueDirectory) *apigen.ValueDirectory {
	return &apigen.ValueDirectory{
		ID:        int32(d.ID),
		SpaceID:   int32(d.SpaceID),
		Name:      d.Name,
		ParentID:  int32(d.ParentID),
		CreatedAt: time.UnixMilli(d.CreatedAt),
		Author:    int32(d.Author),
	}
}

// ListValueDirectories returns every directory across all spaces, ordered by
// space, parent, then name.
func (s *Service) ListValueDirectories() []*apigen.ValueDirectory {
	rows, err := s.q.ListValueDirectories(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListValueDirectories: %v", err))
	}
	out := make([]*apigen.ValueDirectory, 0, len(rows))
	for _, row := range rows {
		out = append(out, valueDirectoryToProto(row))
	}
	return out
}

// GetValueDirectoryMeta returns one directory in wire form.
func (s *Service) GetValueDirectoryMeta(directoryID int32) (*apigen.ValueDirectory, bool) {
	row, err := s.q.GetValueDirectoryByID(context.Background(), int64(directoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetValueDirectoryByID: %v", err))
	}
	return valueDirectoryToProto(row), true
}

// RenameValueDirectory renames a directory in place. The contents keep their
// directory id, so nothing else moves; only sibling uniqueness is at stake.
func (s *Service) RenameValueDirectory(directoryID int32, newName string) (ValueDirectory, error) {
	if !ValidValueName(newName) {
		return ValueDirectory{}, ErrValueNameInvalid
	}
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
	if d.Name == newName {
		return d, nil
	}
	if s.valueSiblingNameTakenLocked(ctx, s.q, d.SpaceID, d.ParentID, newName, 0, 0, d.ID) {
		return ValueDirectory{}, ErrValueAlreadyExists
	}
	if err := s.q.SetValueDirectoryName(ctx, pq.SetValueDirectoryNameParams{Name: newName, ID: d.ID}); err != nil {
		panic(fmt.Sprintf("SetValueDirectoryName: %v", err))
	}
	d.Name = newName
	return d, nil
}

// MoveValueDirectorySpace would move a directory and its subtree to another
// space. Space moves are not supported yet — the secrets and configs underneath
// carry space-scoped references, so the move needs coordinated handling. A
// same-space target is accepted as a no-op. Mirrors MoveSecretSpace.
func (s *Service) MoveValueDirectorySpace(directoryID, newSpaceID int32) error {
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
	if int64(normalizedUserSpaceID(newSpaceID)) == d.SpaceID {
		return nil
	}
	return ErrSpaceMoveUnsupported
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
