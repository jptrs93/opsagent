package values

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

var (
	ErrNotFound             = errors.New("value not found")
	ErrAlreadyExists        = errors.New("value name already exists")
	ErrNameInvalid          = errors.New("value name is not a valid file name")
	ErrDirectoryNotFound    = errors.New("value directory not found")
	ErrDirectoryNotEmpty    = errors.New("value directory is not empty")
	ErrDirectoryCycle       = errors.New("value directory cannot be moved inside itself")
	ErrSpaceMoveUnsupported = errors.New("moving between spaces is not supported")
)

func ValidName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return false
	}
	return !strings.ContainsAny(name, "/\\\x00")
}

func SiblingNameTaken(ctx context.Context, q *pq.Queries, spaceID, directoryID int64, name string, excludeSecretID, excludeConfigID, excludeDirectoryID int64) (bool, error) {
	secretCount, err := q.CountSecretSiblingsWithName(ctx, pq.CountSecretSiblingsWithNameParams{
		SpaceID: spaceID, ValueDirectoryID: directoryID, Name: name, ID: excludeSecretID,
	})
	if err != nil {
		return false, err
	}
	if secretCount > 0 {
		return true, nil
	}
	configCount, err := q.CountConfigSiblingsWithName(ctx, pq.CountConfigSiblingsWithNameParams{
		SpaceID: spaceID, ValueDirectoryID: directoryID, Name: name, ID: excludeConfigID,
	})
	if err != nil {
		return false, err
	}
	if configCount > 0 {
		return true, nil
	}
	dirCount, err := q.CountValueDirectorySiblingsWithName(ctx, pq.CountValueDirectorySiblingsWithNameParams{
		SpaceID: spaceID, ParentID: directoryID, Name: name, ID: excludeDirectoryID,
	})
	if err != nil {
		return false, err
	}
	return dirCount > 0, nil
}

func requireNameFree(ctx context.Context, q *pq.Queries, spaceID, directoryID int64, name string, excludeSecretID, excludeConfigID, excludeDirectoryID int64) error {
	taken, err := SiblingNameTaken(ctx, q, spaceID, directoryID, name, excludeSecretID, excludeConfigID, excludeDirectoryID)
	if err != nil {
		return err
	}
	if taken {
		return ErrAlreadyExists
	}
	return nil
}

func GetDirectory(ctx context.Context, q *pq.Queries, id int64) (*apigen.ValueDirectory, error) {
	d, err := q.GetValueDirectoryByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDirectoryNotFound
	}
	return d, err
}

func ResolveDirectory(ctx context.Context, q *pq.Queries, spaceID int64, directoryID int32) (int64, error) {
	dirID := int64(directoryID)
	if dirID == 0 {
		return 0, nil
	}
	d, err := GetDirectory(ctx, q, dirID)
	if err != nil {
		return 0, err
	}
	if int64(d.SpaceID) != spaceID {
		return 0, ErrDirectoryNotFound
	}
	return dirID, nil
}
