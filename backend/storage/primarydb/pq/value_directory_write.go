package pq

import (
	"context"
)

const countChildValueDirectories = `SELECT COUNT(*) FROM value_directories WHERE parent_id = ?
`

func (q *Queries) CountChildValueDirectories(ctx context.Context, parentID int64) (int64, error) {
	row := q.db.QueryRowContext(ctx, countChildValueDirectories, parentID)
	var count int64
	err := row.Scan(&count)
	return count, err
}

const countValueDirectorySiblingsWithName = `
SELECT COUNT(*) FROM value_directories
WHERE space_id = ? AND parent_id = ? AND name = ? AND id != ?
`

type CountValueDirectorySiblingsWithNameParams struct {
	SpaceID  int64
	ParentID int64
	Name     string
	ID       int64
}

// Config and secret reads and writes are hand-written in values.go: the
// current state is the highest-version event row, event_type is the deletion
// truth, and a value_changed row is a pinnable value version.
func (q *Queries) CountValueDirectorySiblingsWithName(ctx context.Context, arg CountValueDirectorySiblingsWithNameParams) (int64, error) {
	row := q.db.QueryRowContext(ctx, countValueDirectorySiblingsWithName,
		arg.SpaceID,
		arg.ParentID,
		arg.Name,
		arg.ID,
	)
	var count int64
	err := row.Scan(&count)
	return count, err
}

const deleteValueDirectory = `DELETE FROM value_directories WHERE id = ?
`

func (q *Queries) DeleteValueDirectory(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, deleteValueDirectory, id)
	return err
}

const setValueDirectoryName = `UPDATE value_directories SET name = ? WHERE id = ?
`

type SetValueDirectoryNameParams struct {
	Name string
	ID   int64
}

func (q *Queries) SetValueDirectoryName(ctx context.Context, arg SetValueDirectoryNameParams) error {
	_, err := q.db.ExecContext(ctx, setValueDirectoryName, arg.Name, arg.ID)
	return err
}

const setValueDirectoryParent = `UPDATE value_directories SET parent_id = ? WHERE id = ?
`

type SetValueDirectoryParentParams struct {
	ParentID int64
	ID       int64
}

func (q *Queries) SetValueDirectoryParent(ctx context.Context, arg SetValueDirectoryParentParams) error {
	_, err := q.db.ExecContext(ctx, setValueDirectoryParent, arg.ParentID, arg.ID)
	return err
}
