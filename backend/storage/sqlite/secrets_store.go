package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage"
)

// This file implements secrets.Store on the primary PrimaryStorage. The
// secret_keyslots, secrets, and system_secrets tables are primary-only and are
// never replicated to secondaries (the cluster feeder ships only deployment
// configs/status).
//
// These are thin DB passthroughs: the Manager owns the in-memory cache and SMK
// and serialises access, so no extra locking is needed here. Writes follow the
// store-wide panic-on-failure convention.

func (s *PrimaryStorage) ListSecretKeyslots() []secrets.Keyslot {
	rows, err := s.q.ListSecretKeyslots(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListSecretKeyslots: %v", err))
	}
	out := make([]secrets.Keyslot, 0, len(rows))
	for _, r := range rows {
		out = append(out, secrets.Keyslot{
			Slot:       r.Slot,
			SMKVersion: int32(r.SmkVersion),
			WrappedSMK: r.WrappedSmk,
			Nonce:      r.Nonce,
			KDFSalt:    r.KdfSalt,
			CreatedAt:  r.CreatedAt,
		})
	}
	return out
}

func (s *PrimaryStorage) UpsertSecretKeyslot(k secrets.Keyslot) {
	if err := s.q.UpsertSecretKeyslot(context.Background(), UpsertSecretKeyslotParams{
		Slot:       k.Slot,
		SmkVersion: int64(k.SMKVersion),
		WrappedSmk: k.WrappedSMK,
		Nonce:      k.Nonce,
		KdfSalt:    k.KDFSalt,
		CreatedAt:  k.CreatedAt,
	}); err != nil {
		panic(fmt.Sprintf("UpsertSecretKeyslot: %v", err))
	}
}

func (s *PrimaryStorage) ListSecrets() []secrets.Record {
	rows, err := s.q.ListSecrets(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListSecrets: %v", err))
	}
	out := make([]secrets.Record, 0, len(rows))
	for _, r := range rows {
		out = append(out, secrets.Record{
			ID:         int32(r.ID),
			Name:       r.Name,
			Version:    int32(r.Version),
			SpaceID:    int32(r.SpaceID),
			SMKVersion: int32(r.SmkVersion),
			Ciphertext: r.Ciphertext,
			Nonce:      r.Nonce,
			CreatedAt:  r.CreatedAt,
			UpdatedBy:  int32(r.UpdatedBy),
		})
	}
	return out
}

func (s *PrimaryStorage) InsertSecretWithDeploymentUpdates(r secrets.Record, updateDeployments bool, expected []storage.DeploymentConfigVersion, afterCommit func(secrets.Record)) (secrets.Record, []int32, error) {
	var row Secret
	updatedDeployments, err := s.setVersionedValueWithDeploymentUpdates(
		secretValueReference,
		r.Name,
		updateDeployments,
		expected,
		r.UpdatedBy,
		func(q *Queries) (int32, error) {
			version, err := q.GetNextSecretVersion(context.Background(), r.Name)
			if err != nil {
				return 0, fmt.Errorf("get next secret version: %w", err)
			}
			row, err = q.InsertSecret(context.Background(), InsertSecretParams{
				Name:       r.Name,
				Version:    version,
				SpaceID:    int64(normalizedUserSpaceID(r.SpaceID)),
				SmkVersion: int64(r.SMKVersion),
				Ciphertext: r.Ciphertext,
				Nonce:      r.Nonce,
				CreatedAt:  r.CreatedAt,
				UpdatedBy:  int64(r.UpdatedBy),
			})
			if err != nil {
				return 0, fmt.Errorf("insert secret: %w", err)
			}
			return int32(row.ID), nil
		},
		func(_ []int32) {
			if afterCommit != nil {
				afterCommit(secretRowToRecord(row))
			}
		},
	)
	if err != nil {
		return secrets.Record{}, nil, err
	}
	return secretRowToRecord(row), updatedDeployments, nil
}

func secretRowToRecord(row Secret) secrets.Record {
	return secrets.Record{
		ID:         int32(row.ID),
		Name:       row.Name,
		Version:    int32(row.Version),
		SpaceID:    int32(row.SpaceID),
		SMKVersion: int32(row.SmkVersion),
		Ciphertext: row.Ciphertext,
		Nonce:      row.Nonce,
		CreatedAt:  row.CreatedAt,
		UpdatedBy:  int32(row.UpdatedBy),
	}
}

func (s *PrimaryStorage) RenameSecretRecords(name, newName string, records []secrets.Record) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin rename secrets tx: %v", err))
	}
	defer tx.Rollback()
	for _, r := range records {
		res, err := tx.ExecContext(ctx, `
UPDATE secrets
SET name = ?, smk_version = ?, ciphertext = ?, nonce = ?
WHERE id = ? AND name = ?
`, newName, int64(r.SMKVersion), r.Ciphertext, r.Nonce, int64(r.ID), name)
		if err != nil {
			panic(fmt.Sprintf("RenameSecretRecords update: %v", err))
		}
		if n, err := res.RowsAffected(); err != nil {
			panic(fmt.Sprintf("RenameSecretRecords rows affected: %v", err))
		} else if n != 1 {
			panic(fmt.Sprintf("RenameSecretRecords updated %d rows for id %d", n, r.ID))
		}
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit rename secrets tx: %v", err))
	}
}

func (s *PrimaryStorage) GetSystemSecret(name string) (secrets.SystemRecord, bool) {
	r, err := s.q.GetSystemSecret(context.Background(), name)
	if err == sql.ErrNoRows {
		return secrets.SystemRecord{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetSystemSecret: %v", err))
	}
	return secrets.SystemRecord{
		Name:       r.Name,
		SMKVersion: int32(r.SmkVersion),
		Ciphertext: r.Ciphertext,
		Nonce:      r.Nonce,
		CreatedAt:  r.CreatedAt,
		UpdatedAt:  r.UpdatedAt,
	}, true
}

func (s *PrimaryStorage) UpsertSystemSecret(r secrets.SystemRecord) {
	if err := s.q.UpsertSystemSecret(context.Background(), UpsertSystemSecretParams{
		Name:       r.Name,
		SmkVersion: int64(r.SMKVersion),
		Ciphertext: r.Ciphertext,
		Nonce:      r.Nonce,
		CreatedAt:  r.CreatedAt,
		UpdatedAt:  r.UpdatedAt,
	}); err != nil {
		panic(fmt.Sprintf("UpsertSystemSecret: %v", err))
	}
}

func (s *PrimaryStorage) DeleteSecret(name string) {
	if err := s.q.DeleteSecret(context.Background(), name); err != nil {
		panic(fmt.Sprintf("DeleteSecret: %v", err))
	}
}
