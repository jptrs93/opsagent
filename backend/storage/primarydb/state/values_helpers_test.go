package state

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jptrs93/goutil/erru"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func valueMeta(seq int64, author int32, verb apigen.AuthzVerb) pq.EventMeta {
	return pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), Author: int64(author), EventType: verb}
}

func commitForTest(s *Service, mutate func(q *pq.Queries, seq int64) (*WriteUpdate, error)) {
	erru.Must(0, s.Commit(context.Background(), nil, mutate))
}

func createSecretForTest(s *Service, name string) int64 {
	ctx := context.Background()
	var id int64
	commitForTest(s, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		var err error
		if id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_SECRET); err != nil {
			return nil, err
		}
		meta := valueMeta(seq, 1, apigen.AuthzVerb_AUTHZ_VERB_CREATE)
		secret := apigen.Secret{Fs: &apigen.SecretFs{Name: name}, SpaceID: 1, SmkVersion: 1, Ciphertext: []byte{byte(id)}, Nonce: []byte{2}}
		return pq.NewUpdate(pq.SecretMutation(meta, id, secret)), nil
	})
	return id
}

func currentSecretForTest(ctx context.Context, q *pq.Queries, id int64) (pq.SecretRow, pq.SecretVersionRow, error) {
	row, err := q.GetSecretRowByID(ctx, id)
	if err != nil {
		return row, pq.SecretVersionRow{}, err
	}
	v, err := q.GetSecretVersion(ctx, apigen.ValueRef{ID: int32(row.ID), Version: int32(row.ValueVersion)})
	return row, v, err
}

// carrySecretForTest renames a secret or deletes it; the sealed value is
// carried unchanged.
func carrySecretForTest(s *Service, id int64, name string, verb apigen.AuthzVerb) {
	ctx := context.Background()
	commitForTest(s, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		row, v, err := currentSecretForTest(ctx, q, id)
		if err != nil {
			return nil, err
		}
		if verb == apigen.AuthzVerb_AUTHZ_VERB_DELETE {
			return pq.NewUpdate(pq.DeleteMutation(valueMeta(seq, 1, verb), apigen.CoreEntityType_CORE_ENTITY_SECRET, id)), nil
		}
		entity := pq.SecretEntity(row, v)
		entity.Fs.Name = name
		return pq.NewUpdate(pq.SecretMutation(valueMeta(seq, 1, verb), id, entity)), nil
	})
}

func appendSecretVersionForTest(s *Service, id int64, ciphertext []byte) {
	ctx := context.Background()
	commitForTest(s, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		row, v, err := currentSecretForTest(ctx, q, id)
		if err != nil {
			return nil, err
		}
		entity := pq.SecretEntity(row, v)
		entity.Ciphertext, entity.Nonce = ciphertext, []byte{3}
		return pq.NewUpdate(pq.SecretMutation(valueMeta(seq, 2, apigen.AuthzVerb_AUTHZ_VERB_UPDATE), id, entity)), nil
	})
}

func createConfigForTest(s *Service, name, value string) int64 {
	ctx := context.Background()
	var id int64
	commitForTest(s, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		var err error
		if id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_CONFIG); err != nil {
			return nil, err
		}
		meta := valueMeta(seq, 1, apigen.AuthzVerb_AUTHZ_VERB_CREATE)
		config := apigen.Config{Fs: &apigen.ConfigFs{Name: name}, SpaceID: 1, Value: value}
		return pq.NewUpdate(pq.ConfigMutation(meta, id, config)), nil
	})
	return id
}

func currentConfigForTest(ctx context.Context, q *pq.Queries, id int64) (apigen.Config, error) {
	row, err := q.GetConfigRowByID(ctx, id)
	if err != nil {
		return apigen.Config{}, err
	}
	v, err := q.GetConfigVersion(ctx, apigen.ValueRef{ID: int32(row.ID), Version: int32(row.ValueVersion)})
	if err != nil {
		return apigen.Config{}, err
	}
	return pq.ConfigEntity(row, v), nil
}

// writeConfigForTest appends a value version, renames, or deletes a config.
func writeConfigForTest(s *Service, id int64, verb apigen.AuthzVerb, value string) {
	ctx := context.Background()
	commitForTest(s, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		entity, err := currentConfigForTest(ctx, q, id)
		if err != nil {
			return nil, err
		}
		if verb == apigen.AuthzVerb_AUTHZ_VERB_DELETE {
			return pq.NewUpdate(pq.DeleteMutation(valueMeta(seq, 1, verb), apigen.CoreEntityType_CORE_ENTITY_CONFIG, id)), nil
		}
		entity.Value = value
		return pq.NewUpdate(pq.ConfigMutation(valueMeta(seq, 1, verb), id, entity)), nil
	})
}

func renameConfigForTest(s *Service, id int64, name string) {
	ctx := context.Background()
	commitForTest(s, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		entity, err := currentConfigForTest(ctx, q, id)
		if err != nil {
			return nil, err
		}
		entity.Fs.Name = name
		return pq.NewUpdate(pq.ConfigMutation(valueMeta(seq, 1, apigen.AuthzVerb_AUTHZ_VERB_UPDATE), id, entity)), nil
	})
}

func setAssetByKeyForTest(s *Service, key string, blob []byte) *pq.AssetEvent {
	ctx := context.Background()
	sha, storageKey := putAssetContentForTest(s, blob)
	var id int64
	commitForTest(s, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		k, err := q.LookupAssetKey(ctx, int64(defaultSpaceID), 0, key)
		if err == nil && k.Kind == apigen.CoreEntityType_CORE_ENTITY_ASSET {
			id = k.ID
			row, err := q.GetAssetRowByID(ctx, id)
			if err != nil {
				return nil, err
			}
			v, err := q.GetAssetVersion(ctx, apigen.ValueRef{ID: int32(row.ID), Version: int32(row.ValueVersion)})
			if err != nil {
				return nil, err
			}
			entity := pq.AssetEntity(row, v)
			entity.SizeBytes, entity.Sha256, entity.StorageKey = int64(len(blob)), sha, storageKey
			return pq.NewUpdate(pq.AssetMutation(valueMeta(seq, 0, apigen.AuthzVerb_AUTHZ_VERB_UPDATE), id, entity)), nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_ASSET); err != nil {
			return nil, err
		}
		meta := valueMeta(seq, 0, apigen.AuthzVerb_AUTHZ_VERB_CREATE)
		entity := apigen.Asset{Fs: &apigen.AssetFs{Key: key}, SpaceID: defaultSpaceID, SizeBytes: int64(len(blob)), Sha256: sha, StorageKey: storageKey}
		return pq.NewUpdate(pq.AssetMutation(meta, id, entity)), nil
	})
	return erru.Must(s.q.GetAssetEvent(ctx, id))
}

func deleteAssetForTest(s *Service, assetID int32) {
	commitForTest(s, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		return pq.NewUpdate(pq.DeleteMutation(valueMeta(seq, 0, apigen.AuthzVerb_AUTHZ_VERB_DELETE), apigen.CoreEntityType_CORE_ENTITY_ASSET, int64(assetID))), nil
	})
}

func createAssetDirectoryForTest(s *Service, spaceID, parentID int32, key string, author int32) apigen.AssetDirectory {
	ctx := context.Background()
	var d apigen.AssetDirectory
	commitForTest(s, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY)
		if err != nil {
			return nil, err
		}
		meta := valueMeta(seq, author, apigen.AuthzVerb_AUTHZ_VERB_CREATE)
		d = apigen.AssetDirectory{ID: int32(id), SpaceID: spaceID, Key: key, ParentID: parentID}
		return pq.NewUpdate(pq.AssetDirectoryMutation(meta, d)), nil
	})
	return d
}

func deleteAssetDirectoryForTest(s *Service, id int32) {
	commitForTest(s, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		return pq.NewUpdate(pq.DeleteMutation(valueMeta(seq, 0, apigen.AuthzVerb_AUTHZ_VERB_DELETE), apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY, int64(id))), nil
	})
}

func createValueDirectoryForTest(s *Service, spaceID, parentID int32, name string, author int32) *apigen.ValueDirectory {
	ctx := context.Background()
	var d *apigen.ValueDirectory
	commitForTest(s, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY)
		if err != nil {
			return nil, err
		}
		meta := valueMeta(seq, author, apigen.AuthzVerb_AUTHZ_VERB_CREATE)
		d = &apigen.ValueDirectory{ID: int32(id), SpaceID: spaceID, Name: name, ParentID: parentID}
		return pq.NewUpdate(pq.ValueDirectoryMutation(meta, d)), nil
	})
	return d
}

func deleteValueDirectoryForTest(s *Service, id int32) {
	commitForTest(s, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		return pq.NewUpdate(pq.DeleteMutation(valueMeta(seq, 0, apigen.AuthzVerb_AUTHZ_VERB_DELETE), apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY, int64(id))), nil
	})
}
