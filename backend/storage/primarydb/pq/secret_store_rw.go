package pq

import (
	"context"
)

type SecretKeyslot struct {
	Slot       string
	SmkVersion int64
	WrappedSmk []byte
	Nonce      []byte
	KdfSalt    []byte
	CreatedAt  int64
}

type SystemSecret struct {
	Name       string
	SmkVersion int64
	Ciphertext []byte
	Nonce      []byte
	CreatedAt  int64
	UpdatedAt  int64
}

const getSystemSecret = `SELECT name, smk_version, ciphertext, nonce, created_at, updated_at
FROM system_secrets
WHERE name = ?
`

func (q *Queries) GetSystemSecret(ctx context.Context, name string) (SystemSecret, error) {
	row := q.db.QueryRowContext(ctx, getSystemSecret, name)
	var i SystemSecret
	err := row.Scan(
		&i.Name,
		&i.SmkVersion,
		&i.Ciphertext,
		&i.Nonce,
		&i.CreatedAt,
		&i.UpdatedAt,
	)
	return i, err
}

const listSecretKeyslots = `SELECT slot, smk_version, wrapped_smk, nonce, kdf_salt, created_at
FROM secret_keyslots ORDER BY slot
`

func (q *Queries) ListSecretKeyslots(ctx context.Context) ([]SecretKeyslot, error) {
	rows, err := q.db.QueryContext(ctx, listSecretKeyslots)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SecretKeyslot
	for rows.Next() {
		var i SecretKeyslot
		if err := rows.Scan(
			&i.Slot,
			&i.SmkVersion,
			&i.WrappedSmk,
			&i.Nonce,
			&i.KdfSalt,
			&i.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const listSecretVersionIDsBySecretID = `SELECT id FROM secret_event_log
WHERE secret_id = ? AND value_changed != 0
ORDER BY value_version
`

func (q *Queries) ListSecretVersionIDsBySecretID(ctx context.Context, secretID int64) ([]int64, error) {
	rows, err := q.db.QueryContext(ctx, listSecretVersionIDsBySecretID, secretID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		items = append(items, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const upsertSecretKeyslot = `INSERT INTO secret_keyslots (slot, smk_version, wrapped_smk, nonce, kdf_salt, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(slot) DO UPDATE SET
    smk_version = excluded.smk_version,
    wrapped_smk = excluded.wrapped_smk,
    nonce = excluded.nonce,
    kdf_salt = excluded.kdf_salt,
    created_at = excluded.created_at
`

type UpsertSecretKeyslotParams struct {
	Slot       string
	SmkVersion int64
	WrappedSmk []byte
	Nonce      []byte
	KdfSalt    []byte
	CreatedAt  int64
}

func (q *Queries) UpsertSecretKeyslot(ctx context.Context, arg UpsertSecretKeyslotParams) error {
	_, err := q.db.ExecContext(ctx, upsertSecretKeyslot,
		arg.Slot,
		arg.SmkVersion,
		arg.WrappedSmk,
		arg.Nonce,
		arg.KdfSalt,
		arg.CreatedAt,
	)
	return err
}

const upsertSystemSecret = `INSERT INTO system_secrets (name, smk_version, ciphertext, nonce, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
    smk_version = excluded.smk_version,
    ciphertext = excluded.ciphertext,
    nonce = excluded.nonce,
    updated_at = excluded.updated_at
`

type UpsertSystemSecretParams struct {
	Name       string
	SmkVersion int64
	Ciphertext []byte
	Nonce      []byte
	CreatedAt  int64
	UpdatedAt  int64
}

func (q *Queries) UpsertSystemSecret(ctx context.Context, arg UpsertSystemSecretParams) error {
	_, err := q.db.ExecContext(ctx, upsertSystemSecret,
		arg.Name,
		arg.SmkVersion,
		arg.Ciphertext,
		arg.Nonce,
		arg.CreatedAt,
		arg.UpdatedAt,
	)
	return err
}
