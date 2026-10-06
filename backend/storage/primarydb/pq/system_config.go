package pq

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// SystemConfigRevision is the live settings document with the envelope of
// the write that made it; Seq is its version.
type SystemConfigRevision struct {
	Seq         int64
	UpdatedAt   int64
	Author      int64
	CreatedTime int64
	ConfigBlob  []byte
}

func (q *Queries) GetSystemConfig(ctx context.Context) (SystemConfigRevision, error) {
	var i SystemConfigRevision
	err := q.db.QueryRowContext(ctx, `SELECT seq, event_time, author, created_time, config_blob FROM system_config WHERE id = ?`, SystemConfigEntityID).Scan(&i.Seq, &i.UpdatedAt, &i.Author, &i.CreatedTime, &i.ConfigBlob)
	return i, err
}

func (q *Queries) reduceSystemConfig(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, cfg *apigen.SystemConfig) error {
	if cfg == nil {
		return fmt.Errorf("payload has no config")
	}
	value := *cfg
	value.ID = uint32(SystemConfigEntityID)
	return q.upsert(ctx, meta, `INSERT INTO system_config (id, config_blob, seq, event_time, author, created_time) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET config_blob = excluded.config_blob, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, notNullBlob(value.Encode()), env.Seq, env.EventTime, env.Author, env.EventTime)
}

func (q *Queries) deleteSystemConfigRow(ctx context.Context, id uint64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM system_config WHERE id = ?`, id)
	return err
}
