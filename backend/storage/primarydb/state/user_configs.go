package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func (s *Service) ListConfigMetas() []*apigen.ConfigMeta {
	ctx := context.Background()
	displays, err := s.q.ListConfigDisplays(ctx)
	if err != nil {
		panic(fmt.Sprintf("ListConfigDisplays: %v", err))
	}
	events, err := s.q.ListEventsByType(ctx, eventTypeConfig)
	if err != nil {
		panic(fmt.Sprintf("ListEventsByType: %v", err))
	}
	byEntity := make(map[int64][]pq.Event)
	for _, e := range events {
		if e.Action != eventActionDelete {
			byEntity[e.EntityID] = append(byEntity[e.EntityID], e)
		}
	}
	out := make([]*apigen.ConfigMeta, 0, len(displays))
	for _, d := range displays {
		evs := byEntity[d.ID]
		if len(evs) == 0 {
			continue
		}
		out = append(out, configMetaFromDisplay(d, evs))
	}
	return out
}

func (s *Service) GetConfigMeta(configID int32) (*apigen.ConfigMeta, bool) {
	ctx := context.Background()
	d, err := s.q.GetConfigDisplayByID(ctx, int64(configID))
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetConfigDisplayByID: %v", err))
	}
	events, err := s.q.ListEventsByEntity(ctx, pq.ListEventsByEntityParams{EntityType: eventTypeConfig, EntityID: d.ID})
	if err != nil {
		panic(fmt.Sprintf("ListEventsByEntity: %v", err))
	}
	evs := configValueEvents(events)
	if len(evs) == 0 {
		return nil, false
	}
	return configMetaFromDisplay(d, evs), true
}

func (s *Service) CreateConfigWithVersion(name string, spaceID, directoryID, createdBy int32, value string) (*apigen.ConfigMeta, error) {
	if !ValidValueName(name) {
		return nil, ErrValueNameInvalid
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := context.Background()
	space := int64(normalizedUserSpaceID(spaceID))
	dirID, err := s.resolveValueDirectoryLocked(ctx, space, directoryID)
	if err != nil {
		return nil, err
	}
	if s.valueSiblingNameTakenLocked(ctx, s.q, space, dirID, name, 0, 0, 0) {
		return nil, ErrValueAlreadyExists
	}
	now := time.Now().UnixMilli()
	var display pq.ConfigDisplay
	var event pq.Event
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		maxID, err := q.MaxEventEntityID(ctx, eventTypeConfig)
		if err != nil {
			panic(fmt.Sprintf("MaxEventEntityID: %v", err))
		}
		id := maxID + 1
		seq, err := q.InsertEvent(ctx, pq.InsertEventParams{
			Ts:         now,
			AuthorID:   int64(createdBy),
			EntityType: eventTypeConfig,
			EntityID:   id,
			Action:     eventActionCreate,
			Blob:       []byte(value),
		})
		if err != nil {
			panic(fmt.Sprintf("InsertEvent: %v", err))
		}
		if err := q.InsertConfigDisplay(ctx, pq.InsertConfigDisplayParams{
			ID:          id,
			SpaceID:     space,
			Name:        name,
			DirectoryID: dirID,
			UpdatedAt:   now,
			UpdatedBy:   int64(createdBy),
		}); err != nil {
			panic(fmt.Sprintf("InsertConfigDisplay: %v", err))
		}
		display = pq.ConfigDisplay{ID: id, SpaceID: space, Name: name, DirectoryID: dirID, UpdatedAt: now, UpdatedBy: int64(createdBy)}
		event = pq.Event{ID: seq, Ts: now, AuthorID: int64(createdBy), EntityType: eventTypeConfig, EntityID: id, Action: eventActionCreate, Blob: []byte(value)}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("create config tx: %v", err))
	}
	return configMetaFromDisplay(display, []pq.Event{event}), nil
}

func (s *Service) AppendConfigVersionWithDeploymentUpdates(configID int32, value string, updatedBy int32, updateDeployments bool, expected []storage.DeploymentConfigVersion) (*apigen.ConfigMeta, []int32, error) {
	ctx := context.Background()
	insert := func(q *pq.Queries) (int32, error) {
		if _, err := q.GetConfigDisplayByID(ctx, int64(configID)); err == sql.ErrNoRows {
			return 0, ErrValueNotFound
		} else if err != nil {
			return 0, fmt.Errorf("get config display: %w", err)
		}
		seq, err := q.InsertEvent(ctx, pq.InsertEventParams{
			Ts:         time.Now().UnixMilli(),
			AuthorID:   int64(updatedBy),
			EntityType: eventTypeConfig,
			EntityID:   int64(configID),
			Action:     eventActionUpdate,
			Blob:       []byte(value),
		})
		if err != nil {
			return 0, fmt.Errorf("insert config event: %w", err)
		}
		return int32(seq), nil
	}
	updatedDeployments, err := s.setVersionedValueWithDeploymentUpdates(
		configValueReference, configID, updateDeployments, expected, updatedBy, insert, nil)
	if err != nil {
		return nil, nil, err
	}
	meta, ok := s.GetConfigMeta(configID)
	if !ok {
		panic(fmt.Sprintf("config %d missing after append", configID))
	}
	return meta, updatedDeployments, nil
}

func (s *Service) SetConfigByName(name, value string, updatedBy int32) *apigen.ConfigMeta {
	d, err := s.q.GetConfigDisplayByName(context.Background(), pq.GetConfigDisplayByNameParams{
		SpaceID:     int64(DefaultSpaceID),
		DirectoryID: 0,
		Name:        name,
	})
	if err == sql.ErrNoRows {
		meta, createErr := s.CreateConfigWithVersion(name, DefaultSpaceID, 0, updatedBy, value)
		if createErr != nil {
			panic(fmt.Sprintf("SetConfigByName create: %v", createErr))
		}
		return meta
	}
	if err != nil {
		panic(fmt.Sprintf("GetConfigDisplayByName: %v", err))
	}
	meta, _, appendErr := s.AppendConfigVersionWithDeploymentUpdates(int32(d.ID), value, updatedBy, false, nil)
	if appendErr != nil {
		panic(fmt.Sprintf("SetConfigByName append: %v", appendErr))
	}
	return meta
}

func (s *Service) RenameConfig(configID int32, newName string) (*apigen.ConfigMeta, error) {
	if !ValidValueName(newName) {
		return nil, ErrValueNameInvalid
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := context.Background()
	d, err := s.q.GetConfigDisplayByID(ctx, int64(configID))
	if err == sql.ErrNoRows {
		return nil, ErrValueNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetConfigDisplayByID: %v", err))
	}
	if d.Name != newName {
		if s.valueSiblingNameTakenLocked(ctx, s.q, d.SpaceID, d.DirectoryID, newName, 0, d.ID, 0) {
			return nil, ErrValueAlreadyExists
		}
		if err := s.q.RenameConfigDisplay(ctx, pq.RenameConfigDisplayParams{Name: newName, UpdatedAt: time.Now().UnixMilli(), UpdatedBy: d.UpdatedBy, ID: d.ID}); err != nil {
			panic(fmt.Sprintf("RenameConfigDisplay: %v", err))
		}
		slog.Info("renamed config", "id", configID, "name", d.Name, "newName", newName)
	}
	meta, ok := s.GetConfigMeta(configID)
	if !ok {
		return nil, ErrValueNotFound
	}
	return meta, nil
}

func (s *Service) MoveConfigDirectory(configID, newDirectoryID int32) error {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	d, err := s.q.GetConfigDisplayByID(ctx, int64(configID))
	if err == sql.ErrNoRows {
		return ErrValueNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetConfigDisplayByID: %v", err))
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
	if s.valueSiblingNameTakenLocked(ctx, s.q, d.SpaceID, dirID, d.Name, 0, d.ID, 0) {
		return ErrValueAlreadyExists
	}
	if err := s.q.SetConfigDisplayDirectory(ctx, pq.SetConfigDisplayDirectoryParams{DirectoryID: dirID, UpdatedAt: time.Now().UnixMilli(), UpdatedBy: d.UpdatedBy, ID: d.ID}); err != nil {
		panic(fmt.Sprintf("SetConfigDisplayDirectory: %v", err))
	}
	return nil
}

func (s *Service) MoveConfigSpace(configID, newSpaceID, newDirectoryID int32) error {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	d, err := s.q.GetConfigDisplayByID(ctx, int64(configID))
	if err == sql.ErrNoRows {
		return ErrValueNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetConfigDisplayByID: %v", err))
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
	if s.valueSiblingNameTakenLocked(ctx, s.q, spaceID, dirID, d.Name, 0, d.ID, 0) {
		return ErrValueAlreadyExists
	}
	if err := s.q.SetConfigDisplaySpace(ctx, pq.SetConfigDisplaySpaceParams{SpaceID: spaceID, DirectoryID: dirID, UpdatedAt: time.Now().UnixMilli(), UpdatedBy: d.UpdatedBy, ID: d.ID}); err != nil {
		panic(fmt.Sprintf("SetConfigDisplaySpace: %v", err))
	}
	return nil
}

func (s *Service) DeleteConfig(configID int32) (*apigen.ConfigMeta, bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	meta, ok := s.GetConfigMeta(configID)
	if !ok {
		return nil, false
	}
	ctx := context.Background()
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		if _, err := q.InsertEvent(ctx, pq.InsertEventParams{
			Ts:         time.Now().UnixMilli(),
			AuthorID:   0,
			EntityType: eventTypeConfig,
			EntityID:   int64(configID),
			Action:     eventActionDelete,
			Blob:       []byte{},
		}); err != nil {
			panic(fmt.Sprintf("InsertEvent: %v", err))
		}
		if err := q.DeleteConfigDisplay(ctx, int64(configID)); err != nil {
			panic(fmt.Sprintf("DeleteConfigDisplay: %v", err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("delete config tx: %v", err))
	}
	meta.Deleted = true
	return meta, true
}

func (s *Service) ConfigVersionIDs(configID int32) []int32 {
	events, err := s.q.ListEventsByEntity(context.Background(), pq.ListEventsByEntityParams{EntityType: eventTypeConfig, EntityID: int64(configID)})
	if err != nil {
		panic(fmt.Sprintf("ListEventsByEntity: %v", err))
	}
	evs := configValueEvents(events)
	ids := make([]int32, 0, len(evs))
	for _, e := range evs {
		ids = append(ids, int32(e.ID))
	}
	return ids
}

type ConfigVersionRef struct {
	ID        int32
	ConfigID  int32
	Name      string
	SpaceID   int32
	Version   int32
	Value     string
	CreatedAt int64
	CreatedBy int32
}

func (s *Service) GetConfigVersionByID(id int32) (ConfigVersionRef, bool) {
	ctx := context.Background()
	e, err := s.q.GetEventByID(ctx, int64(id))
	if err == sql.ErrNoRows {
		return ConfigVersionRef{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetEventByID: %v", err))
	}
	if e.EntityType != eventTypeConfig || e.Action == eventActionDelete {
		return ConfigVersionRef{}, false
	}
	d, err := s.q.GetConfigDisplayByID(ctx, e.EntityID)
	if err == sql.ErrNoRows {
		return ConfigVersionRef{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetConfigDisplayByID: %v", err))
	}
	events, err := s.q.ListEventsByEntity(ctx, pq.ListEventsByEntityParams{EntityType: eventTypeConfig, EntityID: e.EntityID})
	if err != nil {
		panic(fmt.Sprintf("ListEventsByEntity: %v", err))
	}
	version := 0
	for _, ev := range configValueEvents(events) {
		version++
		if ev.ID == e.ID {
			break
		}
	}
	return ConfigVersionRef{
		ID:        id,
		ConfigID:  int32(e.EntityID),
		Name:      d.Name,
		SpaceID:   int32(d.SpaceID),
		Version:   int32(version),
		Value:     string(e.Blob),
		CreatedAt: e.Ts,
		CreatedBy: int32(e.AuthorID),
	}, true
}

func (s *Service) ResolveConfig(id int32) (string, bool) {
	ref, ok := s.GetConfigVersionByID(id)
	if !ok {
		return "", false
	}
	return ref.Value, true
}

func (s *Service) ResolveConfigs(ids []int32) (map[int32]string, error) {
	out := make(map[int32]string, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, errors.New("config id is required")
		}
		if _, ok := out[id]; ok {
			continue
		}
		ref, ok := s.GetConfigVersionByID(id)
		if !ok {
			return nil, fmt.Errorf("config not found: id %d", id)
		}
		out[id] = ref.Value
	}
	return out, nil
}
