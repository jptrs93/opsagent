package state

// Secrets and configs share ONE file system per space: a name must be unique
// among sibling secrets, configs, and value_directories under the same parent
// directory. The law spans three tables, so it cannot be a SQL constraint —
// every create/rename/move goes through the storage mutex and the check here.

import (
	"context"
	"errors"
	"strings"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

var (
	ErrValueNotFound      = errors.New("value not found")
	ErrValueAlreadyExists = errors.New("value name already exists")
	ErrValueNameInvalid   = errors.New("value name is not a valid file name")
)

func ValidValueName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return false
	}
	return !strings.ContainsAny(name, "/\\\x00")
}

func (s *Service) valueSiblingNameTakenLocked(ctx context.Context, q *pq.Queries, spaceID, directoryID int64, name string, excludeSecretID, excludeConfigID, excludeDirectoryID int64) bool {
	secretCount := erru.Must(q.CountSecretSiblingsWithName(ctx, pq.CountSecretSiblingsWithNameParams{
		SpaceID:          spaceID,
		ValueDirectoryID: directoryID,
		Name:             name,
		ID:               excludeSecretID,
	}))
	if secretCount > 0 {
		return true
	}
	configCount := erru.Must(q.CountConfigSiblingsWithName(ctx, pq.CountConfigSiblingsWithNameParams{
		SpaceID:          spaceID,
		ValueDirectoryID: directoryID,
		Name:             name,
		ID:               excludeConfigID,
	}))
	if configCount > 0 {
		return true
	}
	dirCount := erru.Must(q.CountValueDirectorySiblingsWithName(ctx, pq.CountValueDirectorySiblingsWithNameParams{
		SpaceID:  spaceID,
		ParentID: directoryID,
		Name:     name,
		ID:       excludeDirectoryID,
	}))
	return dirCount > 0
}
