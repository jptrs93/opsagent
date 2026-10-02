package values

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

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

// NameTaken reports whether name under a directory belongs to a secret,
// config, or directory other than the entity given by self and selfID.
func NameTaken(ctx context.Context, q *pq.Queries, spaceID, directoryID int64, name string, self apigen.CoreEntityType, selfID int64) (bool, error) {
	return q.ValueNameTaken(ctx, spaceID, directoryID, name, self, selfID)
}

func requireNameFree(ctx context.Context, q *pq.Queries, spaceID, directoryID int64, name string, self apigen.CoreEntityType, selfID int64) error {
	taken, err := NameTaken(ctx, q, spaceID, directoryID, name, self, selfID)
	if err != nil {
		return err
	}
	if taken {
		return ErrAlreadyExists
	}
	return nil
}

// WriteMeta is the envelope of one value write.
func WriteMeta(seq, now int64, author int32, verb apigen.AuthzVerb) pq.EventMeta {
	return pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: int64(author), EventType: verb}
}

func nowMillis() int64 { return time.Now().UnixMilli() }

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
