package pq

import (
	"bytes"
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

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

// UpdateSecretSeal replaces the sealed bytes of one value version: the
// secret_event_log row and every write log payload of that secret carrying
// that value version, in one transaction, so the log never holds bytes the
// table no longer does.
func (q *Queries) UpdateSecretSeal(ctx context.Context, row SecretSealRow, ciphertext, nonce []byte) error {
	return q.Tx(ctx, func(tx *Queries) error {
		if _, err := tx.db.ExecContext(ctx, `UPDATE secret_event_log SET ciphertext = ?, nonce = ? WHERE id = ?`, ciphertext, nonce, row.ID); err != nil {
			return err
		}
		return tx.resealLoggedPayloads(ctx, row, ciphertext, nonce)
	})
}

func (q *Queries) resealLoggedPayloads(ctx context.Context, row SecretSealRow, ciphertext, nonce []byte) error {
	type logged struct {
		seq, idx int64
		payload  []byte
	}
	rows, err := q.db.QueryContext(ctx, `SELECT seq, idx, payload FROM write_event_mutations WHERE entity_type = ? AND entity_id = ? AND payload IS NOT NULL ORDER BY seq, idx`,
		int64(apigen.CoreEntityType_CORE_ENTITY_SECRET), row.SecretID)
	if err != nil {
		return err
	}
	var entries []logged
	for rows.Next() {
		var l logged
		if err := rows.Scan(&l.seq, &l.idx, &l.payload); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, l := range entries {
		e, err := apigen.DecodeCoreEntity(l.payload)
		if err != nil {
			return fmt.Errorf("secret payload at seq %d: %w", l.seq, err)
		}
		if e.Secret == nil || int64(e.Secret.ValueVersion) != row.ValueVersion || !bytes.Equal(e.Secret.Ciphertext, row.Ciphertext) {
			continue
		}
		e.Secret.Ciphertext, e.Secret.Nonce = ciphertext, nonce
		if _, err := q.db.ExecContext(ctx, `UPDATE write_event_mutations SET payload = ? WHERE seq = ? AND idx = ?`, e.Encode(), l.seq, l.idx); err != nil {
			return err
		}
	}
	return nil
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
