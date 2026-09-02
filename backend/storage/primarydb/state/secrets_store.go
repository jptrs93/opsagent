package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// This file implements secrets.Store on the primary Service. The
// secret_keyslots, secret_event_log, and system_secrets tables are
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
	rows := erru.Must(s.q.ListSecretKeyslots(context.Background()))
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

func (s *Service) UpsertSecretKeyslotLocked(k secrets.Keyslot) {
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
	rows := erru.Must(s.q.ListSecretVersionRecords(context.Background()))
	out := make([]secrets.Record, 0, len(rows))
	for _, r := range rows {
		out = append(out, secretVersionRecordRowToRecord(r))
	}
	return out
}

func nextSecretEvent(prev pq.SecretEventMeta, author int32, eventType int64) pq.InsertSecretEventParams {
	return pq.InsertSecretEventParams{
		EventTime:        time.Now().UnixMilli(),
		CreatedTime:      prev.CreatedTime,
		Author:           int64(author),
		SecretID:         prev.SecretID,
		Version:          prev.Version + 1,
		ValueVersion:     prev.ValueVersion,
		SpaceVersion:     prev.SpaceVersion,
		Name:             prev.Name,
		ValueDirectoryID: prev.ValueDirectoryID,
		SpaceID:          prev.SpaceID,
		EventType:        eventType,
	}
}

func (s *Service) appendSecretEventLocked(ctx context.Context, event pq.InsertSecretEventParams) {
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		event.GlobalSeq = seq
		_, err = q.InsertSecretEvent(ctx, event)
		return err
	}); err != nil {
		panic(fmt.Sprintf("append secret event: %v", err))
	}
}

func (s *Service) mustLatestSecretEventLocked(ctx context.Context, secretID int32) (pq.SecretEventMeta, bool) {
	e, err := s.q.GetLatestSecretEventMeta(ctx, int64(secretID))
	if errors.Is(err, sql.ErrNoRows) {
		return pq.SecretEventMeta{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetLatestSecretEventMeta: %v", err))
	}
	return e, e.EventType != pq.EventDelete
}

// CreateSecretWithVersion creates a new secret in directoryID (0 = the root)
// of spaceID with its first version. seal is called with the new identity id
// and version 1 inside the transaction, once both are known.
func (s *Service) CreateSecretWithVersionLocked(name string, spaceID, directoryID, author int32, seal secrets.SealFunc) (secrets.Record, error) {
	if !ValidValueName(name) {
		return secrets.Record{}, ErrValueNameInvalid
	}
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
	var record secrets.Record
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		seq := erru.Must(q.NextGlobalSeq(ctx))
		id := erru.Must(q.NextSecretID(ctx))
		sealed, err := seal(int32(id), 1)
		if err != nil {
			return err
		}
		rowID, err := q.InsertSecretEvent(ctx, pq.InsertSecretEventParams{
			GlobalSeq:        seq,
			EventTime:        now,
			CreatedTime:      now,
			Author:           int64(author),
			SecretID:         id,
			Version:          1,
			ValueVersion:     1,
			SpaceVersion:     1,
			Name:             name,
			ValueDirectoryID: dirID,
			SpaceID:          space,
			SmkVersion:       sql.NullInt64{Int64: int64(sealed.SMKVersion), Valid: true},
			Ciphertext:       sealed.Ciphertext,
			Nonce:            sealed.Nonce,
			EventType:        pq.EventCreate,
		})
		if err != nil {
			panic(fmt.Sprintf("InsertSecretEvent: %v", err))
		}
		record = secrets.Record{
			ID:         int32(rowID),
			SecretID:   int32(id),
			Name:       name,
			Version:    1,
			SpaceID:    int32(space),
			SMKVersion: sealed.SMKVersion,
			Ciphertext: sealed.Ciphertext,
			Nonce:      sealed.Nonce,
			CreatedAt:  now,
			Author:     author,
		}
		return nil
	}); err != nil {
		return secrets.Record{}, err
	}
	return record, nil
}

// AppendSecretVersionWithDeploymentUpdates appends an immutable secret version
// and optionally rolls the caller-asserted deployment references to the new
// row atomically. seal is called with the identity id and the next version
// number inside the transaction.
func (s *Service) AppendSecretVersionWithDeploymentUpdatesLocked(secretID, author int32, seal secrets.SealFunc, updateDeployments bool, expected []storage.DeploymentSpecVersion, afterCommit func(secrets.Record)) (secrets.Record, []int32, error) {
	ctx := context.Background()
	var record secrets.Record
	insert := func(q *pq.Queries, globalSeq int64) (int32, error) {
		prev, err := q.GetLatestSecretEventMeta(ctx, int64(secretID))
		if err == sql.ErrNoRows {
			return 0, ErrValueNotFound
		} else if err != nil {
			return 0, fmt.Errorf("get latest secret event: %w", err)
		}
		if prev.EventType == pq.EventDelete {
			return 0, ErrValueNotFound
		}
		version := prev.ValueVersion + 1
		sealed, err := seal(secretID, int32(version))
		if err != nil {
			return 0, err
		}
		event := nextSecretEvent(prev, author, pq.EventUpdate)
		event.ValueVersion = version
		event.SmkVersion = sql.NullInt64{Int64: int64(sealed.SMKVersion), Valid: true}
		event.Ciphertext = sealed.Ciphertext
		event.Nonce = sealed.Nonce
		event.GlobalSeq = globalSeq
		rowID, err := q.InsertSecretEvent(ctx, event)
		if err != nil {
			return 0, fmt.Errorf("insert secret value event: %w", err)
		}
		record = secrets.Record{
			ID:         int32(rowID),
			SecretID:   secretID,
			Name:       prev.Name,
			Version:    int32(version),
			SpaceID:    int32(prev.SpaceID),
			SMKVersion: sealed.SMKVersion,
			Ciphertext: sealed.Ciphertext,
			Nonce:      sealed.Nonce,
			CreatedAt:  event.EventTime,
			Author:     author,
		}
		return int32(rowID), nil
	}
	updatedDeployments, err := s.setVersionedValueWithDeploymentUpdatesLocked(
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

// RenameSecret renames the stable secret identity as an event. Value versions
// and their sealed bytes are untouched: the AAD binds the identity id, not
// the name.
func (s *Service) RenameSecretLocked(secretID int32, newName string) error {
	if !ValidValueName(newName) {
		return ErrValueNameInvalid
	}
	ctx := context.Background()
	prev, ok := s.mustLatestSecretEventLocked(ctx, secretID)
	if !ok {
		return ErrValueNotFound
	}
	if prev.Name == newName {
		return nil
	}
	if s.valueSiblingNameTakenLocked(ctx, s.q, prev.SpaceID, prev.ValueDirectoryID, newName, prev.SecretID, 0, 0) {
		return ErrValueAlreadyExists
	}
	event := nextSecretEvent(prev, 0, pq.EventUpdate)
	event.Name = newName
	s.appendSecretEventLocked(ctx, event)
	return nil
}

// MoveSecretDirectory moves a secret to another value directory (0 = the space
// root) in its own space. Value versions and their sealed bytes are untouched:
// the AAD binds the identity id, not the location.
func (s *Service) MoveSecretDirectory(secretID, newDirectoryID int32) (Secret, error) {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	prev, ok := s.mustLatestSecretEventLocked(ctx, secretID)
	if !ok {
		return Secret{}, ErrValueNotFound
	}
	dirID := int64(newDirectoryID)
	current := Secret{ID: prev.SecretID, Name: prev.Name, SpaceID: prev.SpaceID, ValueDirectoryID: prev.ValueDirectoryID, CreatedAt: prev.CreatedTime}
	if prev.ValueDirectoryID == dirID {
		return current, nil
	}
	if dirID != 0 {
		dir, err := s.q.GetValueDirectoryByID(ctx, dirID)
		if err == sql.ErrNoRows {
			return Secret{}, ErrValueDirectoryNotFound
		}
		if err != nil {
			panic(fmt.Sprintf("GetValueDirectoryByID: %v", err))
		}
		if dir.SpaceID != prev.SpaceID {
			return Secret{}, ErrSpaceMoveUnsupported
		}
	}
	if s.valueSiblingNameTakenLocked(ctx, s.q, prev.SpaceID, dirID, prev.Name, prev.SecretID, 0, 0) {
		return Secret{}, ErrValueAlreadyExists
	}
	event := nextSecretEvent(prev, 0, pq.EventUpdate)
	event.ValueDirectoryID = dirID
	s.appendSecretEventLocked(ctx, event)
	current.ValueDirectoryID = dirID
	return current, nil
}

// MoveSecretSpace moves a secret to another space, landing it in
// newDirectoryID there (0 = the destination space's root). Value versions and
// their sealed bytes are untouched: the AAD binds the identity id, not the
// location, so every pinned reference survives. A space change bumps the
// space facet with author as the acting user; a directory-only call appends
// no space history. Reference locality is the caller's law — the handler
// refuses the move while anything outside the destination space references
// the secret.
func (s *Service) MoveSecretSpaceLocked(secretID, newSpaceID, newDirectoryID, author int32) error {
	ctx := context.Background()

	prev, ok := s.mustLatestSecretEventLocked(ctx, secretID)
	if !ok {
		return ErrValueNotFound
	}
	spaceID := int64(normalizedUserSpaceID(newSpaceID))
	dirID := int64(newDirectoryID)
	if spaceID == prev.SpaceID && dirID == prev.ValueDirectoryID {
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
	if s.valueSiblingNameTakenLocked(ctx, s.q, spaceID, dirID, prev.Name, prev.SecretID, 0, 0) {
		return ErrValueAlreadyExists
	}
	event := nextSecretEvent(prev, author, pq.EventUpdate)
	event.ValueDirectoryID = dirID
	if spaceID != prev.SpaceID {
		event.SpaceID = spaceID
		event.SpaceVersion = prev.SpaceVersion + 1
	}
	s.appendSecretEventLocked(ctx, event)
	return nil
}

// DeleteSecret appends the terminal delete event. Value versions and their
// sealed bytes stay in the log, so the delete is recoverable at the DB level;
// current-state reads (including the Manager's startup record load) exclude
// the secret from here on and the name is freed.
func (s *Service) DeleteSecretLocked(secretID int32) error {
	ctx := context.Background()
	prev, ok := s.mustLatestSecretEventLocked(ctx, secretID)
	if !ok {
		return ErrValueNotFound
	}
	s.appendSecretEventLocked(ctx, nextSecretEvent(prev, 0, pq.EventDelete))
	return nil
}

// ListSecrets returns every live secret with its space and version logs,
// newest first, ordered by name. Never returns values or ciphertext.
func (s *Service) ListSecrets() []*apigen.Secret {
	events := erru.Must(s.q.ListAllSecretEventMetas(context.Background()))
	bySecret := groupSecretEvents(events)
	out := make([]*apigen.Secret, 0, len(bySecret))
	for _, group := range bySecret {
		if group[len(group)-1].EventType == pq.EventDelete {
			continue
		}
		out = append(out, secretFromEvents(group))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fs.Name < out[j].Fs.Name })
	return out
}

func groupSecretEvents(events []pq.SecretEventMeta) [][]pq.SecretEventMeta {
	var out [][]pq.SecretEventMeta
	for _, e := range events {
		if n := len(out); n > 0 && out[n-1][0].SecretID == e.SecretID {
			out[n-1] = append(out[n-1], e)
			continue
		}
		out = append(out, []pq.SecretEventMeta{e})
	}
	return out
}

// GetSecret returns the secret with its space and version logs, or false when
// the secret does not exist or is deleted. Never returns values or ciphertext.
func (s *Service) GetSecret(secretID int32) (*apigen.Secret, bool) {
	ctx := context.Background()
	if _, ok := s.mustLatestSecretEventLocked(ctx, secretID); !ok {
		return nil, false
	}
	events := erru.Must(s.q.ListSecretEventMetas(ctx, int64(secretID)))
	if len(events) == 0 {
		return nil, false
	}
	return secretFromEvents(events), true
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

// SecretVersionIDs returns every value version row id of the secret — the set
// a deployment env ref or setting could pin.
func (s *Service) SecretVersionIDs(secretID int32) []int32 {
	rows := erru.Must(s.q.ListSecretVersionIDsBySecretID(context.Background(), int64(secretID)))
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

func (s *Service) UpsertSystemSecretLocked(r secrets.SystemRecord) {
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
