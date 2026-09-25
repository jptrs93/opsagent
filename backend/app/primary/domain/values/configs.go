package values

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"log/slog"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func ListConfigs(q *pq.Queries) []*apigen.ConfigEvent {
	rows := erru.Must(q.ListLatestLiveConfigEvents(context.Background()))
	if rows == nil {
		rows = []*apigen.ConfigEvent{}
	}
	return rows
}

func GetConfig(q *pq.Queries, configID int32) (*apigen.ConfigEvent, bool) {
	row, err := q.GetLatestConfigEvent(context.Background(), int64(configID))
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

type ConfigVersion struct {
	ID        int32
	ConfigID  int32
	Name      string
	SpaceID   int32
	Version   int32
	Value     string
	CreatedAt int64
	Author    int32
}

func GetConfigVersion(q *pq.Queries, ref apigen.ValueRef) (ConfigVersion, bool) {
	r, err := q.GetConfigVersionByRef(context.Background(), ref)
	if errors.Is(err, sql.ErrNoRows) {
		return ConfigVersion{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetConfigVersionByRef: %v", err))
	}
	return ConfigVersion{
		ID: int32(r.ID), ConfigID: int32(r.ConfigID), Name: r.Name, SpaceID: int32(r.SpaceID),
		Version: int32(r.Version), Value: r.Value, CreatedAt: r.CreatedAt, Author: int32(r.Author),
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

func nextConfigEvent(prev *apigen.ConfigEvent, author int32, eventType apigen.EventType) apigen.ConfigEvent {
	event := *prev
	event.EventID, event.Seq = 0, 0
	event.EventTime = time.Now().UnixMilli()
	event.Author, event.EventType = author, eventType
	event.Version++
	fs := *prev.Value.Fs
	event.Value.Fs = &fs
	return event
}

func appendConfigEvent(ctx context.Context, q *pq.Queries, seq int64, event apigen.ConfigEvent) (*state.Update, error) {
	event.Seq = seq
	if err := q.InsertConfigEvent(ctx, &event); err != nil {
		return nil, err
	}
	return &apigen.CoreUpdate{ConfigEvents: []*apigen.ConfigEvent{&event}}, nil
}

func latestConfigEvent(ctx context.Context, q *pq.Queries, configID int32) (*apigen.ConfigEvent, error) {
	e, err := q.GetLatestConfigEvent(ctx, int64(configID))
	if errors.Is(err, sql.ErrNoRows) || err == nil && e.EventType == apigen.EventType_EVENT_TYPE_DELETE {
		return nil, ErrNotFound
	}
	return e, err
}

func CreateConfig(store *state.Service, name string, spaceID, directoryID, author int32, value string) (*apigen.ConfigEvent, error) {
	if !ValidName(name) {
		return nil, ErrNameInvalid
	}
	ctx := context.Background()
	space := int64(nodes.NormalizedUserSpaceID(spaceID))
	now := time.Now().UnixMilli()
	var created *apigen.ConfigEvent
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		dirID, err := ResolveDirectory(ctx, q, space, directoryID)
		if err != nil {
			return nil, err
		}
		if err := requireNameFree(ctx, q, space, dirID, name, 0, 0, 0); err != nil {
			return nil, err
		}
		id, err := q.NextConfigID(ctx)
		if err != nil {
			return nil, err
		}
		event := apigen.ConfigEvent{
			Seq: seq, EventTime: now, CreatedTime: now, Author: author, ConfigID: int32(id),
			Version: 1, ValueVersion: 1,
			Value:     apigen.Config{Fs: &apigen.ConfigFs{Name: name, DirectoryID: int32(dirID)}, SpaceID: int32(space), Value: value},
			EventType: apigen.EventType_EVENT_TYPE_CREATE,
		}
		if err := q.InsertConfigEvent(ctx, &event); err != nil {
			return nil, err
		}
		created = &event
		return &apigen.CoreUpdate{ConfigEvents: []*apigen.ConfigEvent{&event}}, nil
	}); err != nil {
		return nil, err
	}
	return created, nil
}

func AppendConfigVersion(store *state.Service, configID int32, value string, author int32, updateDeployments bool, expected []storage.DeploymentSpecVersion) (*apigen.ConfigEvent, []int32, error) {
	ctx := context.Background()
	var written *apigen.ConfigEvent
	insert := func(q *pq.Queries, globalSeq int64) (int32, apigen.CoreUpdate, error) {
		prev, err := latestConfigEvent(ctx, q, configID)
		if err != nil {
			return 0, apigen.CoreUpdate{}, err
		}
		event := nextConfigEvent(prev, author, apigen.EventType_EVENT_TYPE_UPDATE)
		event.ValueVersion = prev.ValueVersion + 1
		event.Value.Value = value
		event.Seq = globalSeq
		if err := q.InsertConfigEvent(ctx, &event); err != nil {
			return 0, apigen.CoreUpdate{}, fmt.Errorf("insert config value event: %w", err)
		}
		written = &event
		return event.ValueVersion, apigen.CoreUpdate{Seq: globalSeq, ConfigEvents: []*apigen.ConfigEvent{&event}}, nil
	}
	updatedDeployments, err := SetVersionedValueWithDeploymentUpdates(store, ConfigReference, configID, updateDeployments, expected, author, insert, nil)
	if err != nil {
		return nil, nil, err
	}
	return written, updatedDeployments, nil
}

func RenameConfig(store *state.Service, configID int32, newName string) (*apigen.ConfigEvent, error) {
	if !ValidName(newName) {
		return nil, ErrNameInvalid
	}
	ctx := logu.AddTag(context.Background(), "Values")
	var current *apigen.ConfigEvent
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		prev, err := latestConfigEvent(ctx, q, configID)
		if err != nil {
			return nil, err
		}
		current = prev
		if prev.Value.Fs.Name == newName {
			return nil, nil
		}
		if err := requireNameFree(ctx, q, int64(prev.Value.SpaceID), int64(prev.Value.Fs.DirectoryID), newName, 0, int64(prev.ConfigID), 0); err != nil {
			return nil, err
		}
		event := nextConfigEvent(prev, 0, apigen.EventType_EVENT_TYPE_UPDATE)
		event.Value.Fs.Name = newName
		slog.InfoContext(ctx, fmt.Sprintf("renamed config %d from %s to %s", configID, prev.Value.Fs.Name, newName))
		update, err := appendConfigEvent(ctx, q, seq, event)
		if err != nil {
			return nil, err
		}
		current = update.ConfigEvents[0]
		return update, nil
	}); err != nil {
		return nil, err
	}
	return current, nil
}

func MoveConfigDirectory(store *state.Service, configID, newDirectoryID int32) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		prev, err := latestConfigEvent(ctx, q, configID)
		if err != nil {
			return nil, err
		}
		dirID := int64(newDirectoryID)
		if int64(prev.Value.Fs.DirectoryID) == dirID {
			return nil, nil
		}
		if dirID != 0 {
			dir, err := GetDirectory(ctx, q, dirID)
			if err != nil {
				return nil, err
			}
			if dir.SpaceID != prev.Value.SpaceID {
				return nil, ErrSpaceMoveUnsupported
			}
		}
		if err := requireNameFree(ctx, q, int64(prev.Value.SpaceID), dirID, prev.Value.Fs.Name, 0, int64(prev.ConfigID), 0); err != nil {
			return nil, err
		}
		event := nextConfigEvent(prev, 0, apigen.EventType_EVENT_TYPE_UPDATE)
		event.Value.Fs.DirectoryID = int32(dirID)
		return appendConfigEvent(ctx, q, seq, event)
	})
}

func MoveConfigSpace(store *state.Service, configID, newSpaceID, newDirectoryID, author int32, inlockValidate func(*pq.Queries) error) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		if inlockValidate != nil {
			if err := inlockValidate(q); err != nil {
				return nil, err
			}
		}
		prev, err := latestConfigEvent(ctx, q, configID)
		if err != nil {
			return nil, err
		}
		spaceID := int64(nodes.NormalizedUserSpaceID(newSpaceID))
		dirID := int64(newDirectoryID)
		if spaceID == int64(prev.Value.SpaceID) && dirID == int64(prev.Value.Fs.DirectoryID) {
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
		if err := requireNameFree(ctx, q, spaceID, dirID, prev.Value.Fs.Name, 0, int64(prev.ConfigID), 0); err != nil {
			return nil, err
		}
		event := nextConfigEvent(prev, author, apigen.EventType_EVENT_TYPE_UPDATE)
		event.Value.Fs.DirectoryID = int32(dirID)
		event.Value.SpaceID = int32(spaceID)
		return appendConfigEvent(ctx, q, seq, event)
	})
}

func DeleteConfig(store *state.Service, configID int32, inlockValidate func(*pq.Queries) error) (*apigen.ConfigEvent, error) {
	ctx := context.Background()
	var deleted *apigen.ConfigEvent
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		if inlockValidate != nil {
			if err := inlockValidate(q); err != nil {
				return nil, err
			}
		}
		prev, err := latestConfigEvent(ctx, q, configID)
		if err != nil {
			return nil, err
		}
		update, err := appendConfigEvent(ctx, q, seq, nextConfigEvent(prev, 0, apigen.EventType_EVENT_TYPE_DELETE))
		if err != nil {
			return nil, err
		}
		deleted = update.ConfigEvents[0]
		return update, nil
	})
	if err != nil {
		return nil, err
	}
	return deleted, nil
}
