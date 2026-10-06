package values

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

const configType = apigen.CoreEntityType_CORE_ENTITY_CONFIG

func ListConfigs(q *pq.Queries) []*pq.ConfigEvent {
	return erru.Must(q.ListConfigEvents(context.Background()))
}

func GetConfig(q *pq.Queries, configID uint64) (*pq.ConfigEvent, bool) {
	row, err := q.GetConfigEvent(context.Background(), configID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(err)
	}
	return row, true
}

type ConfigVersion struct {
	ConfigID  uint64
	Key       string
	SpaceID   uint64
	Version   uint32
	Value     string
	CreatedAt int64
	Author    int64
}

func GetConfigVersion(q *pq.Queries, ref apigen.ValueRef) (ConfigVersion, bool) {
	j, err := q.GetConfigVersionJoined(context.Background(), ref)
	if errors.Is(err, sql.ErrNoRows) {
		return ConfigVersion{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetConfigVersionJoined: %v", err))
	}
	return ConfigVersion{
		ConfigID: j.Config.ID, Key: j.Config.Key, SpaceID: j.Config.SpaceID,
		Version: j.Version.ValueVersion, Value: j.Version.Value, CreatedAt: j.Version.EventTime, Author: j.Version.Author,
	}, true
}

func ResolveConfigs(q *pq.Queries, refs []apigen.ValueRef) (map[apigen.ValueRef]string, error) {
	out := make(map[apigen.ValueRef]string, len(refs))
	for _, ref := range refs {
		if !ref.Valid() {
			return nil, errors.New("config id and version are required")
		}
		if _, ok := out[ref]; ok {
			continue
		}
		version, ok := GetConfigVersion(q, ref)
		if !ok {
			return nil, fmt.Errorf("config not found: %s", ref)
		}
		out[ref] = version.Value
	}
	return out, nil
}

func currentConfig(ctx context.Context, q *pq.Queries, configID uint64) (pq.ConfigVersionJoined, error) {
	c, err := q.GetConfigRowByID(ctx, configID)
	if errors.Is(err, sql.ErrNoRows) {
		return pq.ConfigVersionJoined{}, ErrNotFound
	}
	if err != nil {
		return pq.ConfigVersionJoined{}, err
	}
	v, err := q.GetConfigVersion(ctx, apigen.ValueRef{ID: c.ID, Version: c.ValueVersion})
	if err != nil {
		return pq.ConfigVersionJoined{}, err
	}
	return pq.ConfigVersionJoined{Config: c, Version: v}, nil
}

func configWrite(meta pq.EventMeta, id uint64, createdTime int64, valueVersion uint32, c apigen.Config) (*pq.ConfigEvent, *state.WriteUpdate) {
	event := &pq.ConfigEvent{ConfigID: id, Seq: meta.GlobalSeq, Author: meta.Author, CreatedTime: createdTime, EventTime: meta.EventTime, ValueVersion: valueVersion, Value: c}
	return event, pq.NewUpdate(pq.ConfigMutation(meta, id, c))
}

func CreateConfig(store *state.Service, key string, spaceID, directoryID uint64, author int64, value string) (*pq.ConfigEvent, error) {
	if !ValidName(key) {
		return nil, ErrNameInvalid
	}
	ctx := context.Background()
	space := nodes.NormalizedUserSpaceID(spaceID)
	now := nowMillis()
	var created *pq.ConfigEvent
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		dirID, err := ResolveDirectory(ctx, q, space, directoryID)
		if err != nil {
			return nil, err
		}
		if err := requireNameFree(ctx, q, space, dirID, key, 0, 0); err != nil {
			return nil, err
		}
		id, err := q.NextEntityID(ctx, configType)
		if err != nil {
			return nil, err
		}
		var update *state.WriteUpdate
		created, update = configWrite(WriteMeta(seq, now, author, apigen.AuthzVerb_AUTHZ_VERB_CREATE), id, now, 1,
			apigen.Config{Fs: apigen.ConfigFs{Key: key, DirectoryID: DirectoryRef(dirID)}, SpaceID: space, Value: value})
		return update, nil
	}); err != nil {
		return nil, err
	}
	return created, nil
}

func AppendConfigVersion(store *state.Service, configID uint64, value string, author int64, updateDeployments bool, expected []apigen.DeploymentExpectedSeq) (*pq.ConfigEvent, []uint64, error) {
	ctx := context.Background()
	var written *pq.ConfigEvent
	insert := func(q *pq.Queries, seq, now int64) (uint32, *state.WriteUpdate, error) {
		cur, err := currentConfig(ctx, q, configID)
		if err != nil {
			return 0, nil, err
		}
		if cur.Version.Value == value {
			written = pq.ConfigEventOf(cur)
			return cur.Version.ValueVersion, nil, nil
		}
		next := pq.ConfigEntity(cur.Config, cur.Version)
		next.Value = value
		version := cur.Version.ValueVersion + 1
		var update *state.WriteUpdate
		written, update = configWrite(WriteMeta(seq, now, author, apigen.AuthzVerb_AUTHZ_VERB_UPDATE), cur.Config.ID, cur.Config.CreatedTime, version, next)
		return version, update, nil
	}
	updatedDeployments, err := SetVersionedValueWithDeploymentUpdates(store, ConfigReference, configID, updateDeployments, expected, author, insert, nil)
	if err != nil {
		return nil, nil, err
	}
	return written, updatedDeployments, nil
}

func RenameConfig(store *state.Service, configID uint64, newKey string) (*pq.ConfigEvent, error) {
	if !ValidName(newKey) {
		return nil, ErrNameInvalid
	}
	ctx := logu.AddTag(context.Background(), "Values")
	var current *pq.ConfigEvent
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		cur, err := currentConfig(ctx, q, configID)
		if err != nil {
			return nil, err
		}
		current = pq.ConfigEventOf(cur)
		if cur.Config.Key == newKey {
			return nil, nil
		}
		if err := requireNameFree(ctx, q, cur.Config.SpaceID, cur.Config.DirectoryID, newKey, configType, cur.Config.ID); err != nil {
			return nil, err
		}
		next := pq.ConfigEntity(cur.Config, cur.Version)
		next.Fs.Key = newKey
		slog.InfoContext(ctx, fmt.Sprintf("renamed config %d from %s to %s", configID, cur.Config.Key, newKey))
		var update *state.WriteUpdate
		current, update = configWrite(WriteMeta(seq, nowMillis(), 0, apigen.AuthzVerb_AUTHZ_VERB_UPDATE), cur.Config.ID, cur.Config.CreatedTime, cur.Version.ValueVersion, next)
		return update, nil
	}); err != nil {
		return nil, err
	}
	return current, nil
}

func MoveConfigDirectory(store *state.Service, configID, newDirectoryID uint64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		cur, err := currentConfig(ctx, q, configID)
		if err != nil {
			return nil, err
		}
		if cur.Config.DirectoryID == newDirectoryID {
			return nil, nil
		}
		if newDirectoryID != 0 {
			dir, err := GetDirectory(ctx, q, newDirectoryID)
			if err != nil {
				return nil, err
			}
			if dir.SpaceID != cur.Config.SpaceID {
				return nil, ErrSpaceMoveUnsupported
			}
		}
		if err := requireNameFree(ctx, q, cur.Config.SpaceID, newDirectoryID, cur.Config.Key, configType, cur.Config.ID); err != nil {
			return nil, err
		}
		next := pq.ConfigEntity(cur.Config, cur.Version)
		next.Fs.DirectoryID = DirectoryRef(newDirectoryID)
		return pq.NewUpdate(pq.ConfigMutation(WriteMeta(seq, nowMillis(), 0, apigen.AuthzVerb_AUTHZ_VERB_UPDATE), cur.Config.ID, next)), nil
	})
}

func MoveConfigSpace(store *state.Service, configID, newSpaceID, newDirectoryID uint64, author int64, inlockValidate func(*pq.Queries) error) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if inlockValidate != nil {
			if err := inlockValidate(q); err != nil {
				return nil, err
			}
		}
		cur, err := currentConfig(ctx, q, configID)
		if err != nil {
			return nil, err
		}
		spaceID := nodes.NormalizedUserSpaceID(newSpaceID)
		if spaceID == cur.Config.SpaceID && newDirectoryID == cur.Config.DirectoryID {
			return nil, nil
		}
		if newDirectoryID != 0 {
			dir, err := GetDirectory(ctx, q, newDirectoryID)
			if err != nil {
				return nil, err
			}
			if dir.SpaceID != spaceID {
				return nil, ErrDirectoryNotFound
			}
		}
		if err := requireNameFree(ctx, q, spaceID, newDirectoryID, cur.Config.Key, configType, cur.Config.ID); err != nil {
			return nil, err
		}
		next := pq.ConfigEntity(cur.Config, cur.Version)
		next.Fs.DirectoryID, next.SpaceID = DirectoryRef(newDirectoryID), spaceID
		return pq.NewUpdate(pq.ConfigMutation(WriteMeta(seq, nowMillis(), author, apigen.AuthzVerb_AUTHZ_VERB_UPDATE), cur.Config.ID, next)), nil
	})
}

func DeleteConfig(store *state.Service, configID uint64, inlockValidate func(*pq.Queries) error) (*pq.ConfigEvent, error) {
	ctx := context.Background()
	var deleted *pq.ConfigEvent
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if inlockValidate != nil {
			if err := inlockValidate(q); err != nil {
				return nil, err
			}
		}
		cur, err := currentConfig(ctx, q, configID)
		if err != nil {
			return nil, err
		}
		deleted = pq.ConfigEventOf(cur)
		return pq.NewUpdate(pq.DeleteMutation(WriteMeta(seq, nowMillis(), 0, apigen.AuthzVerb_AUTHZ_VERB_DELETE), configType, cur.Config.ID)), nil
	})
	if err != nil {
		return nil, err
	}
	return deleted, nil
}
