package secrets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func keyslotOf(k apigen.SecretKeyslot) Keyslot {
	slot := Keyslot{ID: k.ID, SMKVersion: k.SmkVersion, WrappedSMK: k.WrappedSmk, Nonce: k.Nonce}
	switch w := k.Wrapping.Value; {
	case w.MachineKey != nil:
		slot.Kind, slot.NodeID = slotMachine, w.MachineKey.NodeID
	case w.RecoveryCode != nil:
		slot.Kind, slot.KDFSalt = slotRecovery, w.RecoveryCode.KdfSalt
	}
	return slot
}

func (k Keyslot) entity() apigen.SecretKeyslot {
	out := apigen.SecretKeyslot{ID: k.ID, SmkVersion: k.SMKVersion, WrappedSmk: k.WrappedSMK, Nonce: k.Nonce}
	if k.Kind == slotRecovery {
		out.Wrapping = apigen.KeyslotWrapping{Value: apigen.KeyslotWrappingValueOneof{RecoveryCode: &apigen.RecoveryCode{KdfSalt: k.KDFSalt}}}
	} else {
		out.Wrapping = apigen.KeyslotWrapping{Value: apigen.KeyslotWrappingValueOneof{MachineKey: &apigen.MachineKey{NodeID: k.NodeID}}}
	}
	return out
}

func listKeyslots(q *pq.Queries) []Keyslot {
	rows := erru.Must(q.ListSecretKeyslots(context.Background()))
	out := make([]Keyslot, 0, len(rows))
	for _, r := range rows {
		out = append(out, keyslotOf(r))
	}
	return out
}

func writeKeyslot(store *state.Service, k Keyslot, author int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		eventType := apigen.AuthzVerb_AUTHZ_VERB_CREATE
		if existing, ok := findSlot(listKeyslots(q), k.Kind, k.NodeID); ok {
			eventType = apigen.AuthzVerb_AUTHZ_VERB_UPDATE
			k.ID = existing.ID
		} else {
			id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT)
			if err != nil {
				return nil, err
			}
			k.ID = id
		}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), Author: author, EventType: eventType}
		return pq.NewUpdate(pq.SecretKeyslotMutation(meta, k.entity())), nil
	})
}

func recordOf(j pq.SecretVersionJoined) Record {
	return Record{
		SecretID: j.Secret.ID, Key: j.Secret.Key, Version: j.Version.ValueVersion, SpaceID: j.Secret.SpaceID,
		SMKVersion: j.Version.SmkVersion, Ciphertext: j.Version.Ciphertext, Nonce: j.Version.Nonce, CreatedAt: j.Version.EventTime, Author: j.Version.Author,
	}
}

func ListVersionRecords(q *pq.Queries) []Record {
	rows := erru.Must(q.ListSecretVersionJoined(context.Background()))
	out := make([]Record, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordOf(r))
	}
	return out
}

func List(q *pq.Queries) []*pq.SecretEvent {
	rows := erru.Must(q.ListSecretRows(context.Background()))
	out := make([]*pq.SecretEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, pq.SecretEventOf(r))
	}
	return out
}

func Get(q *pq.Queries, secretID uint64) (*pq.SecretEvent, bool) {
	row, err := q.GetSecretRowByID(context.Background(), secretID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(err)
	}
	return pq.SecretEventOf(row), true
}

func IDByName(q *pq.Queries, spaceID uint64, name string) (uint64, bool) {
	return idByNameInSpace(q, nodes.NormalizedUserSpaceID(spaceID), name)
}

func idByNameInSpace(q *pq.Queries, spaceID uint64, name string) (uint64, bool) {
	n, err := q.LookupValueKey(context.Background(), spaceID, 0, name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false
	}
	if err != nil {
		panic(fmt.Sprintf("LookupValueKey: %v", err))
	}
	if n.Kind != apigen.CoreEntityType_CORE_ENTITY_SECRET {
		return 0, false
	}
	return n.ID, true
}

// GetVersion returns the event view of one secret value version.
func GetVersion(q *pq.Queries, ref apigen.ValueRef) (*pq.SecretEvent, bool) {
	j, err := q.GetSecretVersionJoined(context.Background(), ref)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(err)
	}
	return pq.SecretVersionEventOf(j), true
}

type current struct {
	pq.SecretVersionJoined
}

func (c current) entity() apigen.Secret { return pq.SecretEntity(c.Secret, c.Version) }

func currentSecret(ctx context.Context, q *pq.Queries, secretID uint64) (current, error) {
	s, err := q.GetSecretRowByID(ctx, secretID)
	if errors.Is(err, sql.ErrNoRows) {
		return current{}, values.ErrNotFound
	}
	if err != nil {
		return current{}, err
	}
	v, err := q.GetSecretVersion(ctx, apigen.ValueRef{ID: s.ID, Version: s.ValueVersion})
	if err != nil {
		return current{}, err
	}
	return current{pq.SecretVersionJoined{Secret: s, Version: v}}, nil
}

func secretUpdate(seq, now int64, author int64, verb apigen.AuthzVerb, id uint64, s apigen.Secret) *state.WriteUpdate {
	return pq.NewUpdate(pq.SecretMutation(values.WriteMeta(seq, now, author, verb), id, s))
}

func sealedEntity(s apigen.Secret, sealed SealedValue) apigen.Secret {
	s.Sealed = apigen.Some(apigen.SealedSecret{SmkVersion: sealed.SMKVersion, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce})
	return s
}

func CreateWithVersion(store *state.Service, name string, spaceID, directoryID uint64, author int64, seal SealFunc) (Record, error) {
	if !values.ValidName(name) {
		return Record{}, values.ErrNameInvalid
	}
	ctx := context.Background()
	now := time.Now().UnixMilli()
	var record Record
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		dirID, err := values.ResolveDirectory(ctx, q, spaceID, directoryID)
		if err != nil {
			return nil, err
		}
		if taken, err := values.NameTaken(ctx, q, spaceID, dirID, name, 0, 0); err != nil {
			return nil, err
		} else if taken {
			return nil, values.ErrAlreadyExists
		}
		id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_SECRET)
		if err != nil {
			return nil, err
		}
		sealed, err := seal(id)
		if err != nil {
			return nil, err
		}
		record = Record{
			SecretID: id, Key: name, Version: 1, SpaceID: spaceID,
			SMKVersion: sealed.SMKVersion, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, CreatedAt: now, Author: author,
		}
		entity := sealedEntity(apigen.Secret{Fs: apigen.SecretFs{Key: name, DirectoryID: values.DirectoryRef(dirID)}, SpaceID: spaceID}, sealed)
		return secretUpdate(seq, now, author, apigen.AuthzVerb_AUTHZ_VERB_CREATE, id, entity), nil
	}); err != nil {
		return Record{}, err
	}
	return record, nil
}

func appendVersionWithDeploymentUpdates(store *state.Service, secretID uint64, author int64, seal SealFunc, updateDeployments bool, expected []apigen.DeploymentExpectedSeq, afterCommit func(Record)) (Record, []uint64, error) {
	ctx := context.Background()
	var record Record
	insert := func(q *pq.Queries, globalSeq, now int64) (uint32, *state.WriteUpdate, error) {
		cur, err := currentSecret(ctx, q, secretID)
		if err != nil {
			return 0, nil, err
		}
		sealed, err := seal(secretID)
		if err != nil {
			return 0, nil, err
		}
		entity := sealedEntity(cur.entity(), sealed)
		next := cur.Version.ValueVersion + 1
		record = Record{
			SecretID: secretID, Key: cur.Secret.Key, Version: next, SpaceID: cur.Secret.SpaceID,
			SMKVersion: sealed.SMKVersion, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, CreatedAt: now, Author: author,
		}
		return next, secretUpdate(globalSeq, now, author, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, cur.Secret.ID, entity), nil
	}
	updatedDeployments, err := values.SetVersionedValueWithDeploymentUpdates(store, values.SecretReference, secretID, updateDeployments, expected, author, insert, func(_ []uint64) {
		if afterCommit != nil {
			afterCommit(record)
		}
	})
	if err != nil {
		return Record{}, nil, err
	}
	return record, updatedDeployments, nil
}

func renameSecret(store *state.Service, secretID uint64, newName string) error {
	if !values.ValidName(newName) {
		return values.ErrNameInvalid
	}
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		cur, err := currentSecret(ctx, q, secretID)
		if err != nil {
			return nil, err
		}
		if cur.Secret.Key == newName {
			return nil, nil
		}
		if taken, err := values.NameTaken(ctx, q, cur.Secret.SpaceID, cur.Secret.DirectoryID, newName, apigen.CoreEntityType_CORE_ENTITY_SECRET, cur.Secret.ID); err != nil {
			return nil, err
		} else if taken {
			return nil, values.ErrAlreadyExists
		}
		entity := cur.entity()
		entity.Fs.Key = newName
		return secretUpdate(seq, time.Now().UnixMilli(), 0, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, cur.Secret.ID, entity), nil
	})
}

func MoveDirectory(store *state.Service, secretID, newDirectoryID uint64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		cur, err := currentSecret(ctx, q, secretID)
		if err != nil {
			return nil, err
		}
		if cur.Secret.DirectoryID == newDirectoryID {
			return nil, nil
		}
		if newDirectoryID != 0 {
			dir, err := values.GetDirectory(ctx, q, newDirectoryID)
			if err != nil {
				return nil, err
			}
			if dir.SpaceID != cur.Secret.SpaceID {
				return nil, values.ErrSpaceMoveUnsupported
			}
		}
		if taken, err := values.NameTaken(ctx, q, cur.Secret.SpaceID, newDirectoryID, cur.Secret.Key, apigen.CoreEntityType_CORE_ENTITY_SECRET, cur.Secret.ID); err != nil {
			return nil, err
		} else if taken {
			return nil, values.ErrAlreadyExists
		}
		entity := cur.entity()
		entity.Fs.DirectoryID = values.DirectoryRef(newDirectoryID)
		return secretUpdate(seq, time.Now().UnixMilli(), 0, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, cur.Secret.ID, entity), nil
	})
}

func moveSpace(store *state.Service, secretID, newSpaceID, newDirectoryID uint64, author int64, inlockValidate func(*pq.Queries) error) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if inlockValidate != nil {
			if err := inlockValidate(q); err != nil {
				return nil, err
			}
		}
		cur, err := currentSecret(ctx, q, secretID)
		if err != nil {
			return nil, err
		}
		spaceID := nodes.NormalizedUserSpaceID(newSpaceID)
		if spaceID == cur.Secret.SpaceID && newDirectoryID == cur.Secret.DirectoryID {
			return nil, nil
		}
		if newDirectoryID != 0 {
			dir, err := values.GetDirectory(ctx, q, newDirectoryID)
			if err != nil {
				return nil, err
			}
			if dir.SpaceID != spaceID {
				return nil, values.ErrDirectoryNotFound
			}
		}
		if taken, err := values.NameTaken(ctx, q, spaceID, newDirectoryID, cur.Secret.Key, apigen.CoreEntityType_CORE_ENTITY_SECRET, cur.Secret.ID); err != nil {
			return nil, err
		} else if taken {
			return nil, values.ErrAlreadyExists
		}
		entity := cur.entity()
		entity.Fs.DirectoryID, entity.SpaceID = values.DirectoryRef(newDirectoryID), spaceID
		return secretUpdate(seq, time.Now().UnixMilli(), author, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, cur.Secret.ID, entity), nil
	})
}

func deleteSecret(store *state.Service, secretID uint64, inlockValidate func(*pq.Queries) error) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if inlockValidate != nil {
			if err := inlockValidate(q); err != nil {
				return nil, err
			}
		}
		cur, err := currentSecret(ctx, q, secretID)
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.DeleteMutation(values.WriteMeta(seq, time.Now().UnixMilli(), 0, apigen.AuthzVerb_AUTHZ_VERB_DELETE), apigen.CoreEntityType_CORE_ENTITY_SECRET, cur.Secret.ID)), nil
	})
}
