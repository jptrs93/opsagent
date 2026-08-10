package primarydb

// Secrets and configs share ONE file system per space: a name must be unique
// among sibling secrets, configs, and value_directories under the same parent
// directory. The law spans three tables, so it cannot be a SQL constraint —
// every create/rename/move goes through the storage mutex and the check here.

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrValueNotFound      = errors.New("value not found")
	ErrValueAlreadyExists = errors.New("value name already exists")
	ErrValueNameInvalid   = errors.New("value name is not a valid file name")
)

// ValidValueName reports whether name can be a file name in the shared
// secrets/configs namespace. Same rule as asset keys: path separators are
// excluded because the full path is the join of the directory ancestry and
// the name.
func ValidValueName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return false
	}
	return !strings.ContainsAny(name, "/\\\x00")
}

// valueSiblingNameTakenLocked reports whether name is already used by another
// secret, config, or directory under (spaceID, directoryID). Caller must hold
// s.Mu: the law spans three tables, so only the mutex makes the
// check-and-write atomic. excludeSecretID/excludeConfigID exempt the row being
// renamed (0 = exempt nothing).
func (s *Storage) valueSiblingNameTakenLocked(ctx context.Context, q *Queries, spaceID, directoryID int64, name string, excludeSecretID, excludeConfigID int64) bool {
	secretCount, err := q.CountSecretSiblingsWithName(ctx, CountSecretSiblingsWithNameParams{
		SpaceID:          spaceID,
		ValueDirectoryID: directoryID,
		Name:             name,
		ID:               excludeSecretID,
	})
	if err != nil {
		panic(fmt.Sprintf("CountSecretSiblingsWithName: %v", err))
	}
	if secretCount > 0 {
		return true
	}
	configCount, err := q.CountConfigSiblingsWithName(ctx, CountConfigSiblingsWithNameParams{
		SpaceID:          spaceID,
		ValueDirectoryID: directoryID,
		Name:             name,
		ID:               excludeConfigID,
	})
	if err != nil {
		panic(fmt.Sprintf("CountConfigSiblingsWithName: %v", err))
	}
	if configCount > 0 {
		return true
	}
	dirCount, err := q.CountValueDirectorySiblingsWithName(ctx, CountValueDirectorySiblingsWithNameParams{
		SpaceID:  spaceID,
		ParentID: directoryID,
		Name:     name,
	})
	if err != nil {
		panic(fmt.Sprintf("CountValueDirectorySiblingsWithName: %v", err))
	}
	return dirCount > 0
}
