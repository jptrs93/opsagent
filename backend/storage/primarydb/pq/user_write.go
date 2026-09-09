package pq

import (
	"context"
)

const touchUserLastLogin = `UPDATE users SET last_login_at = ? WHERE id = ?
`

type TouchUserLastLoginParams struct {
	LastLoginAt int64
	ID          int64
}

func (q *Queries) TouchUserLastLogin(ctx context.Context, arg TouchUserLastLoginParams) error {
	_, err := q.db.ExecContext(ctx, touchUserLastLogin, arg.LastLoginAt, arg.ID)
	return err
}

const upsertPublicKey = `INSERT INTO public_keys (kid, key_bytes) VALUES (?, ?)
ON CONFLICT(kid) DO UPDATE SET key_bytes = excluded.key_bytes
`

type UpsertPublicKeyParams struct {
	Kid      string
	KeyBytes []byte
}

func (q *Queries) UpsertPublicKey(ctx context.Context, arg UpsertPublicKeyParams) error {
	_, err := q.db.ExecContext(ctx, upsertPublicKey, arg.Kid, arg.KeyBytes)
	return err
}

const upsertUser = `INSERT INTO users (id, name, data_blob, created_at) VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET name = excluded.name, data_blob = excluded.data_blob
`

type UpsertUserParams struct {
	ID        int64
	Name      string
	DataBlob  []byte
	CreatedAt int64
}

func (q *Queries) UpsertUser(ctx context.Context, arg UpsertUserParams) error {
	_, err := q.db.ExecContext(ctx, upsertUser,
		arg.ID,
		arg.Name,
		arg.DataBlob,
		arg.CreatedAt,
	)
	return err
}
