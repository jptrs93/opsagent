package pq

import (
	"context"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type SystemConfigRevisionParams struct {
	EventMeta
	ConfigBlob []byte
}

// InsertSystemConfigRevision appends a settings revision and returns its row
// id, which is the revision's version number.
func (q *Queries) InsertSystemConfigRevision(ctx context.Context, arg SystemConfigRevisionParams) (int64, error) {
	result, err := q.db.ExecContext(ctx, `INSERT INTO system_config_event_log (global_seq, event_time, author, event_type, config_blob) VALUES (?, ?, ?, ?, ?)`,
		arg.GlobalSeq, arg.EventTime, arg.Author, arg.EventType, arg.ConfigBlob)
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

const systemConfigColumns = `id, event_time, config_blob`

func (q *Queries) GetConfigByID(ctx context.Context, id int64) (SystemConfigRevision, error) {
	var i SystemConfigRevision
	err := q.db.QueryRowContext(ctx, `SELECT `+systemConfigColumns+` FROM system_config_event_log WHERE id = ?`, id).Scan(&i.ID, &i.UpdatedAt, &i.ConfigBlob)
	return i, err
}

func (q *Queries) GetLatestConfig(ctx context.Context) (SystemConfigRevision, error) {
	var i SystemConfigRevision
	err := q.db.QueryRowContext(ctx, `SELECT `+systemConfigColumns+` FROM system_config_event_log ORDER BY id DESC LIMIT 1`).Scan(&i.ID, &i.UpdatedAt, &i.ConfigBlob)
	return i, err
}

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
	return scanSystemConfig(q.db.QueryRowContext(ctx, `SELECT `+systemConfigColumns+` FROM system_config_event_log ORDER BY id DESC LIMIT 1`))
}

func (q *Queries) GetSystemConfigByID(ctx context.Context, id int64) (*apigen.SystemConfigVersion, error) {
	return scanSystemConfig(q.db.QueryRowContext(ctx, `SELECT `+systemConfigColumns+` FROM system_config_event_log WHERE id = ?`, id))
}

func (q *Queries) GetSystemConfigAtSeq(ctx context.Context, seq int64) (*apigen.SystemConfigVersion, error) {
	return scanSystemConfig(q.db.QueryRowContext(ctx, `SELECT `+systemConfigColumns+` FROM system_config_event_log WHERE global_seq = ? ORDER BY id DESC LIMIT 1`, seq))
}
