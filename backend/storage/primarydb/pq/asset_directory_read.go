package pq

import (
	"context"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func scanAssetDirectory(row scanner) (apigen.AssetDirectory, error) {
	var e apigen.AssetDirectory
	var createdAt int64
	err := row.Scan(&e.ID, &e.SpaceID, &e.Key, &e.ParentID, &createdAt, &e.Author)
	e.CreatedAt = time.UnixMilli(createdAt)
	return e, err
}

func (q *Queries) GetAssetDirectoryByID(ctx context.Context, id int64) (apigen.AssetDirectory, error) {
	return scanAssetDirectory(q.db.QueryRowContext(ctx, `SELECT id, space_id, key, parent_id, created_at, author
FROM asset_directories
WHERE id = ?`, id))
}

func (q *Queries) ListAssetDirectories(ctx context.Context) ([]apigen.AssetDirectory, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, space_id, key, parent_id, created_at, author
FROM asset_directories
ORDER BY space_id, parent_id, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apigen.AssetDirectory
	for rows.Next() {
		e, err := scanAssetDirectory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type InsertAssetDirectoryParams struct {
	SpaceID   int64
	Key       string
	ParentID  int64
	CreatedAt int64
	Author    int64
}

func (q *Queries) InsertAssetDirectory(ctx context.Context, arg InsertAssetDirectoryParams) (apigen.AssetDirectory, error) {
	return scanAssetDirectory(q.db.QueryRowContext(ctx, `INSERT INTO asset_directories (space_id, key, parent_id, created_at, author)
VALUES (?, ?, ?, ?, ?)
RETURNING id, space_id, key, parent_id, created_at, author`, arg.SpaceID, arg.Key, arg.ParentID, arg.CreatedAt, arg.Author))
}
