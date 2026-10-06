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
	ErrAlreadyExists        = errors.New("value key already exists")
	ErrNameInvalid          = errors.New("value key is not a valid file name")
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

// NameTaken reports whether key under a directory belongs to a secret,
// config, or directory other than the entity given by self and selfID.
func NameTaken(ctx context.Context, q *pq.Queries, spaceID, directoryID uint64, key string, self apigen.CoreEntityType, selfID uint64) (bool, error) {
	return q.ValueKeyTaken(ctx, spaceID, directoryID, key, self, selfID)
}

func requireNameFree(ctx context.Context, q *pq.Queries, spaceID, directoryID uint64, key string, self apigen.CoreEntityType, selfID uint64) error {
	taken, err := NameTaken(ctx, q, spaceID, directoryID, key, self, selfID)
	if err != nil {
		return err
	}
	if taken {
		return ErrAlreadyExists
	}
	return nil
}

// WriteMeta is the envelope of one value write.
func WriteMeta(seq, now int64, author int64, verb apigen.AuthzVerb) pq.EventMeta {
	return pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: author, EventType: verb}
}

func nowMillis() int64 { return time.Now().UnixMilli() }

// DirectoryRef is the wire form of a directory id: absent for the space root.
func DirectoryRef(id uint64) apigen.Maybe[uint64] {
	if id == 0 {
		return apigen.Maybe[uint64]{}
	}
	return apigen.Some(id)
}

// DirectoryID is the directory a wire reference names, 0 for the space root.
func DirectoryID(ref apigen.Maybe[uint64]) uint64 {
	if !ref.Present {
		return 0
	}
	return ref.Value
}

func GetDirectory(ctx context.Context, q *pq.Queries, id uint64) (*apigen.ValueDirectory, error) {
	d, err := q.GetValueDirectoryByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDirectoryNotFound
	}
	return d, err
}

func ResolveDirectory(ctx context.Context, q *pq.Queries, spaceID uint64, directoryID uint64) (uint64, error) {
	if directoryID == 0 {
		return 0, nil
	}
	d, err := GetDirectory(ctx, q, directoryID)
	if err != nil {
		return 0, err
	}
	if d.SpaceID != spaceID {
		return 0, ErrDirectoryNotFound
	}
	return directoryID, nil
}
