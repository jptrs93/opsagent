package secrets

import (
	"context"
	"fmt"
	"log/slog"
)

// migrateSealsLocked is the one-time v0.0.614 move to the secret_id-only
// AEAD binding. Rows sealed under the (secret_id, value_version) binding or
// the unreleased (secret_id, seal_id) binding are opened under their old
// associated data and re-sealed in place, and every system_secrets row is
// appended to the event log in space 0 under the same binding. The legacy
// table and column are dropped once every row has moved; a row that opens
// under no known binding is logged and left, and the pass runs again at the
// next unlock. Caller must hold m.mu with m.smk set. Remove after every
// active cluster has rolled forward, per the migrations.sql history-note
// convention.
func (m *Manager) migrateSealsLocked() error {
	ctx := context.Background()
	pending, err := m.q.LegacySecretSealsPending(ctx)
	if err != nil || !pending {
		return err
	}
	rows, err := m.q.ListSecretSealRows(ctx)
	if err != nil {
		return err
	}
	failed := 0
	resealed := 0
	for _, row := range rows {
		if _, err := aeadOpen(m.smk, row.Ciphertext, row.Nonce, secretAAD(int32(row.SecretID))); err == nil {
			continue
		}
		legacy := legacyUserAAD(int32(row.SecretID), row.LegacySealID, int32(row.ValueVersion))
		pt, err := aeadOpen(m.smk, row.Ciphertext, row.Nonce, legacy)
		if err != nil {
			failed++
			slog.ErrorContext(m.ctx, "secret row opens under no known associated data; left as is", "err", err, "row", row.ID, "secret", row.SecretID)
			continue
		}
		ct, nonce, err := aeadSeal(m.smk, pt, secretAAD(int32(row.SecretID)))
		if err != nil {
			return err
		}
		if err := m.q.UpdateSecretSeal(ctx, row.ID, ct, nonce); err != nil {
			return err
		}
		resealed++
	}
	system, err := m.q.ListLegacySystemSecrets(ctx)
	if err != nil {
		return err
	}
	moved := 0
	for _, row := range system {
		if _, exists := idByNameInSpace(m.q, systemSpaceID, row.Name); exists {
			continue
		}
		pt, err := aeadOpen(m.smk, row.Ciphertext, row.Nonce, legacySystemAAD(row.Name))
		if err != nil {
			failed++
			slog.ErrorContext(m.ctx, "system secret opens under no known associated data; left as is", "err", err, "name", row.Name)
			continue
		}
		if err := m.setInternalLocked(row.Name, pt); err != nil {
			return fmt.Errorf("moving system secret %q into the event log: %w", row.Name, err)
		}
		moved++
	}
	for _, r := range ListVersionRecords(m.q) {
		m.cacheLocked(r)
	}
	if failed > 0 {
		slog.ErrorContext(m.ctx, fmt.Sprintf("secret seal migration incomplete: %d rows left under legacy bindings; retrying at the next unlock", failed))
		return nil
	}
	if err := m.q.DropLegacySecretArtifacts(ctx); err != nil {
		return err
	}
	slog.InfoContext(m.ctx, fmt.Sprintf("re-sealed %d secret rows under the secret_id binding and moved %d system secrets into the event log", resealed, moved))
	return nil
}

func legacyUserAAD(secretID int32, sealID string, valueVersion int32) []byte {
	if sealID == "" {
		sealID = fmt.Sprintf("v%d", valueVersion)
	}
	return []byte(fmt.Sprintf("opendeploy-secret:user:s%d:%s", secretID, sealID))
}

func legacySystemAAD(name string) []byte {
	return []byte("opendeploy-secret:system:" + name)
}
