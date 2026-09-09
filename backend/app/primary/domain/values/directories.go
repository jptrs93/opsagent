package values

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func ListDirectories(q *pq.Queries) []*apigen.ValueDirectory {
	rows := erru.Must(q.ListValueDirectories(context.Background()))
	if rows == nil {
		rows = []*apigen.ValueDirectory{}
	}
	return rows
}

func DirectoryMeta(q *pq.Queries, directoryID int32) (*apigen.ValueDirectory, bool) {
	row, err := q.GetValueDirectoryByID(context.Background(), int64(directoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetValueDirectoryByID: %v", err))
	}
	return row, true
}

func directoryUpdate(d *apigen.ValueDirectory) *state.Update {
	return &apigen.CoreUpdate{ValueDirectories: []*apigen.ValueDirectory{d}}
}

func CreateDirectory(store *state.Service, spaceID, parentID int32, name string, author int32) (*apigen.ValueDirectory, error) {
	if !ValidName(name) {
		return nil, ErrNameInvalid
	}
	ctx := context.Background()
	space := int64(nodes.NormalizedUserSpaceID(spaceID))
	parent := int64(parentID)
	var d *apigen.ValueDirectory
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		if parent != 0 {
			p, err := GetDirectory(ctx, q, parent)
			if err != nil {
				return nil, err
			}
			if int64(p.SpaceID) != space {
				return nil, ErrDirectoryNotFound
			}
		}
		if err := requireNameFree(ctx, q, space, parent, name, 0, 0, 0); err != nil {
			return nil, err
		}
		var err error
		d, err = q.InsertValueDirectory(ctx, pq.InsertValueDirectoryParams{
			SpaceID: space, Name: name, ParentID: parent, CreatedAt: time.Now().UnixMilli(), Author: int64(author),
		})
		if err != nil {
			return nil, err
		}
		return directoryUpdate(d), nil
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

func RenameDirectory(store *state.Service, directoryID int32, newName string) (*apigen.ValueDirectory, error) {
	if !ValidName(newName) {
		return nil, ErrNameInvalid
	}
	ctx := context.Background()
	var d *apigen.ValueDirectory
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		var err error
		d, err = GetDirectory(ctx, q, int64(directoryID))
		if err != nil {
			return nil, err
		}
		if d.Name == newName {
			return nil, nil
		}
		if err := requireNameFree(ctx, q, int64(d.SpaceID), int64(d.ParentID), newName, 0, 0, int64(d.ID)); err != nil {
			return nil, err
		}
		if err := q.SetValueDirectoryName(ctx, pq.SetValueDirectoryNameParams{Name: newName, ID: int64(d.ID)}); err != nil {
			return nil, err
		}
		d, err = q.GetValueDirectoryByID(ctx, int64(d.ID))
		if err != nil {
			return nil, err
		}
		return directoryUpdate(d), nil
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

func MoveDirectorySpace(store *state.Service, directoryID, newSpaceID int32) error {
	d, err := GetDirectory(context.Background(), store.Queries(), int64(directoryID))
	if err != nil {
		return err
	}
	if nodes.NormalizedUserSpaceID(newSpaceID) == d.SpaceID {
		return nil
	}
	return ErrSpaceMoveUnsupported
}

func MoveDirectory(store *state.Service, directoryID, newParentID int32) (*apigen.ValueDirectory, error) {
	ctx := context.Background()
	parent := int64(newParentID)
	var d *apigen.ValueDirectory
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		var err error
		d, err = GetDirectory(ctx, q, int64(directoryID))
		if err != nil {
			return nil, err
		}
		if int64(d.ParentID) == parent {
			return nil, nil
		}
		for cur := parent; cur != 0; {
			if cur == int64(d.ID) {
				return nil, ErrDirectoryCycle
			}
			p, err := GetDirectory(ctx, q, cur)
			if err != nil {
				return nil, err
			}
			if p.SpaceID != d.SpaceID {
				return nil, ErrSpaceMoveUnsupported
			}
			cur = int64(p.ParentID)
		}
		if err := requireNameFree(ctx, q, int64(d.SpaceID), parent, d.Name, 0, 0, int64(d.ID)); err != nil {
			return nil, err
		}
		if err := q.SetValueDirectoryParent(ctx, pq.SetValueDirectoryParentParams{ParentID: parent, ID: int64(d.ID)}); err != nil {
			return nil, err
		}
		d, err = q.GetValueDirectoryByID(ctx, int64(d.ID))
		if err != nil {
			return nil, err
		}
		return directoryUpdate(d), nil
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

func DeleteDirectory(store *state.Service, directoryID int32) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		d, err := GetDirectory(ctx, q, int64(directoryID))
		if err != nil {
			return nil, err
		}
		secretCount, err := q.CountSecretsInDirectory(ctx, int64(d.ID))
		if err != nil {
			return nil, err
		}
		configCount, err := q.CountConfigsInDirectory(ctx, int64(d.ID))
		if err != nil {
			return nil, err
		}
		children, err := q.CountChildValueDirectories(ctx, int64(d.ID))
		if err != nil {
			return nil, err
		}
		if secretCount > 0 || configCount > 0 || children > 0 {
			return nil, ErrDirectoryNotEmpty
		}
		if err := q.DeleteValueDirectory(ctx, int64(d.ID)); err != nil {
			return nil, err
		}
		tombstone := *d
		tombstone.Deleted = true
		return directoryUpdate(&tombstone), nil
	})
}
