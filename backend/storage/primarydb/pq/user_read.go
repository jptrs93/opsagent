package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func scanUser(row scanner) (apigen.User, error) {
	var e apigen.User
	err := row.Scan(&e.ID, &e.Name, &e.CreatedAt, &e.LastLoginAt)
	return e, err
}

func (q *Queries) GetUser(ctx context.Context, id int64) (apigen.User, error) {
	return scanUser(q.db.QueryRowContext(ctx, `SELECT id, name, created_at, last_login_at FROM users WHERE id = ?`, id))
}

func (q *Queries) ListUsers(ctx context.Context) ([]apigen.User, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, name, created_at, last_login_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apigen.User
	for rows.Next() {
		e, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) GetInternalUser(ctx context.Context, id int64) (*apigen.InternalUser, error) {
	var blob []byte
	if err := q.db.QueryRowContext(ctx, `SELECT data_blob FROM users WHERE id=?`, id).Scan(&blob); err != nil {
		return nil, err
	}
	return apigen.DecodeInternalUser(blob)
}

func (q *Queries) ListInternalUsers(ctx context.Context) ([]*apigen.InternalUser, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT data_blob FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.InternalUser
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, err
		}
		u, err := apigen.DecodeInternalUser(blob)
		if err != nil {
			// Preserve matching semantics: an invalid credential record cannot match.
			continue
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
