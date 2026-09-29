package pq

import "context"

// The secret AEAD bound (secret_id, value_version) until v0.0.614, and the
// unreleased v0.0.614 builds bound (secret_id, seal_id). Both are re-sealed
// under the secret_id-only binding by the secrets manager once it holds the
// master key; the system_secrets table is folded into secret_event_log by
// the same pass. Its presence is the marker that the pass has not completed,
// which is why a fresh schema no longer creates it.

type SecretSealRow struct {
	ID           int64
	SecretID     int64
	ValueVersion int64
	LegacySealID string
	Ciphertext   []byte
	Nonce        []byte
}

type LegacySystemSecret struct {
	Name       string
	Ciphertext []byte
	Nonce      []byte
}

func (q *Queries) LegacySecretSealsPending(ctx context.Context) (bool, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'system_secrets'`).Scan(&n)
	return n > 0, err
}

func (q *Queries) ListSecretSealRows(ctx context.Context) ([]SecretSealRow, error) {
	cols, err := tableColumns(ctx, q.sqlDB(), "secret_event_log")
	if err != nil {
		return nil, err
	}
	sealExpr := "''"
	if cols["seal_id"] {
		sealExpr = "seal_id"
	}
	rows, err := q.db.QueryContext(ctx, `SELECT id, secret_id, value_version, `+sealExpr+`, ciphertext, nonce FROM secret_event_log ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecretSealRow
	for rows.Next() {
		var r SecretSealRow
		if err := rows.Scan(&r.ID, &r.SecretID, &r.ValueVersion, &r.LegacySealID, &r.Ciphertext, &r.Nonce); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) UpdateSecretSeal(ctx context.Context, id int64, ciphertext, nonce []byte) error {
	_, err := q.db.ExecContext(ctx, `UPDATE secret_event_log SET ciphertext = ?, nonce = ? WHERE id = ?`, ciphertext, nonce, id)
	return err
}

func (q *Queries) ListLegacySystemSecrets(ctx context.Context) ([]LegacySystemSecret, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT name, ciphertext, nonce FROM system_secrets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LegacySystemSecret
	for rows.Next() {
		var r LegacySystemSecret
		if err := rows.Scan(&r.Name, &r.Ciphertext, &r.Nonce); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) DropLegacySecretArtifacts(ctx context.Context) error {
	if _, err := q.db.ExecContext(ctx, `DROP TABLE IF EXISTS system_secrets`); err != nil {
		return err
	}
	cols, err := tableColumns(ctx, q.sqlDB(), "secret_event_log")
	if err != nil {
		return err
	}
	if cols["seal_id"] {
		_, err = q.db.ExecContext(ctx, `ALTER TABLE secret_event_log DROP COLUMN seal_id`)
	}
	return err
}
