package pq

import (
	"context"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func scanValueDirectory(row scanner) (*apigen.ValueDirectory, error) {
	e := &apigen.ValueDirectory{}
	var createdAt int64
	if err := row.Scan(&e.ID, &e.SpaceID, &e.Name, &e.ParentID, &createdAt, &e.Author); err != nil {
		return nil, err
	}
	e.CreatedAt = time.UnixMilli(createdAt)
	return e, nil
}

func (q *Queries) GetValueDirectoryByID(ctx context.Context, id int64) (*apigen.ValueDirectory, error) {
	return scanValueDirectory(q.db.QueryRowContext(ctx, `SELECT id, space_id, name, parent_id, created_at, author
FROM value_directories
WHERE id = ?`, id))
}

func (q *Queries) ListValueDirectories(ctx context.Context) ([]*apigen.ValueDirectory, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, space_id, name, parent_id, created_at, author
FROM value_directories
ORDER BY space_id, parent_id, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.ValueDirectory
	for rows.Next() {
		e, err := scanValueDirectory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type InsertValueDirectoryParams struct {
	SpaceID   int64
	Name      string
	ParentID  int64
	CreatedAt int64
	Author    int64
}

func (q *Queries) InsertValueDirectory(ctx context.Context, arg InsertValueDirectoryParams) (*apigen.ValueDirectory, error) {
	return scanValueDirectory(q.db.QueryRowContext(ctx, `INSERT INTO value_directories (space_id, name, parent_id, created_at, author)
VALUES (?, ?, ?, ?, ?)
RETURNING id, space_id, name, parent_id, created_at, author`, arg.SpaceID, arg.Name, arg.ParentID, arg.CreatedAt, arg.Author))
}
