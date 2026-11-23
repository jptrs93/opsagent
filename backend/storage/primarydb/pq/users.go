package pq

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// UserRow is one live account: the facts of the User payload with the
// encoded UserAuthentication as DataBlob, and the envelope of the last
// write.
type UserRow struct {
	ID           uint64
	Name         string
	Email        string
	PasswordHash string
	ActivatedAt  int64
	DataBlob     []byte
	CreatedTime  int64
	Seq          int64
	EventTime    int64
	Author       int64
}

func maybeString(s string) apigen.Maybe[string] {
	if s == "" {
		return apigen.Maybe[string]{}
	}
	return apigen.Some(s)
}

func maybeID(id uint64) apigen.Maybe[uint64] {
	if id == 0 {
		return apigen.Maybe[uint64]{}
	}
	return apigen.Some(id)
}

// Public is the account without its authentication material or password
// hash.
func (r UserRow) Public() apigen.User {
	return apigen.User{ID: r.ID, Name: maybeString(r.Name), Email: maybeString(r.Email), ActivatedAt: millisToTime(r.ActivatedAt)}
}

// UserEntity is the row as the event stream carries it: the account with
// its authentication material and password hash.
func UserEntity(r UserRow) (apigen.User, error) {
	u := r.Public()
	u.PasswordHash = maybeString(r.PasswordHash)
	auth, err := apigen.DecodeUserAuthentication(r.DataBlob)
	if err != nil {
		return u, fmt.Errorf("user %d authentication: %w", r.ID, err)
	}
	u.Authentication = *auth
	return u, nil
}

const userColumns = `id, name, email, password_hash, activated_at, data_blob, created_time, seq, event_time, author`

func scanUserRow(row scanner) (UserRow, error) {
	var r UserRow
	err := row.Scan(&r.ID, &r.Name, &r.Email, &r.PasswordHash, &r.ActivatedAt, &r.DataBlob, &r.CreatedTime, &r.Seq, &r.EventTime, &r.Author)
	return r, err
}

// GetUserRow returns the live account, or sql.ErrNoRows.
func (q *Queries) GetUserRow(ctx context.Context, userID uint64) (UserRow, error) {
	return scanUserRow(q.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, userID))
}

func (q *Queries) ListUserRows(ctx context.Context) ([]UserRow, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserRow
	for rows.Next() {
		r, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) GetUser(ctx context.Context, userID uint64) (apigen.User, error) {
	r, err := q.GetUserRow(ctx, userID)
	return r.Public(), err
}

func (q *Queries) ListUsers(ctx context.Context) ([]apigen.User, error) {
	rows, err := q.ListUserRows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.User, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Public())
	}
	return out, nil
}

// GetFullUser returns the live account with its authentication material.
func (q *Queries) GetFullUser(ctx context.Context, userID uint64) (*apigen.User, error) {
	r, err := q.GetUserRow(ctx, userID)
	if err != nil {
		return nil, err
	}
	u, err := UserEntity(r)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// ListFullUsers returns every live account with its authentication
// material; an account whose record does not decode is left out, so it can
// never match a credential.
func (q *Queries) ListFullUsers(ctx context.Context) ([]*apigen.User, error) {
	rows, err := q.ListUserRows(ctx)
	if err != nil {
		return nil, err
	}
	var out []*apigen.User
	for _, r := range rows {
		u, err := UserEntity(r)
		if err != nil {
			continue
		}
		out = append(out, &u)
	}
	return out, nil
}

func (q *Queries) reduceUser(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, u *apigen.User) error {
	if u == nil {
		return fmt.Errorf("payload has no user")
	}
	return q.upsert(ctx, meta, `INSERT INTO users (id, name, email, password_hash, activated_at, data_blob, created_time, seq, event_time, author) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET name = excluded.name, email = excluded.email, password_hash = excluded.password_hash, activated_at = excluded.activated_at,
  data_blob = excluded.data_blob, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, u.Name.Value, u.Email.Value, u.PasswordHash.Value, timeToMillis(u.ActivatedAt), notNullBlob(u.Authentication.Encode()), env.EventTime, env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteUserRow(ctx context.Context, id uint64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}
