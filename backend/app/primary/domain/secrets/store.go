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

func listKeyslots(q *pq.Queries) []Keyslot {
	rows := erru.Must(q.ListSecretKeyslots(context.Background()))
	out := make([]Keyslot, 0, len(rows))
	for _, r := range rows {
		out = append(out, Keyslot{
			Kind: r.Kind, NodeID: int32(r.NodeID), SMKVersion: int32(r.SmkVersion), WrappedSMK: r.WrappedSmk, Nonce: r.Nonce, KDFSalt: r.KdfSalt, CreatedAt: r.UpdatedAt,
		})
	}
	return out
}

func writeKeyslot(store *state.Service, k Keyslot, author int32) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		eventType := apigen.AuthzVerb_AUTHZ_VERB_CREATE
		if _, ok := findSlot(listKeyslots(q), k.Kind, k.NodeID); ok {
			eventType = apigen.AuthzVerb_AUTHZ_VERB_UPDATE
		}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: k.CreatedAt, Author: int64(author), EventType: eventType}
		slot := pq.SecretKeyslot{Kind: k.Kind, NodeID: int64(k.NodeID), SmkVersion: int64(k.SMKVersion), WrappedSmk: k.WrappedSMK, Nonce: k.Nonce, KdfSalt: k.KDFSalt, UpdatedAt: k.CreatedAt}
		return pq.NewUpdate(pq.SecretKeyslotMutation(meta, slot)), nil
	})
}

func recordOf(j pq.SecretVersionJoined) Record {
	return Record{
		SecretID: int32(j.Secret.ID), Name: j.Secret.Name, Version: int32(j.Version.ValueVersion), SpaceID: int32(j.Secret.SpaceID),
		SMKVersion: int32(j.Version.SmkVersion), Ciphertext: j.Version.Ciphertext, Nonce: j.Version.Nonce, CreatedAt: j.Version.EventTime, Author: int32(j.Version.Author),
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

func Get(q *pq.Queries, secretID int32) (*pq.SecretEvent, bool) {
	row, err := q.GetSecretRowByID(context.Background(), int64(secretID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(err)
	}
	return pq.SecretEventOf(row), true
}

func IDByName(q *pq.Queries, spaceID int32, name string) (int32, bool) {
	return idByNameInSpace(q, nodes.NormalizedUserSpaceID(spaceID), name)
}

func idByNameInSpace(q *pq.Queries, spaceID int32, name string) (int32, bool) {
	n, err := q.LookupValueName(context.Background(), int64(spaceID), 0, name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false
	}
	if err != nil {
		panic(fmt.Sprintf("LookupValueName: %v", err))
	}
	if n.Kind != apigen.CoreEntityType_CORE_ENTITY_SECRET {
		return 0, false
	}
	return int32(n.ID), true
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

func currentSecret(ctx context.Context, q *pq.Queries, secretID int32) (current, error) {
	s, err := q.GetSecretRowByID(ctx, int64(secretID))
	if errors.Is(err, sql.ErrNoRows) {
		return current{}, values.ErrNotFound
	}
	if err != nil {
		return current{}, err
	}
	v, err := q.GetSecretVersion(ctx, apigen.ValueRef{ID: int32(s.ID), Version: int32(s.ValueVersion)})
	if err != nil {
		return current{}, err
	}
	return current{pq.SecretVersionJoined{Secret: s, Version: v}}, nil
}

func secretUpdate(seq, now int64, author int32, verb apigen.AuthzVerb, id int64, s apigen.Secret) *state.WriteUpdate {
	return pq.NewUpdate(pq.SecretMutation(values.WriteMeta(seq, now, author, verb), id, s))
}

func sealedEntity(s apigen.Secret, sealed SealedValue) apigen.Secret {
	s.SmkVersion, s.Ciphertext, s.Nonce = int64(sealed.SMKVersion), sealed.Ciphertext, sealed.Nonce
	return s
}

func CreateWithVersion(store *state.Service, name string, spaceID, directoryID, author int32, seal SealFunc) (Record, error) {
	if !values.ValidName(name) {
		return Record{}, values.ErrNameInvalid
	}
	ctx := context.Background()
	space := int64(spaceID)
	now := time.Now().UnixMilli()
	var record Record
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		dirID, err := values.ResolveDirectory(ctx, q, space, directoryID)
		if err != nil {
			return nil, err
		}
		if taken, err := values.NameTaken(ctx, q, space, dirID, name, 0, 0); err != nil {
			return nil, err
		} else if taken {
			return nil, values.ErrAlreadyExists
		}
		id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_SECRET)
		if err != nil {
			return nil, err
		}
		sealed, err := seal(int32(id))
		if err != nil {
			return nil, err
		}
		record = Record{
			SecretID: int32(id), Name: name, Version: 1, SpaceID: int32(space),
			SMKVersion: sealed.SMKVersion, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, CreatedAt: now, Author: author,
		}
		entity := sealedEntity(apigen.Secret{Fs: &apigen.SecretFs{Name: name, DirectoryID: int32(dirID)}, SpaceID: int32(space)}, sealed)
		return secretUpdate(seq, now, author, apigen.AuthzVerb_AUTHZ_VERB_CREATE, id, entity), nil
	}); err != nil {
		return Record{}, err
	}
	return record, nil
}

func appendVersionWithDeploymentUpdates(store *state.Service, secretID, author int32, seal SealFunc, updateDeployments bool, expected []*apigen.DeploymentExpectedSeq, afterCommit func(Record)) (Record, []int32, error) {
	ctx := context.Background()
	var record Record
	insert := func(q *pq.Queries, globalSeq, now int64) (int32, *state.WriteUpdate, error) {
		cur, err := currentSecret(ctx, q, secretID)
		if err != nil {
			return 0, nil, err
		}
		sealed, err := seal(secretID)
		if err != nil {
			return 0, nil, err
		}
		entity := sealedEntity(cur.entity(), sealed)
		next := int32(cur.Version.ValueVersion) + 1
		record = Record{
			SecretID: secretID, Name: cur.Secret.Name, Version: next, SpaceID: int32(cur.Secret.SpaceID),
			SMKVersion: sealed.SMKVersion, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, CreatedAt: now, Author: author,
		}
		return next, secretUpdate(globalSeq, now, author, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, cur.Secret.ID, entity), nil
	}
	updatedDeployments, err := values.SetVersionedValueWithDeploymentUpdates(store, values.SecretReference, secretID, updateDeployments, expected, author, insert, func(_ []int32) {
		if afterCommit != nil {
			afterCommit(record)
		}
	})
	if err != nil {
		return Record{}, nil, err
	}
	return record, updatedDeployments, nil
}

func renameSecret(store *state.Service, secretID int32, newName string) error {
	if !values.ValidName(newName) {
		return values.ErrNameInvalid
	}
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		cur, err := currentSecret(ctx, q, secretID)
		if err != nil {
			return nil, err
		}
		if cur.Secret.Name == newName {
			return nil, nil
		}
		if taken, err := values.NameTaken(ctx, q, cur.Secret.SpaceID, cur.Secret.DirectoryID, newName, apigen.CoreEntityType_CORE_ENTITY_SECRET, cur.Secret.ID); err != nil {
			return nil, err
		} else if taken {
			return nil, values.ErrAlreadyExists
		}
		entity := cur.entity()
		entity.Fs.Name = newName
		return secretUpdate(seq, time.Now().UnixMilli(), 0, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, cur.Secret.ID, entity), nil
	})
}

func MoveDirectory(store *state.Service, secretID, newDirectoryID int32) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		cur, err := currentSecret(ctx, q, secretID)
		if err != nil {
			return nil, err
		}
		dirID := int64(newDirectoryID)
		if cur.Secret.DirectoryID == dirID {
			return nil, nil
		}
		if dirID != 0 {
			dir, err := values.GetDirectory(ctx, q, dirID)
			if err != nil {
				return nil, err
			}
			if int64(dir.SpaceID) != cur.Secret.SpaceID {
				return nil, values.ErrSpaceMoveUnsupported
			}
		}
		if taken, err := values.NameTaken(ctx, q, cur.Secret.SpaceID, dirID, cur.Secret.Name, apigen.CoreEntityType_CORE_ENTITY_SECRET, cur.Secret.ID); err != nil {
			return nil, err
		} else if taken {
			return nil, values.ErrAlreadyExists
		}
		entity := cur.entity()
		entity.Fs.DirectoryID = int32(dirID)
		return secretUpdate(seq, time.Now().UnixMilli(), 0, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, cur.Secret.ID, entity), nil
	})
}

func moveSpace(store *state.Service, secretID, newSpaceID, newDirectoryID, author int32, inlockValidate func(*pq.Queries) error) error {
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
		spaceID := int64(nodes.NormalizedUserSpaceID(newSpaceID))
		dirID := int64(newDirectoryID)
		if spaceID == cur.Secret.SpaceID && dirID == cur.Secret.DirectoryID {
			return nil, nil
		}
		if dirID != 0 {
			dir, err := values.GetDirectory(ctx, q, dirID)
			if err != nil {
				return nil, err
			}
			if int64(dir.SpaceID) != spaceID {
				return nil, values.ErrDirectoryNotFound
			}
		}
		if taken, err := values.NameTaken(ctx, q, spaceID, dirID, cur.Secret.Name, apigen.CoreEntityType_CORE_ENTITY_SECRET, cur.Secret.ID); err != nil {
			return nil, err
		} else if taken {
			return nil, values.ErrAlreadyExists
		}
		entity := cur.entity()
		entity.Fs.DirectoryID, entity.SpaceID = int32(dirID), int32(spaceID)
		return secretUpdate(seq, time.Now().UnixMilli(), author, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, cur.Secret.ID, entity), nil
	})
}

func deleteSecret(store *state.Service, secretID int32, inlockValidate func(*pq.Queries) error) error {
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
