package pq

import (
	"context"
)

// InsertSystemConfigRevision appends a settings revision and returns its row
// id.
func (q *Queries) InsertSystemConfigRevision(ctx context.Context, updatedAt int64, configBlob []byte) (int64, error) {
	result, err := q.db.ExecContext(ctx, `
INSERT INTO system_config_revisions (updated_at, config_blob) VALUES (?, ?)
`, updatedAt, configBlob)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

type SystemConfigRevision struct {
	ID         int64
	UpdatedAt  int64
	ConfigBlob []byte
}

const getConfigByID = `SELECT id, updated_at, config_blob FROM system_config_revisions WHERE id = ?
`

func (q *Queries) GetConfigByID(ctx context.Context, id int64) (SystemConfigRevision, error) {
	row := q.db.QueryRowContext(ctx, getConfigByID, id)
	var i SystemConfigRevision
	err := row.Scan(&i.ID, &i.UpdatedAt, &i.ConfigBlob)
	return i, err
}

const getLatestConfig = `select id, updated_at, config_blob from system_config_revisions order by id desc limit 1
`

func (q *Queries) GetLatestConfig(ctx context.Context) (SystemConfigRevision, error) {
	row := q.db.QueryRowContext(ctx, getLatestConfig)
	var i SystemConfigRevision
	err := row.Scan(&i.ID, &i.UpdatedAt, &i.ConfigBlob)
	return i, err
}
