package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jptrs93/opsagent/backend/lib/secrets"
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

func (s *PrimaryStorage) NextSecretVersion(name string) int32 {
	version, err := s.q.GetNextSecretVersion(context.Background(), name)
	if err != nil {
		panic(fmt.Sprintf("GetNextSecretVersion: %v", err))
	}
	return int32(version)
}

func (s *PrimaryStorage) InsertSecret(r secrets.Record) secrets.Record {
	row, err := s.q.InsertSecret(context.Background(), InsertSecretParams{
		Name:       r.Name,
		Version:    int64(r.Version),
		SpaceID:    int64(normalizedUserSpaceID(r.SpaceID)),
		SmkVersion: int64(r.SMKVersion),
		Ciphertext: r.Ciphertext,
		Nonce:      r.Nonce,
		CreatedAt:  r.CreatedAt,
		UpdatedBy:  int64(r.UpdatedBy),
	})
	if err != nil {
		panic(fmt.Sprintf("InsertSecret: %v", err))
	}
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
	tx, beginErr := s.db.BeginTx(ctx, nil)
	if beginErr != nil {
		panic(fmt.Sprintf("begin rename secrets tx: %v", beginErr))
	}
	defer tx.Rollback()
	for _, r := range records {
		res, updateErr := tx.ExecContext(ctx, `
UPDATE secrets
SET name = ?, smk_version = ?, ciphertext = ?, nonce = ?
WHERE id = ? AND name = ?
`, newName, int64(r.SMKVersion), r.Ciphertext, r.Nonce, int64(r.ID), name)
		if updateErr != nil {
			panic(fmt.Sprintf("RenameSecretRecords update: %v", updateErr))
		}
		if affected, rowsErr := res.RowsAffected(); rowsErr != nil {
			panic(fmt.Sprintf("RenameSecretRecords rows affected: %v", rowsErr))
		} else if affected != 1 {
			panic(fmt.Sprintf("RenameSecretRecords updated %d rows for id %d", affected, r.ID))
		}
	}
	if commitErr := tx.Commit(); commitErr != nil {
		panic(fmt.Sprintf("commit rename secrets tx: %v", commitErr))
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
