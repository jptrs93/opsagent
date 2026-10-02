package pq

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// newMeta is the meta of an entity after the write env describes, before
// the reducer fills in its creation time and counters.
func newMeta(env rowEnvelope) *apigen.EntityMeta {
	return &apigen.EntityMeta{UpdatedTime: env.EventTime, UpdatedSeq: env.Seq, UpdatedActor: int32(env.Author)}
}

// rowMeta is the meta of a row read back from a table: its creation time
// and the envelope of its last write.
func rowMeta(createdTime, seq, eventTime, author int64) *apigen.EntityMeta {
	return &apigen.EntityMeta{CreatedTime: createdTime, UpdatedTime: eventTime, UpdatedSeq: seq, UpdatedActor: int32(author)}
}

func (env rowEnvelope) meta() *apigen.EntityMeta {
	return rowMeta(env.CreatedTime, env.Seq, env.EventTime, env.Author)
}

// upsert runs an INSERT ... ON CONFLICT ... RETURNING created_time and
// records the creation time on meta. A conditional upsert that keeps the
// existing row returns nothing; the fallback query then reads the row's
// creation time.
func (q *Queries) upsert(ctx context.Context, meta *apigen.EntityMeta, query string, args ...any) error {
	err := q.db.QueryRowContext(ctx, query, args...).Scan(&meta.CreatedTime)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

// rowMetaIfStale replaces meta with the row's when a conditional upsert kept
// the existing row, so a stale status report is stamped with the state the
// table holds rather than the write it lost to.
func (q *Queries) rowMetaIfStale(ctx context.Context, meta *apigen.EntityMeta, t apigen.CoreEntityType, id int64) error {
	if meta.CreatedTime != 0 {
		return nil
	}
	current, err := q.MetaOf(ctx, t, id)
	if err != nil || current == nil {
		return err
	}
	*meta = *current
	return nil
}

var metaQueries = map[apigen.CoreEntityType]string{
	apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:                `SELECT created_time, seq, event_time, author, version, spec_version, 0 FROM deployments WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:        `SELECT created_time, seq, event_time, author, 0, 0, 0 FROM scheduled_instances WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_NODE:                      `SELECT created_time, seq, event_time, author, 0, 0, 0 FROM nodes WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_SECRET:                    `SELECT created_time, seq, event_time, author, 0, 0, value_version FROM secrets WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_CONFIG:                    `SELECT created_time, seq, event_time, author, 0, 0, value_version FROM configs WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_ASSET:                     `SELECT created_time, seq, event_time, author, 0, 0, value_version FROM assets WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:            `SELECT created_time, seq, event_time, author, 0, 0, 0 FROM network_policies WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_SPACE:                     `SELECT created_time, seq, event_time, author, 0, 0, 0 FROM spaces WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_USER:                      `SELECT created_time, seq, event_time, author, 0, 0, 0 FROM users WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY:           `SELECT created_time, seq, event_time, author, 0, 0, 0 FROM value_directories WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY:           `SELECT created_time, seq, event_time, author, 0, 0, 0 FROM asset_directories WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_AUTHZ_RULE_TEMPLATE:       `SELECT created_time, seq, event_time, author, 0, 0, 0 FROM authz_rule_templates WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT:               `SELECT created_time, seq, event_time, author, 0, 0, 0 FROM authz_grants WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE:         `SELECT created_time, seq, event_time, author, 0, 0, 0 FROM authz_global_rules WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG:             `SELECT created_time, seq, event_time, author, 0, 0, 0 FROM system_config WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS: `SELECT created_time, seq, event_time, author, 0, 0, 0 FROM scheduled_instance_status WHERE scheduled_instance_id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS:               `SELECT created_time, seq, event_time, author, 0, 0, 0 FROM node_status WHERE node_id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION:             `SELECT created_at, seq, event_time, author, 0, 0, 0 FROM agent_sessions WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_USER_SESSION:              `SELECT created_at, seq, event_time, author, 0, 0, 0 FROM user_sessions WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET:           `SELECT created_time, seq, event_time, author, 0, 0, 0 FROM nix_store_resets WHERE id = ?`,
	apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT:            `SELECT created_time, seq, event_time, author, 0, 0, 0 FROM secret_keyslots WHERE id = ?`,
}

// MetaOf returns the meta of a live entity from its row, or nil when the
// entity has none.
func (q *Queries) MetaOf(ctx context.Context, t apigen.CoreEntityType, id int64) (*apigen.EntityMeta, error) {
	query, ok := metaQueries[t]
	if !ok {
		return nil, fmt.Errorf("meta of %v: unknown entity type", t)
	}
	var meta apigen.EntityMeta
	err := q.db.QueryRowContext(ctx, query, id).Scan(&meta.CreatedTime, &meta.UpdatedSeq, &meta.UpdatedTime, &meta.UpdatedActor, &meta.Version, &meta.SpecVersion, &meta.ValueVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &meta, nil
}

// StampMeta fills the meta of every create and update in an update built
// outside Commit from the live rows, for a receipt.
func (q *Queries) StampMeta(ctx context.Context, u *apigen.CoreWriteUpdate) error {
	for _, m := range u.Mutations {
		if m.Delete != nil || m.Meta() != nil {
			continue
		}
		meta, err := q.MetaOf(ctx, m.Type(), m.EntityID())
		if err != nil {
			return err
		}
		m.SetMeta(meta)
	}
	return nil
}
