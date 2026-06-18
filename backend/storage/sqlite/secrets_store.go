package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jptrs93/opsagent/backend/secrets"
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
			Group:      r.SecretGroup,
			SMKVersion: int32(r.SmkVersion),
			Ciphertext: r.Ciphertext,
			Nonce:      r.Nonce,
			CreatedAt:  r.CreatedAt,
			UpdatedAt:  r.UpdatedAt,
			UpdatedBy:  int32(r.UpdatedBy),
		})
	}
	return out
}

func (s *PrimaryStorage) NextSecretID() int32 {
	id, err := s.q.GetNextSecretID(context.Background())
	if err != nil {
		panic(fmt.Sprintf("GetNextSecretID: %v", err))
	}
	return int32(id)
}

func (s *PrimaryStorage) UpsertSecret(r secrets.Record) {
	if err := s.q.UpsertSecret(context.Background(), UpsertSecretParams{
		ID:          int64(r.ID),
		Name:        r.Name,
		SecretGroup: r.Group,
		SmkVersion:  int64(r.SMKVersion),
		Ciphertext:  r.Ciphertext,
		Nonce:       r.Nonce,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
		UpdatedBy:   int64(r.UpdatedBy),
	}); err != nil {
		panic(fmt.Sprintf("UpsertSecret: %v", err))
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
