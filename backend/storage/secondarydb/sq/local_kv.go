package sq

import (
	"context"
)

// DeleteLocalKV removes one machine-local key. Missing keys are a no-op.
func (q *Queries) DeleteLocalKV(ctx context.Context, key string) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM local_kv WHERE key = ?`, key)
	return err
}
