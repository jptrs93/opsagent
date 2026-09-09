package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func scanSpace(row scanner) (apigen.Space, error) {
	var e apigen.Space
	err := row.Scan(&e.ID, &e.Name)
	return e, err
}

func (q *Queries) ListSpaces(ctx context.Context) ([]apigen.Space, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, name FROM spaces ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apigen.Space
	for rows.Next() {
		e, err := scanSpace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) CreateSpace(ctx context.Context, name string) (apigen.Space, error) {
	return scanSpace(q.db.QueryRowContext(ctx, `INSERT INTO spaces (name) VALUES (?)
RETURNING id, name`, name))
}

type UpdateSpaceParams struct {
	Name string
	ID   int64
}

func (q *Queries) UpdateSpace(ctx context.Context, arg UpdateSpaceParams) (apigen.Space, error) {
	return scanSpace(q.db.QueryRowContext(ctx, `UPDATE spaces SET name = ? WHERE id = ?
RETURNING id, name`, arg.Name, arg.ID))
}

const deleteSpace = `DELETE FROM spaces WHERE id = ?
`

func (q *Queries) DeleteSpace(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, deleteSpace, id)
	return err
}
