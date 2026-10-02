package pq

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// UserRow is one live account: the facts of the User payload with the
// encoded InternalUser as DataBlob, and the envelope of the last write.
type UserRow struct {
	ID          int64
	Name        string
	DataBlob    []byte
	CreatedTime int64
	Seq         int64
	EventTime   int64
	Author      int64
}

func (r UserRow) Public() apigen.User {
	return apigen.User{ID: int32(r.ID), Name: r.Name}
}

// UserEntity is the row as the event stream carries it.
func UserEntity(r UserRow) apigen.User {
	u := r.Public()
	u.Credentials = r.DataBlob
	return u
}

const userColumns = `id, name, data_blob, created_time, seq, event_time, author`

func scanUserRow(row scanner) (UserRow, error) {
	var r UserRow
	err := row.Scan(&r.ID, &r.Name, &r.DataBlob, &r.CreatedTime, &r.Seq, &r.EventTime, &r.Author)
	return r, err
}

// GetUserRow returns the live account, or sql.ErrNoRows.
func (q *Queries) GetUserRow(ctx context.Context, userID int64) (UserRow, error) {
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

func (q *Queries) GetUser(ctx context.Context, userID int64) (apigen.User, error) {
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

func (q *Queries) GetInternalUser(ctx context.Context, userID int64) (*apigen.InternalUser, error) {
	r, err := q.GetUserRow(ctx, userID)
	if err != nil {
		return nil, err
	}
	return apigen.DecodeInternalUser(r.DataBlob)
}

func (q *Queries) ListInternalUsers(ctx context.Context) ([]*apigen.InternalUser, error) {
	rows, err := q.ListUserRows(ctx)
	if err != nil {
		return nil, err
	}
	var out []*apigen.InternalUser
	for _, r := range rows {
		u, err := apigen.DecodeInternalUser(r.DataBlob)
		if err != nil {
			// Preserve matching semantics: an invalid credential record cannot match.
			continue
		}
		out = append(out, u)
	}
	return out, nil
}

func (q *Queries) reduceUser(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id int64, u *apigen.User) error {
	if u == nil {
		return fmt.Errorf("payload has no user")
	}
	return q.upsert(ctx, meta, `INSERT INTO users (id, name, data_blob, created_time, seq, event_time, author) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET name = excluded.name, data_blob = excluded.data_blob, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, u.Name, notNullBlob(u.Credentials), env.EventTime, env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteUserRow(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}
