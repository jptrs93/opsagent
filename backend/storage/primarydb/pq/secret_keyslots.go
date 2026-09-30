package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type SecretKeyslot struct {
	Kind       apigen.SecretKeyslotKind
	NodeID     int64
	SmkVersion int64
	WrappedSmk []byte
	Nonce      []byte
	KdfSalt    []byte
	UpdatedAt  int64
}

const secretKeyslotColumns = `kind, node_id, smk_version, wrapped_smk, nonce, kdf_salt, event_time, event_type`

func scanSecretKeyslot(row scanner) (SecretKeyslot, bool, error) {
	var k SecretKeyslot
	var eventType int64
	err := row.Scan(&k.Kind, &k.NodeID, &k.SmkVersion, &k.WrappedSmk, &k.Nonce, &k.KdfSalt, &k.UpdatedAt, &eventType)
	return k, eventType != int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE), err
}

func (q *Queries) ListLiveSecretKeyslots(ctx context.Context) ([]SecretKeyslot, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+secretKeyslotColumns+` FROM secret_keyslot_event_log
		WHERE id IN (SELECT MAX(id) FROM secret_keyslot_event_log GROUP BY kind, node_id) ORDER BY kind, node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecretKeyslot
	for rows.Next() {
		k, live, err := scanSecretKeyslot(rows)
		if err != nil {
			return nil, err
		}
		if live {
			out = append(out, k)
		}
	}
	return out, rows.Err()
}

type SecretKeyslotEventParams struct {
	EventMeta
	SecretKeyslot
}

func (q *Queries) InsertSecretKeyslotEvent(ctx context.Context, arg SecretKeyslotEventParams) error {
	_, err := q.db.ExecContext(ctx, `INSERT INTO secret_keyslot_event_log (global_seq, event_time, author, kind, node_id, event_type, smk_version, wrapped_smk, nonce, kdf_salt)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		arg.GlobalSeq, arg.EventTime, arg.Author, arg.Kind, arg.NodeID, arg.EventType, arg.SmkVersion, arg.WrappedSmk, arg.Nonce, arg.KdfSalt)
	return err
}

func (q *Queries) DeleteNodeSecretKeyslots(ctx context.Context, meta EventMeta, nodeID int64) ([]Mutation, error) {
	live, err := q.ListLiveSecretKeyslots(ctx)
	if err != nil {
		return nil, err
	}
	meta.EventType = apigen.AuthzVerb_AUTHZ_VERB_DELETE
	var deleted []Mutation
	for _, k := range live {
		if k.Kind != apigen.SecretKeyslotKind_SECRET_KEYSLOT_MACHINE || k.NodeID != nodeID {
			continue
		}
		if err := q.InsertSecretKeyslotEvent(ctx, SecretKeyslotEventParams{EventMeta: meta, SecretKeyslot: k}); err != nil {
			return deleted, err
		}
		deleted = append(deleted, SecretKeyslotMutation(meta, k))
	}
	return deleted, nil
}
