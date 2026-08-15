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

// This file implements secrets.Store on the primary Service. Secret versions
// live in the global events table (blob = SecretEnvelope); secret_displays
// owns the mutable name/directory/space labels. All secret tables are
// primary-only and are never replicated to secondaries.
//
// The Manager owns the SMK and all crypto; this layer owns the identities,
// versions, and the shared secrets/configs namespace law. Sealing happens
// through a caller-supplied secrets.SealFunc inside the write transaction,
// after the version's event row is pre-allocated, because the AAD binds the
// event id.

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
	ctx := context.Background()
	displays, err := s.q.ListSecretDisplays(ctx)
	if err != nil {
		panic(fmt.Sprintf("ListSecretDisplays: %v", err))
	}
	events, err := s.q.ListEventsByType(ctx, eventTypeSecret)
	if err != nil {
		panic(fmt.Sprintf("ListEventsByType: %v", err))
	}
	byEntity := make(map[int64][]pq.Event)
	for _, e := range events {
		if e.Action != eventActionDelete {
			byEntity[e.EntityID] = append(byEntity[e.EntityID], e)
		}
	}
	var out []secrets.Record
	for _, d := range displays {
		for i, e := range byEntity[d.ID] {
			out = append(out, secretRecordFromEvent(d, e, i+1))
		}
	}
	return out
}

// CreateSecretWithVersion creates a new secret in directoryID (0 = the root)
// of spaceID with its first version. seal is called with the pre-allocated
// version event id inside the transaction.
func (s *Service) CreateSecretWithVersion(name string, spaceID, directoryID, createdBy int32, seal secrets.SealFunc) (secrets.Record, error) {
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
	var record secrets.Record
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		maxID, err := q.MaxEventEntityID(ctx, eventTypeSecret)
		if err != nil {
			panic(fmt.Sprintf("MaxEventEntityID: %v", err))
		}
		id := maxID + 1
		eventID, sealed, err := insertSealedSecretEvent(ctx, q, id, eventActionCreate, now, createdBy, seal)
		if err != nil {
			return err
		}
		if err := q.InsertSecretDisplay(ctx, pq.InsertSecretDisplayParams{
			ID:          id,
			SpaceID:     space,
			Name:        name,
			DirectoryID: dirID,
			UpdatedAt:   now,
			UpdatedBy:   int64(createdBy),
		}); err != nil {
			panic(fmt.Sprintf("InsertSecretDisplay: %v", err))
		}
		record = secrets.Record{
			ID:         int32(eventID),
			SecretID:   int32(id),
			Name:       name,
			Version:    1,
			SpaceID:    int32(space),
			SMKVersion: sealed.SMKVersion,
			Ciphertext: sealed.Ciphertext,
			Nonce:      sealed.Nonce,
			CreatedAt:  now,
			CreatedBy:  createdBy,
		}
		return nil
	}); err != nil {
		return secrets.Record{}, err
	}
	return record, nil
}

// insertSealedSecretEvent pre-allocates the version's event row so seal can
// bind the event id, then writes the envelope blob back onto it.
func insertSealedSecretEvent(ctx context.Context, q *pq.Queries, entityID int64, action int64, ts int64, createdBy int32, seal secrets.SealFunc) (int64, secrets.SealedValue, error) {
	eventID, err := q.InsertEvent(ctx, pq.InsertEventParams{
		Ts:         ts,
		AuthorID:   int64(createdBy),
		EntityType: eventTypeSecret,
		EntityID:   entityID,
		Action:     action,
		Blob:       []byte{},
	})
	if err != nil {
		return 0, secrets.SealedValue{}, fmt.Errorf("insert secret event: %w", err)
	}
	sealed, err := seal(int32(eventID))
	if err != nil {
		return 0, secrets.SealedValue{}, err
	}
	env := &apigen.SecretEnvelope{
		SmkVersion: sealed.SMKVersion,
		Nonce:      sealed.Nonce,
		Ciphertext: sealed.Ciphertext,
	}
	if err := q.UpdateEventBlob(ctx, pq.UpdateEventBlobParams{Blob: env.Encode(), ID: eventID}); err != nil {
		return 0, secrets.SealedValue{}, fmt.Errorf("write secret envelope: %w", err)
	}
	return eventID, sealed, nil
}

// AppendSecretVersionWithDeploymentUpdates appends an immutable secret version
// and optionally rolls the caller-asserted deployment references to the new
// event atomically. seal is called with the pre-allocated event id inside the
// transaction.
func (s *Service) AppendSecretVersionWithDeploymentUpdates(secretID, createdBy int32, seal secrets.SealFunc, updateDeployments bool, expected []storage.DeploymentConfigVersion, afterCommit func(secrets.Record)) (secrets.Record, []int32, error) {
	ctx := context.Background()
	var record secrets.Record
	insert := func(q *pq.Queries) (int32, error) {
		d, err := q.GetSecretDisplayByID(ctx, int64(secretID))
		if err == sql.ErrNoRows {
			return 0, ErrValueNotFound
		} else if err != nil {
			return 0, fmt.Errorf("get secret display: %w", err)
		}
		events, err := q.ListEventsByEntity(ctx, pq.ListEventsByEntityParams{EntityType: eventTypeSecret, EntityID: d.ID})
		if err != nil {
			return 0, fmt.Errorf("list secret events: %w", err)
		}
		now := time.Now().UnixMilli()
		eventID, sealed, err := insertSealedSecretEvent(ctx, q, d.ID, eventActionUpdate, now, createdBy, seal)
		if err != nil {
			return 0, err
		}
		record = secrets.Record{
			ID:         int32(eventID),
			SecretID:   secretID,
			Name:       d.Name,
			Version:    int32(len(valueEvents(events)) + 1),
			SpaceID:    int32(d.SpaceID),
			SMKVersion: sealed.SMKVersion,
			Ciphertext: sealed.Ciphertext,
			Nonce:      sealed.Nonce,
			CreatedAt:  now,
			CreatedBy:  createdBy,
		}
		return int32(eventID), nil
	}
	updatedDeployments, err := s.setVersionedValueWithDeploymentUpdates(
		secretValueReference, secretID, updateDeployments, expected, createdBy, insert,
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
// bytes are untouched: the AAD binds ids, not the name.
func (s *Service) RenameSecret(secretID int32, newName string) error {
	if !ValidValueName(newName) {
		return ErrValueNameInvalid
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := context.Background()
	d, err := s.q.GetSecretDisplayByID(ctx, int64(secretID))
	if err == sql.ErrNoRows {
		return ErrValueNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetSecretDisplayByID: %v", err))
	}
	if d.Name == newName {
		return nil
	}
	if s.valueSiblingNameTakenLocked(ctx, s.q, d.SpaceID, d.DirectoryID, newName, d.ID, 0, 0) {
		return ErrValueAlreadyExists
	}
	if err := s.q.RenameSecretDisplay(ctx, pq.RenameSecretDisplayParams{Name: newName, UpdatedAt: time.Now().UnixMilli(), UpdatedBy: d.UpdatedBy, ID: d.ID}); err != nil {
		panic(fmt.Sprintf("RenameSecretDisplay: %v", err))
	}
	return nil
}

// MoveSecretDirectory moves a secret to another value directory (0 = the space
// root) in its own space. Version events and their sealed bytes are untouched.
func (s *Service) MoveSecretDirectory(secretID, newDirectoryID int32) error {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	d, err := s.q.GetSecretDisplayByID(ctx, int64(secretID))
	if err == sql.ErrNoRows {
		return ErrValueNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetSecretDisplayByID: %v", err))
	}
	dirID := int64(newDirectoryID)
	if d.DirectoryID == dirID {
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
		if dir.SpaceID != d.SpaceID {
			return ErrSpaceMoveUnsupported
		}
	}
	if s.valueSiblingNameTakenLocked(ctx, s.q, d.SpaceID, dirID, d.Name, d.ID, 0, 0) {
		return ErrValueAlreadyExists
	}
	if err := s.q.SetSecretDisplayDirectory(ctx, pq.SetSecretDisplayDirectoryParams{DirectoryID: dirID, UpdatedAt: time.Now().UnixMilli(), UpdatedBy: d.UpdatedBy, ID: d.ID}); err != nil {
		panic(fmt.Sprintf("SetSecretDisplayDirectory: %v", err))
	}
	return nil
}

// MoveSecretSpace moves a secret to another space, landing it in
// newDirectoryID there (0 = the destination space's root). Reference locality
// is the caller's law — the handler refuses the move while anything outside
// the destination space references the secret.
func (s *Service) MoveSecretSpace(secretID, newSpaceID, newDirectoryID int32) error {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	d, err := s.q.GetSecretDisplayByID(ctx, int64(secretID))
	if err == sql.ErrNoRows {
		return ErrValueNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetSecretDisplayByID: %v", err))
	}
	spaceID := int64(normalizedUserSpaceID(newSpaceID))
	dirID := int64(newDirectoryID)
	if spaceID == d.SpaceID && dirID == d.DirectoryID {
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
		if dir.SpaceID != spaceID {
			return ErrValueDirectoryNotFound
		}
	}
	if s.valueSiblingNameTakenLocked(ctx, s.q, spaceID, dirID, d.Name, d.ID, 0, 0) {
		return ErrValueAlreadyExists
	}
	if err := s.q.SetSecretDisplaySpace(ctx, pq.SetSecretDisplaySpaceParams{SpaceID: spaceID, DirectoryID: dirID, UpdatedAt: time.Now().UnixMilli(), UpdatedBy: d.UpdatedBy, ID: d.ID}); err != nil {
		panic(fmt.Sprintf("SetSecretDisplaySpace: %v", err))
	}
	return nil
}

// DeleteSecret excises every version's sealed bytes from the log, records a
// tombstone event, and removes the display row. Excision is what makes delete
// a real cryptographic erasure despite the append-only log.
func (s *Service) DeleteSecret(secretID int32) error {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := context.Background()
	if _, err := s.q.GetSecretDisplayByID(ctx, int64(secretID)); err == sql.ErrNoRows {
		return ErrValueNotFound
	} else if err != nil {
		panic(fmt.Sprintf("GetSecretDisplayByID: %v", err))
	}
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		if err := q.ClearEventBlobsByEntity(ctx, pq.ClearEventBlobsByEntityParams{EntityType: eventTypeSecret, EntityID: int64(secretID)}); err != nil {
			panic(fmt.Sprintf("ClearEventBlobsByEntity: %v", err))
		}
		if _, err := q.InsertEvent(ctx, pq.InsertEventParams{
			Ts:         time.Now().UnixMilli(),
			AuthorID:   0,
			EntityType: eventTypeSecret,
			EntityID:   int64(secretID),
			Action:     eventActionDelete,
			Blob:       []byte{},
		}); err != nil {
			panic(fmt.Sprintf("InsertEvent: %v", err))
		}
		if err := q.DeleteSecretDisplay(ctx, int64(secretID)); err != nil {
			panic(fmt.Sprintf("DeleteSecretDisplay: %v", err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("delete secret tx: %v", err))
	}
	return nil
}

// --- secret metas (no values, no ciphertext) ---

// ListSecretMetas returns every secret with its full version index, newest
// version first, ordered by name. Never returns values or ciphertext.
func (s *Service) ListSecretMetas() []*apigen.SecretMeta {
	ctx := context.Background()
	displays, err := s.q.ListSecretDisplays(ctx)
	if err != nil {
		panic(fmt.Sprintf("ListSecretDisplays: %v", err))
	}
	events, err := s.q.ListEventsByType(ctx, eventTypeSecret)
	if err != nil {
		panic(fmt.Sprintf("ListEventsByType: %v", err))
	}
	byEntity := make(map[int64][]pq.Event)
	for _, e := range events {
		if e.Action != eventActionDelete {
			byEntity[e.EntityID] = append(byEntity[e.EntityID], e)
		}
	}
	out := make([]*apigen.SecretMeta, 0, len(displays))
	for _, d := range displays {
		evs := byEntity[d.ID]
		if len(evs) == 0 {
			continue
		}
		out = append(out, secretMetaFromDisplay(d, evs))
	}
	return out
}

// GetSecretMeta returns one secret with its full version index, newest first.
func (s *Service) GetSecretMeta(secretID int32) (*apigen.SecretMeta, bool) {
	ctx := context.Background()
	d, err := s.q.GetSecretDisplayByID(ctx, int64(secretID))
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetSecretDisplayByID: %v", err))
	}
	events, err := s.q.ListEventsByEntity(ctx, pq.ListEventsByEntityParams{EntityType: eventTypeSecret, EntityID: d.ID})
	if err != nil {
		panic(fmt.Sprintf("ListEventsByEntity: %v", err))
	}
	evs := valueEvents(events)
	if len(evs) == 0 {
		return nil, false
	}
	return secretMetaFromDisplay(d, evs), true
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
	row, err := s.q.GetSecretDisplayByName(context.Background(), pq.GetSecretDisplayByNameParams{
		SpaceID:     int64(normalizedUserSpaceID(spaceID)),
		DirectoryID: 0,
		Name:        name,
	})
	if err == sql.ErrNoRows {
		return Secret{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetSecretDisplayByName: %v", err))
	}
	return row, true
}

// SecretVersionIDs returns every version event id of the secret — the set a
// deployment env ref or setting could pin.
func (s *Service) SecretVersionIDs(secretID int32) []int32 {
	events, err := s.q.ListEventsByEntity(context.Background(), pq.ListEventsByEntityParams{EntityType: eventTypeSecret, EntityID: int64(secretID)})
	if err != nil {
		panic(fmt.Sprintf("ListEventsByEntity: %v", err))
	}
	evs := valueEvents(events)
	ids := make([]int32, 0, len(evs))
	for _, e := range evs {
		ids = append(ids, int32(e.ID))
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
