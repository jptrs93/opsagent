package pq

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// SecretKeyslot is one live wrapping of the secrets master key. The wrapped
// bytes are stored and copied as written; nothing here opens them.
type SecretKeyslot struct {
	Kind       apigen.SecretKeyslotKind
	NodeID     int64
	SmkVersion int64
	WrappedSmk []byte
	Nonce      []byte
	KdfSalt    []byte
	UpdatedAt  int64
}

func (k SecretKeyslot) Entity() *apigen.SecretKeyslot {
	return &apigen.SecretKeyslot{Kind: k.Kind, NodeID: int32(k.NodeID), SmkVersion: k.SmkVersion, WrappedSmk: k.WrappedSmk, Nonce: k.Nonce, KdfSalt: k.KdfSalt, UpdatedAt: k.UpdatedAt}
}

const secretKeyslotColumns = `kind, node_id, smk_version, wrapped_smk, nonce, kdf_salt, updated_at`

func (q *Queries) ListSecretKeyslots(ctx context.Context) ([]SecretKeyslot, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+secretKeyslotColumns+` FROM secret_keyslots ORDER BY kind, node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecretKeyslot
	for rows.Next() {
		var k SecretKeyslot
		if err := rows.Scan(&k.Kind, &k.NodeID, &k.SmkVersion, &k.WrappedSmk, &k.Nonce, &k.KdfSalt, &k.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

type secretKeyslotRow struct {
	rowEnvelope
	SecretKeyslot
}

func (q *Queries) listSecretKeyslotRows(ctx context.Context) ([]secretKeyslotRow, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+secretKeyslotColumns+`, seq, event_time, author, created_time FROM secret_keyslots ORDER BY kind, node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []secretKeyslotRow
	for rows.Next() {
		var r secretKeyslotRow
		if err := rows.Scan(&r.Kind, &r.NodeID, &r.SmkVersion, &r.WrappedSmk, &r.Nonce, &r.KdfSalt, &r.UpdatedAt, &r.Seq, &r.EventTime, &r.Author, &r.CreatedTime); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// NodeSecretKeyslotDeletes returns the delete mutations that remove a node's
// machine slots.
func (q *Queries) NodeSecretKeyslotDeletes(ctx context.Context, meta EventMeta, nodeID int64) ([]Mutation, error) {
	live, err := q.ListSecretKeyslots(ctx)
	if err != nil {
		return nil, err
	}
	var deleted []Mutation
	for _, k := range live {
		if k.Kind == apigen.SecretKeyslotKind_SECRET_KEYSLOT_MACHINE && k.NodeID == nodeID {
			deleted = append(deleted, DeleteMutation(meta, apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT, SecretKeyslotEntityID(k)))
		}
	}
	return deleted, nil
}

func (q *Queries) reduceSecretKeyslot(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id int64, k *apigen.SecretKeyslot) error {
	if k == nil {
		return fmt.Errorf("payload has no keyslot")
	}
	return q.upsert(ctx, meta, `INSERT INTO secret_keyslots (id, kind, node_id, smk_version, wrapped_smk, nonce, kdf_salt, updated_at, seq, event_time, author, created_time)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, node_id = excluded.node_id, smk_version = excluded.smk_version, wrapped_smk = excluded.wrapped_smk,
  nonce = excluded.nonce, kdf_salt = excluded.kdf_salt, updated_at = excluded.updated_at, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, int64(k.Kind), int64(k.NodeID), k.SmkVersion, notNullBlob(k.WrappedSmk), notNullBlob(k.Nonce), k.KdfSalt, k.UpdatedAt, env.Seq, env.EventTime, env.Author, env.EventTime)
}

func (q *Queries) deleteSecretKeyslotRow(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM secret_keyslots WHERE id = ?`, id)
	return err
}
