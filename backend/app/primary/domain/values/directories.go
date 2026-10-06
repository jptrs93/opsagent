package values

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

const directoryType = apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY

func ListDirectories(q *pq.Queries) []*apigen.ValueDirectory {
	return erru.Must(q.ListValueDirectories(context.Background()))
}

func DirectoryMeta(q *pq.Queries, directoryID uint64) (*apigen.ValueDirectory, bool) {
	row, err := q.GetValueDirectoryByID(context.Background(), directoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetValueDirectoryByID: %v", err))
	}
	return row, true
}

func directoryUpdate(seq int64, author int64, verb apigen.AuthzVerb, d *apigen.ValueDirectory) *state.WriteUpdate {
	return pq.NewUpdate(pq.ValueDirectoryMutation(WriteMeta(seq, nowMillis(), author, verb), d))
}

func CreateDirectory(store *state.Service, spaceID, parentID uint64, name string, author int64) (*apigen.ValueDirectory, error) {
	if !ValidName(name) {
		return nil, ErrNameInvalid
	}
	ctx := context.Background()
	space := nodes.NormalizedUserSpaceID(spaceID)
	var d *apigen.ValueDirectory
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if parentID != 0 {
			p, err := GetDirectory(ctx, q, parentID)
			if err != nil {
				return nil, err
			}
			if p.SpaceID != space {
				return nil, ErrDirectoryNotFound
			}
		}
		if err := requireNameFree(ctx, q, space, parentID, name, 0, 0); err != nil {
			return nil, err
		}
		id, err := q.NextEntityID(ctx, directoryType)
		if err != nil {
			return nil, err
		}
		d = &apigen.ValueDirectory{ID: id, SpaceID: space, Key: name, ParentID: DirectoryRef(parentID)}
		return directoryUpdate(seq, author, apigen.AuthzVerb_AUTHZ_VERB_CREATE, d), nil
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

func RenameDirectory(store *state.Service, directoryID uint64, newName string, author int64) (*apigen.ValueDirectory, error) {
	if !ValidName(newName) {
		return nil, ErrNameInvalid
	}
	ctx := context.Background()
	var d *apigen.ValueDirectory
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		var err error
		d, err = GetDirectory(ctx, q, directoryID)
		if err != nil {
			return nil, err
		}
		if d.Key == newName {
			return nil, nil
		}
		if err := requireNameFree(ctx, q, d.SpaceID, DirectoryID(d.ParentID), newName, directoryType, d.ID); err != nil {
			return nil, err
		}
		d.Key = newName
		return directoryUpdate(seq, author, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, d), nil
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

func MoveDirectorySpace(store *state.Service, directoryID, newSpaceID uint64) error {
	d, err := GetDirectory(context.Background(), store.Queries(), directoryID)
	if err != nil {
		return err
	}
	if nodes.NormalizedUserSpaceID(newSpaceID) == d.SpaceID {
		return nil
	}
	return ErrSpaceMoveUnsupported
}

func MoveDirectory(store *state.Service, directoryID, newParentID uint64, author int64) (*apigen.ValueDirectory, error) {
	ctx := context.Background()
	var d *apigen.ValueDirectory
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		var err error
		d, err = GetDirectory(ctx, q, directoryID)
		if err != nil {
			return nil, err
		}
		if DirectoryID(d.ParentID) == newParentID {
			return nil, nil
		}
		for cur := newParentID; cur != 0; {
			if cur == d.ID {
				return nil, ErrDirectoryCycle
			}
			p, err := GetDirectory(ctx, q, cur)
			if err != nil {
				return nil, err
			}
			if p.SpaceID != d.SpaceID {
				return nil, ErrSpaceMoveUnsupported
			}
			cur = DirectoryID(p.ParentID)
		}
		if err := requireNameFree(ctx, q, d.SpaceID, newParentID, d.Key, directoryType, d.ID); err != nil {
			return nil, err
		}
		d.ParentID = DirectoryRef(newParentID)
		return directoryUpdate(seq, author, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, d), nil
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

func DeleteDirectory(store *state.Service, directoryID uint64, author int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		d, err := GetDirectory(ctx, q, directoryID)
		if err != nil {
			return nil, err
		}
		children, err := q.CountValueKeysUnder(ctx, d.ID)
		if err != nil {
			return nil, err
		}
		if children > 0 {
			return nil, ErrDirectoryNotEmpty
		}
		return pq.NewUpdate(pq.DeleteMutation(WriteMeta(seq, nowMillis(), author, apigen.AuthzVerb_AUTHZ_VERB_DELETE), directoryType, d.ID)), nil
	})
}
