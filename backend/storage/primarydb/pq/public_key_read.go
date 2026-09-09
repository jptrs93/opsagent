package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func scanPublicKey(row scanner) (apigen.PublicKeyRecord, error) {
	var e apigen.PublicKeyRecord
	err := row.Scan(&e.Kid, &e.KeyBytes)
	return e, err
}

func (q *Queries) GetPublicKey(ctx context.Context, kid string) (apigen.PublicKeyRecord, error) {
	return scanPublicKey(q.db.QueryRowContext(ctx, `SELECT kid, key_bytes FROM public_keys WHERE kid = ?`, kid))
}
