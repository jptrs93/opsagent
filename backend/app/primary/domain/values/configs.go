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

func GetConfig(q *pq.Queries, configID int32) (*pq.ConfigEvent, bool) {
	row, err := q.GetConfigEvent(context.Background(), int64(configID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		panic(err)
	}
	return row, true
}

type ConfigVersion struct {
	ConfigID  int32
	Name      string
	SpaceID   int32
	Version   int32
	Value     string
	CreatedAt int64
	Author    int32
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
		ConfigID: int32(j.Config.ID), Name: j.Config.Name, SpaceID: int32(j.Config.SpaceID),
		Version: int32(j.Version.ValueVersion), Value: j.Version.Value, CreatedAt: j.Version.EventTime, Author: int32(j.Version.Author),
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

func currentConfig(ctx context.Context, q *pq.Queries, configID int32) (pq.ConfigVersionJoined, error) {
	c, err := q.GetConfigRowByID(ctx, int64(configID))
	if errors.Is(err, sql.ErrNoRows) {
		return pq.ConfigVersionJoined{}, ErrNotFound
	}
	if err != nil {
		return pq.ConfigVersionJoined{}, err
	}
	v, err := q.GetConfigVersion(ctx, apigen.ValueRef{ID: int32(c.ID), Version: int32(c.ValueVersion)})
	if err != nil {
		return pq.ConfigVersionJoined{}, err
	}
	return pq.ConfigVersionJoined{Config: c, Version: v}, nil
}

func configWrite(meta pq.EventMeta, id, createdTime int64, valueVersion int32, c apigen.Config) (*pq.ConfigEvent, *state.WriteUpdate) {
	event := &pq.ConfigEvent{ConfigID: int32(id), Seq: meta.GlobalSeq, Author: int32(meta.Author), CreatedTime: createdTime, EventTime: meta.EventTime, ValueVersion: valueVersion, Value: c}
	return event, pq.NewUpdate(pq.ConfigMutation(meta, id, c))
}

func CreateConfig(store *state.Service, name string, spaceID, directoryID, author int32, value string) (*pq.ConfigEvent, error) {
	if !ValidName(name) {
		return nil, ErrNameInvalid
	}
	ctx := context.Background()
	space := int64(nodes.NormalizedUserSpaceID(spaceID))
	now := nowMillis()
	var created *pq.ConfigEvent
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		dirID, err := ResolveDirectory(ctx, q, space, directoryID)
		if err != nil {
			return nil, err
		}
		if err := requireNameFree(ctx, q, space, dirID, name, 0, 0); err != nil {
			return nil, err
		}
		id, err := q.NextEntityID(ctx, configType)
		if err != nil {
			return nil, err
		}
		var update *state.WriteUpdate
		created, update = configWrite(WriteMeta(seq, now, author, apigen.AuthzVerb_AUTHZ_VERB_CREATE), id, now, 1,
			apigen.Config{Fs: &apigen.ConfigFs{Name: name, DirectoryID: int32(dirID)}, SpaceID: int32(space), Value: value})
		return update, nil
	}); err != nil {
		return nil, err
	}
	return created, nil
}

func AppendConfigVersion(store *state.Service, configID int32, value string, author int32, updateDeployments bool, expected []*apigen.DeploymentExpectedSeq) (*pq.ConfigEvent, []int32, error) {
	ctx := context.Background()
	var written *pq.ConfigEvent
	insert := func(q *pq.Queries, seq, now int64) (int32, *state.WriteUpdate, error) {
		cur, err := currentConfig(ctx, q, configID)
		if err != nil {
			return 0, nil, err
		}
		if cur.Version.Value == value {
			written = pq.ConfigEventOf(cur)
			return int32(cur.Version.ValueVersion), nil, nil
		}
		next := pq.ConfigEntity(cur.Config, cur.Version)
		next.Value = value
		version := int32(cur.Version.ValueVersion) + 1
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

func RenameConfig(store *state.Service, configID int32, newName string) (*pq.ConfigEvent, error) {
	if !ValidName(newName) {
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
		if cur.Config.Name == newName {
			return nil, nil
		}
		if err := requireNameFree(ctx, q, cur.Config.SpaceID, cur.Config.DirectoryID, newName, configType, cur.Config.ID); err != nil {
			return nil, err
		}
		next := pq.ConfigEntity(cur.Config, cur.Version)
		next.Fs.Name = newName
		slog.InfoContext(ctx, fmt.Sprintf("renamed config %d from %s to %s", configID, cur.Config.Name, newName))
		var update *state.WriteUpdate
		current, update = configWrite(WriteMeta(seq, nowMillis(), 0, apigen.AuthzVerb_AUTHZ_VERB_UPDATE), cur.Config.ID, cur.Config.CreatedTime, int32(cur.Version.ValueVersion), next)
		return update, nil
	}); err != nil {
		return nil, err
	}
	return current, nil
}

func MoveConfigDirectory(store *state.Service, configID, newDirectoryID int32) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		cur, err := currentConfig(ctx, q, configID)
		if err != nil {
			return nil, err
		}
		dirID := int64(newDirectoryID)
		if cur.Config.DirectoryID == dirID {
			return nil, nil
		}
		if dirID != 0 {
			dir, err := GetDirectory(ctx, q, dirID)
			if err != nil {
				return nil, err
			}
			if int64(dir.SpaceID) != cur.Config.SpaceID {
				return nil, ErrSpaceMoveUnsupported
			}
		}
		if err := requireNameFree(ctx, q, cur.Config.SpaceID, dirID, cur.Config.Name, configType, cur.Config.ID); err != nil {
			return nil, err
		}
		next := pq.ConfigEntity(cur.Config, cur.Version)
		next.Fs.DirectoryID = int32(dirID)
		return pq.NewUpdate(pq.ConfigMutation(WriteMeta(seq, nowMillis(), 0, apigen.AuthzVerb_AUTHZ_VERB_UPDATE), cur.Config.ID, next)), nil
	})
}

func MoveConfigSpace(store *state.Service, configID, newSpaceID, newDirectoryID, author int32, inlockValidate func(*pq.Queries) error) error {
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
		spaceID := int64(nodes.NormalizedUserSpaceID(newSpaceID))
		dirID := int64(newDirectoryID)
		if spaceID == cur.Config.SpaceID && dirID == cur.Config.DirectoryID {
			return nil, nil
		}
		if dirID != 0 {
			dir, err := GetDirectory(ctx, q, dirID)
			if err != nil {
				return nil, err
			}
			if int64(dir.SpaceID) != spaceID {
				return nil, ErrDirectoryNotFound
			}
		}
		if err := requireNameFree(ctx, q, spaceID, dirID, cur.Config.Name, configType, cur.Config.ID); err != nil {
			return nil, err
		}
		next := pq.ConfigEntity(cur.Config, cur.Version)
		next.Fs.DirectoryID, next.SpaceID = int32(dirID), int32(spaceID)
		return pq.NewUpdate(pq.ConfigMutation(WriteMeta(seq, nowMillis(), author, apigen.AuthzVerb_AUTHZ_VERB_UPDATE), cur.Config.ID, next)), nil
	})
}

func DeleteConfig(store *state.Service, configID int32, inlockValidate func(*pq.Queries) error) (*pq.ConfigEvent, error) {
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
