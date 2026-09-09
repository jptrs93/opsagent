package pq

import (
	"context"
)

const countChildAssetDirectories = `SELECT COUNT(*) FROM asset_directories WHERE parent_id = ?
`

func (q *Queries) CountChildAssetDirectories(ctx context.Context, parentID int64) (int64, error) {
	row := q.db.QueryRowContext(ctx, countChildAssetDirectories, parentID)
	var count int64
	err := row.Scan(&count)
	return count, err
}

const countDirectorySiblingsWithKey = `
SELECT COUNT(*) FROM asset_directories
WHERE space_id = ? AND parent_id = ? AND key = ? AND id != ?
`

type CountDirectorySiblingsWithKeyParams struct {
	SpaceID  int64
	ParentID int64
	Key      string
	ID       int64
}

// Asset reads and writes are hand-written in assets.go: the current state is
// the highest-version event row, event_type is the deletion truth, and a
// value_changed row is a pinnable content version.
func (q *Queries) CountDirectorySiblingsWithKey(ctx context.Context, arg CountDirectorySiblingsWithKeyParams) (int64, error) {
	row := q.db.QueryRowContext(ctx, countDirectorySiblingsWithKey,
		arg.SpaceID,
		arg.ParentID,
		arg.Key,
		arg.ID,
	)
	var count int64
	err := row.Scan(&count)
	return count, err
}

const deleteAssetDirectory = `DELETE FROM asset_directories WHERE id = ?
`

func (q *Queries) DeleteAssetDirectory(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, deleteAssetDirectory, id)
	return err
}

const setAssetDirectoryKey = `UPDATE asset_directories SET key = ? WHERE id = ?
`

type SetAssetDirectoryKeyParams struct {
	Key string
	ID  int64
}

func (q *Queries) SetAssetDirectoryKey(ctx context.Context, arg SetAssetDirectoryKeyParams) error {
	_, err := q.db.ExecContext(ctx, setAssetDirectoryKey, arg.Key, arg.ID)
	return err
}

const setAssetDirectoryParent = `UPDATE asset_directories SET parent_id = ? WHERE id = ?
`

type SetAssetDirectoryParentParams struct {
	ParentID int64
	ID       int64
}

func (q *Queries) SetAssetDirectoryParent(ctx context.Context, arg SetAssetDirectoryParentParams) error {
	_, err := q.db.ExecContext(ctx, setAssetDirectoryParent, arg.ParentID, arg.ID)
	return err
}
