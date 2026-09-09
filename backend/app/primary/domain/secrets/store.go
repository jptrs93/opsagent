package secrets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func listKeyslots(q *pq.Queries) []Keyslot {
	rows := erru.Must(q.ListSecretKeyslots(context.Background()))
	out := make([]Keyslot, 0, len(rows))
	for _, r := range rows {
		out = append(out, Keyslot{
			Slot: r.Slot, SMKVersion: int32(r.SmkVersion), WrappedSMK: r.WrappedSmk, Nonce: r.Nonce, KDFSalt: r.KdfSalt, CreatedAt: r.CreatedAt,
		})
	}
	return out
}

func upsertKeyslot(q *pq.Queries, k Keyslot) {
	if err := q.UpsertSecretKeyslot(context.Background(), pq.UpsertSecretKeyslotParams{
		Slot: k.Slot, SmkVersion: int64(k.SMKVersion), WrappedSmk: k.WrappedSMK, Nonce: k.Nonce, KdfSalt: k.KDFSalt, CreatedAt: k.CreatedAt,
	}); err != nil {
		panic(fmt.Sprintf("UpsertSecretKeyslot: %v", err))
	}
}

func recordFromRow(r pq.SecretVersionRecordRow) Record {
	return Record{
		ID: int32(r.ID), SecretID: int32(r.SecretID), Name: r.Name, Version: int32(r.Version), SpaceID: int32(r.SpaceID),
		SMKVersion: int32(r.SmkVersion), Ciphertext: r.Ciphertext, Nonce: r.Nonce, CreatedAt: r.CreatedAt, Author: int32(r.Author),
	}
}

func ListVersionRecords(q *pq.Queries) []Record {
	rows := erru.Must(q.ListSecretVersionRecords(context.Background()))
	out := make([]Record, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordFromRow(r))
	}
	return out
}

func getSystemSecret(q *pq.Queries, name string) (SystemRecord, bool) {
	r, err := q.GetSystemSecret(context.Background(), name)
	if errors.Is(err, sql.ErrNoRows) {
		return SystemRecord{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetSystemSecret: %v", err))
	}
	return SystemRecord{Name: r.Name, SMKVersion: int32(r.SmkVersion), Ciphertext: r.Ciphertext, Nonce: r.Nonce, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}, true
}

func upsertSystemSecret(q *pq.Queries, r SystemRecord) {
	if err := q.UpsertSystemSecret(context.Background(), pq.UpsertSystemSecretParams{
		Name: r.Name, SmkVersion: int64(r.SMKVersion), Ciphertext: r.Ciphertext, Nonce: r.Nonce, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}); err != nil {
		panic(fmt.Sprintf("UpsertSystemSecret: %v", err))
	}
}

func List(q *pq.Queries) []*apigen.SecretEvent {
	return erru.Must(q.ListLatestLiveSecretEvents(context.Background()))
}

func Get(q *pq.Queries, secretID int32) (*apigen.SecretEvent, bool) {
	row, err := q.GetLatestSecretEvent(context.Background(), int64(secretID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(err)
	}
	if row.EventType == apigen.EventType_EVENT_TYPE_DELETE {
		return nil, false
	}
	return row, true
}

func IDByName(q *pq.Queries, spaceID int32, name string) (int32, bool) {
	row, err := q.GetSecretInDirectoryByName(context.Background(), pq.GetSecretInDirectoryByNameParams{
		SpaceID: int64(nodes.NormalizedUserSpaceID(spaceID)), ValueDirectoryID: 0, Name: name,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetSecretInDirectoryByName: %v", err))
	}
	return int32(row.ID), true
}

func VersionIDs(q *pq.Queries, secretID int32) []int32 {
	rows := erru.Must(q.ListSecretVersionIDsBySecretID(context.Background(), int64(secretID)))
	ids := make([]int32, 0, len(rows))
	for _, id := range rows {
		ids = append(ids, int32(id))
	}
	return ids
}

func GetEvent(q *pq.Queries, secretID int32, eventID int64) (*apigen.SecretEvent, bool) {
	row, err := q.GetSecretEventByID(context.Background(), eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(err)
	}
	if row.SecretID != secretID {
		return nil, false
	}
	return row, true
}

func nextSecretEvent(prev *apigen.SecretEvent, author int32, eventType int64) pq.SecretEvent {
	return pq.SecretEvent{
		EventTime: time.Now().UnixMilli(), CreatedTime: prev.CreatedTime, Author: int64(author),
		SecretID: int64(prev.SecretID), Version: int64(prev.Version) + 1, ValueVersion: int64(prev.ValueVersion), SpaceVersion: int64(prev.SpaceVersion),
		Name: prev.Value.Fs.Name, ValueDirectoryID: int64(prev.Value.Fs.DirectoryID), SpaceID: int64(prev.Value.SpaceID), EventType: eventType,
	}
}

func appendSecretEvent(ctx context.Context, q *pq.Queries, seq int64, event pq.SecretEvent) (*state.Update, error) {
	event.GlobalSeq = seq
	written, err := q.InsertSecretCarryEvent(ctx, event)
	if err != nil {
		return nil, err
	}
	return &apigen.CoreUpdate{SecretEvents: []*apigen.SecretEvent{written}}, nil
}

func latestSecretEvent(ctx context.Context, q *pq.Queries, secretID int32) (*apigen.SecretEvent, error) {
	e, err := q.GetLatestSecretEvent(ctx, int64(secretID))
	if errors.Is(err, sql.ErrNoRows) || err == nil && e.EventType == apigen.EventType_EVENT_TYPE_DELETE {
		return nil, values.ErrNotFound
	}
	return e, err
}

func CreateWithVersion(store *state.Service, name string, spaceID, directoryID, author int32, seal SealFunc) (Record, error) {
	if !values.ValidName(name) {
		return Record{}, values.ErrNameInvalid
	}
	ctx := context.Background()
	space := int64(nodes.NormalizedUserSpaceID(spaceID))
	now := time.Now().UnixMilli()
	var record Record
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		dirID, err := values.ResolveDirectory(ctx, q, space, directoryID)
		if err != nil {
			return nil, err
		}
		taken, err := values.SiblingNameTaken(ctx, q, space, dirID, name, 0, 0, 0)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, values.ErrAlreadyExists
		}
		id, err := q.NextSecretID(ctx)
		if err != nil {
			return nil, err
		}
		sealed, err := seal(int32(id), 1)
		if err != nil {
			return nil, err
		}
		written, err := q.InsertSecretEvent(ctx, pq.SecretEvent{
			GlobalSeq: seq, EventTime: now, CreatedTime: now, Author: int64(author), SecretID: id,
			Version: 1, ValueVersion: 1, SpaceVersion: 1, ValueChanged: 1, SpaceChanged: 1,
			Name: name, ValueDirectoryID: dirID, SpaceID: space,
			SmkVersion: int64(sealed.SMKVersion), Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, EventType: pq.EventCreate,
		})
		if err != nil {
			return nil, fmt.Errorf("InsertSecretEvent: %w", err)
		}
		record = Record{
			ID: int32(written.EventID), SecretID: int32(id), Name: name, Version: 1, SpaceID: int32(space),
			SMKVersion: sealed.SMKVersion, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, CreatedAt: now, Author: author,
		}
		return &apigen.CoreUpdate{SecretEvents: []*apigen.SecretEvent{written}}, nil
	}); err != nil {
		return Record{}, err
	}
	return record, nil
}

func appendVersionWithDeploymentUpdates(store *state.Service, secretID, author int32, seal SealFunc, updateDeployments bool, expected []storage.DeploymentSpecVersion, afterCommit func(Record)) (Record, []int32, error) {
	ctx := context.Background()
	var record Record
	insert := func(q *pq.Queries, globalSeq int64) (int32, apigen.CoreUpdate, error) {
		prev, err := latestSecretEvent(ctx, q, secretID)
		if err != nil {
			return 0, apigen.CoreUpdate{}, err
		}
		version := int64(prev.ValueVersion) + 1
		sealed, err := seal(secretID, int32(version))
		if err != nil {
			return 0, apigen.CoreUpdate{}, err
		}
		event := nextSecretEvent(prev, author, pq.EventUpdate)
		event.ValueVersion = version
		event.ValueChanged = 1
		event.SmkVersion = int64(sealed.SMKVersion)
		event.Ciphertext = sealed.Ciphertext
		event.Nonce = sealed.Nonce
		event.GlobalSeq = globalSeq
		written, err := q.InsertSecretEvent(ctx, event)
		if err != nil {
			return 0, apigen.CoreUpdate{}, fmt.Errorf("insert secret value event: %w", err)
		}
		record = Record{
			ID: int32(written.EventID), SecretID: secretID, Name: prev.Value.Fs.Name, Version: int32(version), SpaceID: prev.Value.SpaceID,
			SMKVersion: sealed.SMKVersion, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, CreatedAt: event.EventTime, Author: author,
		}
		return int32(written.EventID), apigen.CoreUpdate{Seq: globalSeq, SecretEvents: []*apigen.SecretEvent{written}}, nil
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
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		prev, err := latestSecretEvent(ctx, q, secretID)
		if err != nil {
			return nil, err
		}
		if prev.Value.Fs.Name == newName {
			return nil, nil
		}
		taken, err := values.SiblingNameTaken(ctx, q, int64(prev.Value.SpaceID), int64(prev.Value.Fs.DirectoryID), newName, int64(prev.SecretID), 0, 0)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, values.ErrAlreadyExists
		}
		event := nextSecretEvent(prev, 0, pq.EventUpdate)
		event.Name = newName
		return appendSecretEvent(ctx, q, seq, event)
	})
}

func MoveDirectory(store *state.Service, secretID, newDirectoryID int32) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		prev, err := latestSecretEvent(ctx, q, secretID)
		if err != nil {
			return nil, err
		}
		dirID := int64(newDirectoryID)
		if int64(prev.Value.Fs.DirectoryID) == dirID {
			return nil, nil
		}
		if dirID != 0 {
			dir, err := values.GetDirectory(ctx, q, dirID)
			if err != nil {
				return nil, err
			}
			if dir.SpaceID != prev.Value.SpaceID {
				return nil, values.ErrSpaceMoveUnsupported
			}
		}
		taken, err := values.SiblingNameTaken(ctx, q, int64(prev.Value.SpaceID), dirID, prev.Value.Fs.Name, int64(prev.SecretID), 0, 0)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, values.ErrAlreadyExists
		}
		event := nextSecretEvent(prev, 0, pq.EventUpdate)
		event.ValueDirectoryID = dirID
		return appendSecretEvent(ctx, q, seq, event)
	})
}

func moveSpace(store *state.Service, secretID, newSpaceID, newDirectoryID, author int32, inlockValidate pq.Validator) error {
	ctx := context.Background()
	return store.Commit(ctx, inlockValidate, func(q *pq.Queries, seq int64) (*state.Update, error) {
		prev, err := latestSecretEvent(ctx, q, secretID)
		if err != nil {
			return nil, err
		}
		spaceID := int64(nodes.NormalizedUserSpaceID(newSpaceID))
		dirID := int64(newDirectoryID)
		if spaceID == int64(prev.Value.SpaceID) && dirID == int64(prev.Value.Fs.DirectoryID) {
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
		taken, err := values.SiblingNameTaken(ctx, q, spaceID, dirID, prev.Value.Fs.Name, int64(prev.SecretID), 0, 0)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, values.ErrAlreadyExists
		}
		event := nextSecretEvent(prev, author, pq.EventUpdate)
		event.ValueDirectoryID = dirID
		if spaceID != int64(prev.Value.SpaceID) {
			event.SpaceID = spaceID
			event.SpaceVersion = int64(prev.SpaceVersion) + 1
			event.SpaceChanged = 1
		}
		return appendSecretEvent(ctx, q, seq, event)
	})
}

func deleteSecret(store *state.Service, secretID int32, inlockValidate pq.Validator) error {
	ctx := context.Background()
	return store.Commit(ctx, inlockValidate, func(q *pq.Queries, seq int64) (*state.Update, error) {
		prev, err := latestSecretEvent(ctx, q, secretID)
		if err != nil {
			return nil, err
		}
		return appendSecretEvent(ctx, q, seq, nextSecretEvent(prev, 0, pq.EventDelete))
	})
}
