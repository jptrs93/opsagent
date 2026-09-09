package pq

import (
	"context"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// scanSystemConfig returns the public revision; credential hashes remain internal.
func scanSystemConfig(row scanner) (*apigen.SystemConfigVersion, error) {
	var e apigen.SystemConfigVersion
	var updatedAt int64
	var blob []byte
	if err := row.Scan(&e.Version, &updatedAt, &blob); err != nil {
		return nil, err
	}
	cfg, err := apigen.DecodeSystemConfig(blob)
	if err != nil {
		return nil, err
	}
	cfg.MasterPasswordHash = ""
	e.Config = *cfg
	e.UpdatedAt = time.UnixMilli(updatedAt)
	return &e, nil
}

func (q *Queries) GetLatestSystemConfig(ctx context.Context) (*apigen.SystemConfigVersion, error) {
	return scanSystemConfig(q.db.QueryRowContext(ctx, `SELECT id,updated_at,config_blob FROM system_config_revisions ORDER BY id DESC LIMIT 1`))
}

func (q *Queries) GetSystemConfigByID(ctx context.Context, id int64) (*apigen.SystemConfigVersion, error) {
	return scanSystemConfig(q.db.QueryRowContext(ctx, `SELECT id,updated_at,config_blob FROM system_config_revisions WHERE id=?`, id))
}
