package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// ListConfigs returns every live config with its space and value logs, newest
// first, ordered by name.
func (s *Service) ListConfigs() []*apigen.Config {
	events := erru.Must(s.q.ListAllConfigEvents(context.Background()))
	byConfig := groupConfigEvents(events)
	out := make([]*apigen.Config, 0, len(byConfig))
	for _, group := range byConfig {
		if group[len(group)-1].EventType == pq.EventDelete {
			continue
		}
		out = append(out, configFromEvents(group))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fs.Name < out[j].Fs.Name })
	return out
}

func groupConfigEvents(events []pq.ConfigEvent) [][]pq.ConfigEvent {
	var out [][]pq.ConfigEvent
	for _, e := range events {
		if n := len(out); n > 0 && out[n-1][0].ConfigID == e.ConfigID {
			out[n-1] = append(out[n-1], e)
			continue
		}
		out = append(out, []pq.ConfigEvent{e})
	}
	return out
}

// GetConfig returns the config with its space and value logs, or false when
// the config does not exist or is deleted.
func (s *Service) GetConfig(configID int32) (*apigen.Config, bool) {
	ctx := context.Background()
	if _, err := s.q.GetConfigRowByID(ctx, int64(configID)); err == sql.ErrNoRows {
		return nil, false
	} else if err != nil {
		panic(fmt.Sprintf("GetConfigRowByID: %v", err))
	}
	events := erru.Must(s.q.ListConfigEvents(ctx, int64(configID)))
	if len(events) == 0 {
		return nil, false
	}
	return configFromEvents(events), true
}

func nextConfigEvent(prev pq.ConfigEvent, author int32, eventType int64) pq.ConfigEvent {
	return pq.ConfigEvent{
		EventTime:        time.Now().UnixMilli(),
		CreatedTime:      prev.CreatedTime,
		Author:           int64(author),
		ConfigID:         prev.ConfigID,
		Version:          prev.Version + 1,
		ValueVersion:     prev.ValueVersion,
		SpaceVersion:     prev.SpaceVersion,
		Name:             prev.Name,
		ValueDirectoryID: prev.ValueDirectoryID,
		SpaceID:          prev.SpaceID,
		EventType:        eventType,
	}
}

func (s *Service) appendConfigEventLocked(ctx context.Context, event pq.ConfigEvent) {
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		event.GlobalSeq = seq
		_, err = q.InsertConfigEvent(ctx, event)
		return err
	}); err != nil {
		panic(fmt.Sprintf("append config event: %v", err))
	}
}

func (s *Service) mustLatestConfigEventLocked(ctx context.Context, configID int32) (pq.ConfigEvent, bool) {
	e, err := s.q.GetLatestConfigEvent(ctx, int64(configID))
	if errors.Is(err, sql.ErrNoRows) {
		return pq.ConfigEvent{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetLatestConfigEvent: %v", err))
	}
	return e, e.EventType != pq.EventDelete
}

// CreateConfigWithVersion creates a new config in directoryID (0 = the root)
// of spaceID with its first version.
func (s *Service) CreateConfigWithVersion(name string, spaceID, directoryID, author int32, value string) (*apigen.Config, error) {
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
	var configID int64
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		seq := erru.Must(q.NextGlobalSeq(ctx))
		id := erru.Must(q.NextConfigID(ctx))
		configID = id
		_, err := q.InsertConfigEvent(ctx, pq.ConfigEvent{
			GlobalSeq:        seq,
			EventTime:        now,
			CreatedTime:      now,
			Author:           int64(author),
			ConfigID:         id,
			Version:          1,
			ValueVersion:     1,
			SpaceVersion:     1,
			Name:             name,
			ValueDirectoryID: dirID,
			SpaceID:          space,
			Value:            sql.NullString{String: value, Valid: true},
			EventType:        pq.EventCreate,
		})
		return err
	}); err != nil {
		panic(fmt.Sprintf("create config tx: %v", err))
	}
	c, ok := s.GetConfig(int32(configID))
	if !ok {
		panic(fmt.Sprintf("created config %d not readable", configID))
	}
	return c, nil
}

// AppendConfigVersionWithDeploymentUpdates appends an immutable value version
// and optionally rolls the caller-asserted deployment references to the new
// row atomically.
func (s *Service) AppendConfigVersionWithDeploymentUpdatesLocked(configID int32, value string, author int32, updateDeployments bool, expected []storage.DeploymentSpecVersion) (*apigen.Config, []int32, error) {
	ctx := context.Background()
	insert := func(q *pq.Queries, globalSeq int64) (int32, error) {
		prev, err := q.GetLatestConfigEvent(ctx, int64(configID))
		if err == sql.ErrNoRows {
			return 0, ErrValueNotFound
		} else if err != nil {
			return 0, fmt.Errorf("get latest config event: %w", err)
		}
		if prev.EventType == pq.EventDelete {
			return 0, ErrValueNotFound
		}
		event := nextConfigEvent(prev, author, pq.EventUpdate)
		event.ValueVersion = prev.ValueVersion + 1
		event.Value = sql.NullString{String: value, Valid: true}
		event.GlobalSeq = globalSeq
		id, err := q.InsertConfigEvent(ctx, event)
		if err != nil {
			return 0, fmt.Errorf("insert config value event: %w", err)
		}
		return int32(id), nil
	}
	updatedDeployments, err := s.setVersionedValueWithDeploymentUpdatesLocked(
		configValueReference, configID, updateDeployments, expected, author, insert, nil)
	if err != nil {
		return nil, nil, err
	}
	c, ok := s.GetConfig(configID)
	if !ok {
		panic(fmt.Sprintf("config %d missing after append", configID))
	}
	return c, updatedDeployments, nil
}

// RenameConfig renames the stable config identity as an event. Value versions
// are untouched.
func (s *Service) RenameConfig(configID int32, newName string) (*apigen.Config, error) {
	if !ValidValueName(newName) {
		return nil, ErrValueNameInvalid
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := logu.AddTag(context.Background(), "Store")
	prev, ok := s.mustLatestConfigEventLocked(ctx, configID)
	if !ok {
		return nil, ErrValueNotFound
	}
	if prev.Name != newName {
		if s.valueSiblingNameTakenLocked(ctx, s.q, prev.SpaceID, prev.ValueDirectoryID, newName, 0, prev.ConfigID, 0) {
			return nil, ErrValueAlreadyExists
		}
		event := nextConfigEvent(prev, 0, pq.EventUpdate)
		event.Name = newName
		s.appendConfigEventLocked(ctx, event)
		slog.InfoContext(ctx, fmt.Sprintf("renamed config %d from %s to %s", configID, prev.Name, newName))
	}
	c, ok := s.GetConfig(configID)
	if !ok {
		return nil, ErrValueNotFound
	}
	return c, nil
}

// MoveConfigDirectory moves a config to another value directory (0 = the space
// root) in its own space. Value versions are untouched.
func (s *Service) MoveConfigDirectory(configID, newDirectoryID int32) (Config, error) {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	prev, ok := s.mustLatestConfigEventLocked(ctx, configID)
	if !ok {
		return Config{}, ErrValueNotFound
	}
	dirID := int64(newDirectoryID)
	current := Config{ID: prev.ConfigID, Name: prev.Name, SpaceID: prev.SpaceID, ValueDirectoryID: prev.ValueDirectoryID, CreatedAt: prev.CreatedTime}
	if prev.ValueDirectoryID == dirID {
		return current, nil
	}
	if dirID != 0 {
		dir, err := s.q.GetValueDirectoryByID(ctx, dirID)
		if err == sql.ErrNoRows {
			return Config{}, ErrValueDirectoryNotFound
		}
		if err != nil {
			panic(fmt.Sprintf("GetValueDirectoryByID: %v", err))
		}
		if dir.SpaceID != prev.SpaceID {
			return Config{}, ErrSpaceMoveUnsupported
		}
	}
	if s.valueSiblingNameTakenLocked(ctx, s.q, prev.SpaceID, dirID, prev.Name, 0, prev.ConfigID, 0) {
		return Config{}, ErrValueAlreadyExists
	}
	event := nextConfigEvent(prev, 0, pq.EventUpdate)
	event.ValueDirectoryID = dirID
	s.appendConfigEventLocked(ctx, event)
	current.ValueDirectoryID = dirID
	return current, nil
}

// MoveConfigSpace moves a config to another space, landing it in
// newDirectoryID there (0 = the destination space's root). Value versions are
// untouched, so every pinned reference survives. A space change bumps the
// space facet with author as the acting user; a directory-only call appends
// no space history. Reference locality is the caller's law — the handler
// refuses the move while anything outside the destination space references
// the config.
func (s *Service) MoveConfigSpaceLocked(configID, newSpaceID, newDirectoryID, author int32) error {
	ctx := context.Background()

	prev, ok := s.mustLatestConfigEventLocked(ctx, configID)
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
	if s.valueSiblingNameTakenLocked(ctx, s.q, spaceID, dirID, prev.Name, 0, prev.ConfigID, 0) {
		return ErrValueAlreadyExists
	}
	event := nextConfigEvent(prev, author, pq.EventUpdate)
	event.ValueDirectoryID = dirID
	if spaceID != prev.SpaceID {
		event.SpaceID = spaceID
		event.SpaceVersion = prev.SpaceVersion + 1
	}
	s.appendConfigEventLocked(ctx, event)
	return nil
}

// DeleteConfig appends the terminal delete event. Value versions and space
// history stay in the log, so pinned references keep resolving; current-state
// reads exclude the config from here on and the name is freed. Returns the
// deleted config (stamped DeletedAt) for notification, or false if absent.
func (s *Service) DeleteConfigLocked(configID int32) (*apigen.Config, bool) {
	c, ok := s.GetConfig(configID)
	if !ok {
		return nil, false
	}
	ctx := context.Background()
	prev, ok := s.mustLatestConfigEventLocked(ctx, configID)
	if !ok {
		return nil, false
	}
	now := time.Now()
	s.appendConfigEventLocked(ctx, nextConfigEvent(prev, 0, pq.EventDelete))
	c.DeletedAt = now
	return c, true
}

// ConfigVersionIDs returns every value version row id of the config — the set
// a deployment env ref or setting could pin.
func (s *Service) ConfigVersionIDs(configID int32) []int32 {
	rows := erru.Must(s.q.ListConfigVersionIDsByConfigID(context.Background(), int64(configID)))
	ids := make([]int32, 0, len(rows))
	for _, id := range rows {
		ids = append(ids, int32(id))
	}
	return ids
}

// ConfigVersionRef is one value version row joined with its identity.
type ConfigVersionRef struct {
	ID        int32 // version row id
	ConfigID  int32
	Name      string
	SpaceID   int32
	Version   int32
	Value     string
	CreatedAt int64
	Author    int32
}

// GetConfigVersionByID resolves a pinned value version row id.
func (s *Service) GetConfigVersionByID(id int32) (ConfigVersionRef, bool) {
	r, err := s.q.GetConfigVersionByID(context.Background(), int64(id))
	if err == sql.ErrNoRows {
		return ConfigVersionRef{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetConfigVersionByID: %v", err))
	}
	return ConfigVersionRef{
		ID:        int32(r.ID),
		ConfigID:  int32(r.ConfigID),
		Name:      r.Name,
		SpaceID:   int32(r.SpaceID),
		Version:   int32(r.Version),
		Value:     r.Value,
		CreatedAt: r.CreatedAt,
		Author:    int32(r.Author),
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
