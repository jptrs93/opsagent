package pq

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// Keyslot kinds as the secret_keyslots.kind column stores them.
const (
	keyslotKindMachine  = 1
	keyslotKindRecovery = 2
)

const secretKeyslotColumns = `id, kind, node_id, smk_version, wrapped_smk, nonce, kdf_salt`

func keyslotColumns(k *apigen.SecretKeyslot) (kind int64, nodeID uint64, kdfSalt []byte, err error) {
	switch w := k.Wrapping.Value; {
	case w.MachineKey != nil:
		return keyslotKindMachine, w.MachineKey.NodeID, nil, nil
	case w.RecoveryCode != nil:
		return keyslotKindRecovery, 0, w.RecoveryCode.KdfSalt, nil
	}
	return 0, 0, nil, fmt.Errorf("keyslot %d has no wrapping", k.ID)
}

func scanSecretKeyslotInto(k *apigen.SecretKeyslot, extra []any, row scanner) error {
	var kind int64
	var nodeID uint64
	var kdfSalt []byte
	dest := []any{&k.ID, &kind, &nodeID, &k.SmkVersion, &k.WrappedSmk, &k.Nonce, &kdfSalt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return err
	}
	switch kind {
	case keyslotKindMachine:
		k.Wrapping = apigen.KeyslotWrapping{Value: apigen.KeyslotWrappingValueOneof{MachineKey: &apigen.MachineKey{NodeID: nodeID}}}
	case keyslotKindRecovery:
		k.Wrapping = apigen.KeyslotWrapping{Value: apigen.KeyslotWrappingValueOneof{RecoveryCode: &apigen.RecoveryCode{KdfSalt: kdfSalt}}}
	default:
		return fmt.Errorf("keyslot %d has unknown kind %d", k.ID, kind)
	}
	return nil
}

// ListSecretKeyslots returns every live wrapping of the secrets master key.
// The wrapped bytes are returned as stored; nothing here opens them.
func (q *Queries) ListSecretKeyslots(ctx context.Context) ([]apigen.SecretKeyslot, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+secretKeyslotColumns+` FROM secret_keyslots ORDER BY kind, node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apigen.SecretKeyslot
	for rows.Next() {
		var k apigen.SecretKeyslot
		if err := scanSecretKeyslotInto(&k, nil, rows); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

type secretKeyslotRow struct {
	rowEnvelope
	Keyslot apigen.SecretKeyslot
}

func (q *Queries) listSecretKeyslotRows(ctx context.Context) ([]secretKeyslotRow, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+secretKeyslotColumns+`, seq, event_time, author, created_time FROM secret_keyslots ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []secretKeyslotRow
	for rows.Next() {
		var r secretKeyslotRow
		if err := scanSecretKeyslotInto(&r.Keyslot, []any{&r.Seq, &r.EventTime, &r.Author, &r.CreatedTime}, rows); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// NodeSecretKeyslotDeletes returns the delete mutations that remove a node's
// machine slots.
func (q *Queries) NodeSecretKeyslotDeletes(ctx context.Context, meta EventMeta, nodeID uint64) ([]Mutation, error) {
	live, err := q.ListSecretKeyslots(ctx)
	if err != nil {
		return nil, err
	}
	var deleted []Mutation
	for _, k := range live {
		if mk := k.Wrapping.Value.MachineKey; mk != nil && mk.NodeID == nodeID {
			deleted = append(deleted, DeleteMutation(meta, apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT, k.ID))
		}
	}
	return deleted, nil
}

func (q *Queries) reduceSecretKeyslot(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, k *apigen.SecretKeyslot) error {
	if k == nil {
		return fmt.Errorf("payload has no keyslot")
	}
	kind, nodeID, kdfSalt, err := keyslotColumns(k)
	if err != nil {
		return err
	}
	return q.upsert(ctx, meta, `INSERT INTO secret_keyslots (id, kind, node_id, smk_version, wrapped_smk, nonce, kdf_salt, seq, event_time, author, created_time)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, node_id = excluded.node_id, smk_version = excluded.smk_version, wrapped_smk = excluded.wrapped_smk,
  nonce = excluded.nonce, kdf_salt = excluded.kdf_salt, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, kind, nodeID, k.SmkVersion, notNullBlob(k.WrappedSmk), notNullBlob(k.Nonce), kdfSalt, env.Seq, env.EventTime, env.Author, env.EventTime)
}

func (q *Queries) deleteSecretKeyslotRow(ctx context.Context, id uint64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM secret_keyslots WHERE id = ?`, id)
	return err
}
