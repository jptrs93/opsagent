package state

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// This file implements secrets.Store on the primary Service. The
// secret_keyslots, secrets, secret_versions, and system_secrets tables are
// primary-only and are never replicated to secondaries (the cluster feeder
// ships only deployment configs/status).
//
// The Manager owns the SMK and all crypto; this layer owns the identities,
// versions, and the shared secrets/configs namespace law. Sealing happens
// through a caller-supplied secrets.SealFunc inside the write transaction,
// because the id-and-version-bound AAD needs the identity id before the
// ciphertext can exist. Writes follow the store-wide panic-on-failure
// convention.

func (s *Service) ListSecretKeyslots() []secrets.Keyslot {
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

func (s *Service) UpsertSecretKeyslot(k secrets.Keyslot) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := s.q.UpsertSecretKeyslot(context.Background(), pq.UpsertSecretKeyslotParams{
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

func (s *Service) ListSecretVersionRecords() []secrets.Record {
	rows, err := s.q.ListSecretVersionRecords(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListSecretVersionRecords: %v", err))
	}
	out := make([]secrets.Record, 0, len(rows))
	for _, r := range rows {
		out = append(out, secrets.Record{
			ID:         int32(r.ID),
			SecretID:   int32(r.SecretID),
			Name:       r.Name,
			Version:    int32(r.Version),
			SpaceID:    int32(r.SpaceID),
			SMKVersion: int32(r.SmkVersion),
			Ciphertext: r.Ciphertext,
			Nonce:      r.Nonce,
			CreatedAt:  r.CreatedAt,
			Author:     int32(r.Author),
		})
	}
	return out
}

// CreateSecretWithVersion creates a new secret in directoryID (0 = the root)
// of spaceID with its first version. seal is called with the new identity id
// and version 1 inside the transaction, once both are known.
func (s *Service) CreateSecretWithVersion(name string, spaceID, directoryID, author int32, seal secrets.SealFunc) (secrets.Record, error) {
	if !ValidValueName(name) {
		return secrets.Record{}, ErrValueNameInvalid
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := context.Background()
	space := int64(normalizedUserSpaceID(spaceID))
	dirID, err := s.resolveValueDirectoryLocked(ctx, space, directoryID)
	if err != nil {
		return secrets.Record{}, err
	}
	if s.valueSiblingNameTakenLocked(ctx, s.q, space, dirID, name, 0, 0, 0) {
		return secrets.Record{}, ErrValueAlreadyExists
	}
	now := time.Now().UnixMilli()
	var row Secret
	var version pq.SecretVersion
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			panic(fmt.Sprintf("NextGlobalSeq: %v", err))
		}
		id, err := q.InsertSecretRow(ctx, pq.InsertSecretRowParams{
			Name:             name,
			ValueDirectoryID: dirID,
			CreatedAt:        now,
		})
		if err != nil {
			panic(fmt.Sprintf("InsertSecretRow: %v", err))
		}
		if err := q.InsertSecretSpaceRow(ctx, pq.InsertSecretSpaceRowParams{
			SecretID:  id,
			Author:    int64(author),
			CreatedAt: now,
			SpaceID:   space,
			GlobalSeq: seq,
		}); err != nil {
			panic(fmt.Sprintf("InsertSecretSpaceRow: %v", err))
		}
		row = Secret{ID: id, Name: name, SpaceID: space, ValueDirectoryID: dirID, CreatedAt: now}
		sealed, err := seal(int32(row.ID), 1)
		if err != nil {
			return err
		}
		version, err = q.InsertSecretVersion(ctx, pq.InsertSecretVersionParams{
			SecretID:   row.ID,
			Version:    1,
			SmkVersion: int64(sealed.SMKVersion),
			Ciphertext: sealed.Ciphertext,
			Nonce:      sealed.Nonce,
			CreatedAt:  now,
			Author:     int64(author),
			GlobalSeq:  seq,
		})
		if err != nil {
			panic(fmt.Sprintf("InsertSecretVersion: %v", err))
		}
		return nil
	}); err != nil {
		return secrets.Record{}, err
	}
	return secretVersionRecord(row, version), nil
}

// AppendSecretVersionWithDeploymentUpdates appends an immutable secret version
// and optionally rolls the caller-asserted deployment references to the new
// row atomically. seal is called with the identity id and the next version
// number inside the transaction.
func (s *Service) AppendSecretVersionWithDeploymentUpdates(secretID, author int32, seal secrets.SealFunc, updateDeployments bool, expected []storage.DeploymentConfigVersion, afterCommit func(secrets.Record)) (secrets.Record, []int32, error) {
	ctx := context.Background()
	var record secrets.Record
	insert := func(q *pq.Queries, globalSeq int64) (int32, error) {
		identity, err := q.GetSecretRowByID(ctx, int64(secretID))
		if err == sql.ErrNoRows {
			return 0, ErrValueNotFound
		} else if err != nil {
			return 0, fmt.Errorf("get secret row: %w", err)
		}
		version, err := q.GetNextSecretVersionNumber(ctx, int64(secretID))
		if err != nil {
			return 0, fmt.Errorf("get next secret version: %w", err)
		}
		sealed, err := seal(secretID, int32(version))
		if err != nil {
			return 0, err
		}
		row, err := q.InsertSecretVersion(ctx, pq.InsertSecretVersionParams{
			SecretID:   int64(secretID),
			Version:    version,
			SmkVersion: int64(sealed.SMKVersion),
			Ciphertext: sealed.Ciphertext,
			Nonce:      sealed.Nonce,
			CreatedAt:  time.Now().UnixMilli(),
			Author:     int64(author),
			GlobalSeq:  globalSeq,
		})
		if err != nil {
			return 0, fmt.Errorf("insert secret version: %w", err)
		}
		record = secretVersionRecord(identity, row)
		return int32(row.ID), nil
	}
	updatedDeployments, err := s.setVersionedValueWithDeploymentUpdates(
		secretValueReference, secretID, updateDeployments, expected, author, insert,
		func(_ []int32) {
			if afterCommit != nil {
				afterCommit(record)
			}
		})
	if err != nil {
		return secrets.Record{}, nil, err
	}
	return record, updatedDeployments, nil
}

// RenameSecret renames the stable secret identity. Versions and their sealed
// bytes are untouched: the AAD binds the identity id, not the name.
func (s *Service) RenameSecret(secretID int32, newName string) error {
	if !ValidValueName(newName) {
		return ErrValueNameInvalid
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := context.Background()
	row, err := s.q.GetSecretRowByID(ctx, int64(secretID))
	if err == sql.ErrNoRows {
		return ErrValueNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetSecretRowByID: %v", err))
	}
	if row.Name == newName {
		return nil
	}
	if s.valueSiblingNameTakenLocked(ctx, s.q, row.SpaceID, row.ValueDirectoryID, newName, row.ID, 0, 0) {
		return ErrValueAlreadyExists
	}
	if err := s.q.RenameSecretRow(ctx, pq.RenameSecretRowParams{Name: newName, ID: row.ID}); err != nil {
		panic(fmt.Sprintf("RenameSecretRow: %v", err))
	}
	return nil
}

// MoveSecretDirectory moves a secret to another value directory (0 = the space
// root) in its own space. Version rows and their sealed bytes are untouched:
// the AAD binds the identity id, not the location.
func (s *Service) MoveSecretDirectory(secretID, newDirectoryID int32) (Secret, error) {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	row, err := s.q.GetSecretRowByID(ctx, int64(secretID))
	if err == sql.ErrNoRows {
		return Secret{}, ErrValueNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetSecretRowByID: %v", err))
	}
	dirID := int64(newDirectoryID)
	if row.ValueDirectoryID == dirID {
		return row, nil
	}
	if dirID != 0 {
		dir, err := s.q.GetValueDirectoryByID(ctx, dirID)
		if err == sql.ErrNoRows {
			return Secret{}, ErrValueDirectoryNotFound
		}
		if err != nil {
			panic(fmt.Sprintf("GetValueDirectoryByID: %v", err))
		}
		if dir.SpaceID != row.SpaceID {
			return Secret{}, ErrSpaceMoveUnsupported
		}
	}
	if s.valueSiblingNameTakenLocked(ctx, s.q, row.SpaceID, dirID, row.Name, row.ID, 0, 0) {
		return Secret{}, ErrValueAlreadyExists
	}
	if err := s.q.SetSecretValueDirectoryID(ctx, pq.SetSecretValueDirectoryIDParams{ValueDirectoryID: dirID, ID: row.ID}); err != nil {
		panic(fmt.Sprintf("SetSecretValueDirectoryID: %v", err))
	}
	row.ValueDirectoryID = dirID
	return row, nil
}

// MoveSecretSpace moves a secret to another space, landing it in
// newDirectoryID there (0 = the destination space's root). Version rows and
// their sealed bytes are untouched: the AAD binds the identity id, not the
// location, so every pinned reference survives. A space change appends to the
// secret_spaces log with author as the acting user. Reference locality is the
// caller's law — the handler refuses the move while anything outside the
// destination space references the secret.
func (s *Service) MoveSecretSpace(secretID, newSpaceID, newDirectoryID, author int32) error {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	row, err := s.q.GetSecretRowByID(ctx, int64(secretID))
	if err == sql.ErrNoRows {
		return ErrValueNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetSecretRowByID: %v", err))
	}
	spaceID := int64(normalizedUserSpaceID(newSpaceID))
	dirID := int64(newDirectoryID)
	if spaceID == row.SpaceID && dirID == row.ValueDirectoryID {
		return nil
	}
	if dirID != 0 {
		dir, err := s.q.GetValueDirectoryByID(ctx, dirID)
		if err == sql.ErrNoRows {
			return ErrValueDirectoryNotFound
		}
		if err != nil {
			panic(fmt.Sprintf("GetValueDirectoryByID: %v", err))
		}
		// A directory in any space but the destination reads as absent, matching
		// the create path's treatment of foreign-space directories.
		if dir.SpaceID != spaceID {
			return ErrValueDirectoryNotFound
		}
	}
	if s.valueSiblingNameTakenLocked(ctx, s.q, spaceID, dirID, row.Name, row.ID, 0, 0) {
		return ErrValueAlreadyExists
	}
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		if spaceID != row.SpaceID {
			seq, err := q.NextGlobalSeq(ctx)
			if err != nil {
				panic(fmt.Sprintf("NextGlobalSeq: %v", err))
			}
			if err := q.InsertSecretSpaceRow(ctx, pq.InsertSecretSpaceRowParams{
				SecretID:  row.ID,
				Author:    int64(author),
				CreatedAt: time.Now().UnixMilli(),
				SpaceID:   spaceID,
				GlobalSeq: seq,
			}); err != nil {
				panic(fmt.Sprintf("InsertSecretSpaceRow: %v", err))
			}
		}
		if err := q.SetSecretValueDirectoryID(ctx, pq.SetSecretValueDirectoryIDParams{ValueDirectoryID: dirID, ID: row.ID}); err != nil {
			panic(fmt.Sprintf("SetSecretValueDirectoryID: %v", err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("secret space move tx: %v", err))
	}
	return nil
}

// DeleteSecret soft-deletes the secret identity. Version rows and their
// sealed bytes stay in place, so the delete is recoverable at the DB level;
// reads (including the Manager's startup record load) exclude the secret from
// here on.
func (s *Service) DeleteSecret(secretID int32) error {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := context.Background()
	if _, err := s.q.GetSecretRowByID(ctx, int64(secretID)); err == sql.ErrNoRows {
		return ErrValueNotFound
	} else if err != nil {
		panic(fmt.Sprintf("GetSecretRowByID: %v", err))
	}
	if err := s.q.SoftDeleteSecretRow(ctx, pq.SoftDeleteSecretRowParams{
		DeletedAt: time.Now().UnixMilli(),
		ID:        int64(secretID),
	}); err != nil {
		panic(fmt.Sprintf("SoftDeleteSecretRow: %v", err))
	}
	return nil
}

// ListSecrets returns every secret with its space and version logs, newest
// first, ordered by name. Never returns values or ciphertext.
func (s *Service) ListSecrets() []*apigen.Secret {
	ctx := context.Background()
	rows, err := s.q.ListSecretRows(ctx)
	if err != nil {
		panic(fmt.Sprintf("ListSecretRows: %v", err))
	}
	versions, err := s.q.ListSecretVersionMetas(ctx)
	if err != nil {
		panic(fmt.Sprintf("ListSecretVersionMetas: %v", err))
	}
	spaceRows, err := s.q.ListSecretSpaceRows(ctx)
	if err != nil {
		panic(fmt.Sprintf("ListSecretSpaceRows: %v", err))
	}
	versionsBySecret := make(map[int64][]pq.ListSecretVersionMetasRow, len(rows))
	for _, v := range versions {
		versionsBySecret[v.SecretID] = append(versionsBySecret[v.SecretID], v)
	}
	spacesBySecret := make(map[int64][]pq.SecretSpace, len(rows))
	for _, sp := range spaceRows {
		spacesBySecret[sp.SecretID] = append(spacesBySecret[sp.SecretID], sp)
	}
	out := make([]*apigen.Secret, 0, len(rows))
	for _, sec := range rows {
		vs := versionsBySecret[sec.ID]
		if len(vs) == 0 {
			continue
		}
		out = append(out, secretFromParts(sec, spacesBySecret[sec.ID], vs))
	}
	return out
}

// GetSecret returns the secret with its space and version logs, or false when
// the secret does not exist or has no version. Never returns values or
// ciphertext.
func (s *Service) GetSecret(secretID int32) (*apigen.Secret, bool) {
	ctx := context.Background()
	sec, err := s.q.GetSecretRowByID(ctx, int64(secretID))
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetSecretRowByID: %v", err))
	}
	rows, err := s.q.ListSecretVersionsBySecretID(ctx, sec.ID)
	if err != nil {
		panic(fmt.Sprintf("ListSecretVersionsBySecretID: %v", err))
	}
	if len(rows) == 0 {
		return nil, false
	}
	versions := make([]pq.ListSecretVersionMetasRow, 0, len(rows))
	for _, r := range rows {
		versions = append(versions, pq.ListSecretVersionMetasRow(r))
	}
	spaces, err := s.q.ListSecretSpaceRowsBySecretID(ctx, sec.ID)
	if err != nil {
		panic(fmt.Sprintf("ListSecretSpaceRowsBySecretID: %v", err))
	}
	return secretFromParts(sec, spaces, versions), true
}

// GetSecretIDByName implements the Manager's name lookup for install/restore
// flows: the root directory of spaceID.
func (s *Service) GetSecretIDByName(spaceID int32, name string) (int32, bool) {
	row, ok := s.GetSecretInRootByName(spaceID, name)
	if !ok {
		return 0, false
	}
	return int32(row.ID), true
}

// GetSecretInRootByName finds a secret identity by name in the root directory
// of spaceID.
func (s *Service) GetSecretInRootByName(spaceID int32, name string) (Secret, bool) {
	row, err := s.q.GetSecretInDirectoryByName(context.Background(), pq.GetSecretInDirectoryByNameParams{
		SpaceID:          int64(normalizedUserSpaceID(spaceID)),
		ValueDirectoryID: 0,
		Name:             name,
	})
	if err == sql.ErrNoRows {
		return Secret{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetSecretInDirectoryByName: %v", err))
	}
	return row, true
}

// SecretVersionIDs returns every version row id of the secret — the set a
// deployment env ref or setting could pin.
func (s *Service) SecretVersionIDs(secretID int32) []int32 {
	rows, err := s.q.ListSecretVersionIDsBySecretID(context.Background(), int64(secretID))
	if err != nil {
		panic(fmt.Sprintf("ListSecretVersionIDsBySecretID: %v", err))
	}
	ids := make([]int32, 0, len(rows))
	for _, id := range rows {
		ids = append(ids, int32(id))
	}
	return ids
}

func (s *Service) GetSystemSecret(name string) (secrets.SystemRecord, bool) {
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

func (s *Service) UpsertSystemSecret(r secrets.SystemRecord) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := s.q.UpsertSystemSecret(context.Background(), pq.UpsertSystemSecretParams{
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
