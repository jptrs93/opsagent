package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// ListConfigs returns every config with its space and value logs, newest
// first, ordered by name.
func (s *Service) ListConfigs() []*apigen.Config {
	ctx := context.Background()
	rows, err := s.q.ListConfigRows(ctx)
	if err != nil {
		panic(fmt.Sprintf("ListConfigRows: %v", err))
	}
	versions, err := s.q.ListConfigVersionRows(ctx)
	if err != nil {
		panic(fmt.Sprintf("ListConfigVersionRows: %v", err))
	}
	spaceRows, err := s.q.ListConfigSpaceRows(ctx)
	if err != nil {
		panic(fmt.Sprintf("ListConfigSpaceRows: %v", err))
	}
	versionsByConfig := make(map[int64][]pq.ConfigVersion, len(rows))
	for _, v := range versions {
		versionsByConfig[v.ConfigID] = append(versionsByConfig[v.ConfigID], v)
	}
	spacesByConfig := make(map[int64][]pq.ConfigSpace, len(rows))
	for _, sp := range spaceRows {
		spacesByConfig[sp.ConfigID] = append(spacesByConfig[sp.ConfigID], sp)
	}
	out := make([]*apigen.Config, 0, len(rows))
	for _, c := range rows {
		vs := versionsByConfig[c.ID]
		if len(vs) == 0 {
			continue
		}
		out = append(out, configFromParts(c, spacesByConfig[c.ID], vs))
	}
	return out
}

// GetConfig returns the config with its space and value logs, or false when
// the config does not exist or has no version.
func (s *Service) GetConfig(configID int32) (*apigen.Config, bool) {
	ctx := context.Background()
	c, err := s.q.GetConfigRowByID(ctx, int64(configID))
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetConfigRowByID: %v", err))
	}
	versions, err := s.q.ListConfigVersionsByConfigID(ctx, c.ID)
	if err != nil {
		panic(fmt.Sprintf("ListConfigVersionsByConfigID: %v", err))
	}
	if len(versions) == 0 {
		return nil, false
	}
	spaces, err := s.q.ListConfigSpaceRowsByConfigID(ctx, c.ID)
	if err != nil {
		panic(fmt.Sprintf("ListConfigSpaceRowsByConfigID: %v", err))
	}
	return configFromParts(c, spaces, versions), true
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
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			panic(fmt.Sprintf("NextGlobalSeq: %v", err))
		}
		id, err := q.InsertConfigRow(ctx, pq.InsertConfigRowParams{
			Name:             name,
			ValueDirectoryID: dirID,
			CreatedAt:        now,
		})
		if err != nil {
			panic(fmt.Sprintf("InsertConfigRow: %v", err))
		}
		if err := q.InsertConfigSpaceRow(ctx, pq.InsertConfigSpaceRowParams{
			ConfigID:  id,
			Author:    int64(author),
			CreatedAt: now,
			SpaceID:   space,
			GlobalSeq: seq,
		}); err != nil {
			panic(fmt.Sprintf("InsertConfigSpaceRow: %v", err))
		}
		configID = id
		if _, err := q.InsertConfigVersion(ctx, pq.InsertConfigVersionParams{
			ConfigID:  id,
			Version:   1,
			Value:     value,
			CreatedAt: now,
			Author:    int64(author),
			GlobalSeq: seq,
		}); err != nil {
			panic(fmt.Sprintf("InsertConfigVersion: %v", err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("create config tx: %v", err))
	}
	c, ok := s.GetConfig(int32(configID))
	if !ok {
		panic(fmt.Sprintf("created config %d not readable", configID))
	}
	return c, nil
}

// AppendConfigVersionWithDeploymentUpdates appends an immutable config version
// and optionally rolls the caller-asserted deployment references to the new
// row atomically.
func (s *Service) AppendConfigVersionWithDeploymentUpdates(configID int32, value string, author int32, updateDeployments bool, expected []storage.DeploymentConfigVersion) (*apigen.Config, []int32, error) {
	ctx := context.Background()
	insert := func(q *pq.Queries, globalSeq int64) (int32, error) {
		if _, err := q.GetConfigRowByID(ctx, int64(configID)); err == sql.ErrNoRows {
			return 0, ErrValueNotFound
		} else if err != nil {
			return 0, fmt.Errorf("get config row: %w", err)
		}
		version, err := q.GetNextConfigVersionNumber(ctx, int64(configID))
		if err != nil {
			return 0, fmt.Errorf("get next config version: %w", err)
		}
		row, err := q.InsertConfigVersion(ctx, pq.InsertConfigVersionParams{
			ConfigID:  int64(configID),
			Version:   version,
			Value:     value,
			CreatedAt: time.Now().UnixMilli(),
			Author:    int64(author),
			GlobalSeq: globalSeq,
		})
		if err != nil {
			return 0, fmt.Errorf("insert config version: %w", err)
		}
		return int32(row.ID), nil
	}
	updatedDeployments, err := s.setVersionedValueWithDeploymentUpdates(
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

// RenameConfig renames the stable config identity. Versions are untouched.
func (s *Service) RenameConfig(configID int32, newName string) (*apigen.Config, error) {
	if !ValidValueName(newName) {
		return nil, ErrValueNameInvalid
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := logu.AddTag(context.Background(), "Store")
	row, err := s.q.GetConfigRowByID(ctx, int64(configID))
	if err == sql.ErrNoRows {
		return nil, ErrValueNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetConfigRowByID: %v", err))
	}
	if row.Name != newName {
		if s.valueSiblingNameTakenLocked(ctx, s.q, row.SpaceID, row.ValueDirectoryID, newName, 0, row.ID, 0) {
			return nil, ErrValueAlreadyExists
		}
		if err := s.q.RenameConfigRow(ctx, pq.RenameConfigRowParams{Name: newName, ID: row.ID}); err != nil {
			panic(fmt.Sprintf("RenameConfigRow: %v", err))
		}
		slog.InfoContext(ctx, fmt.Sprintf("renamed config %d from %s to %s", configID, row.Name, newName))
	}
	c, ok := s.GetConfig(configID)
	if !ok {
		return nil, ErrValueNotFound
	}
	return c, nil
}

// MoveConfigDirectory moves a config to another value directory (0 = the space
// root) in its own space. Version rows are untouched.
func (s *Service) MoveConfigDirectory(configID, newDirectoryID int32) (Config, error) {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	row, err := s.q.GetConfigRowByID(ctx, int64(configID))
	if err == sql.ErrNoRows {
		return Config{}, ErrValueNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetConfigRowByID: %v", err))
	}
	dirID := int64(newDirectoryID)
	if row.ValueDirectoryID == dirID {
		return row, nil
	}
	if dirID != 0 {
		dir, err := s.q.GetValueDirectoryByID(ctx, dirID)
		if err == sql.ErrNoRows {
			return Config{}, ErrValueDirectoryNotFound
		}
		if err != nil {
			panic(fmt.Sprintf("GetValueDirectoryByID: %v", err))
		}
		if dir.SpaceID != row.SpaceID {
			return Config{}, ErrSpaceMoveUnsupported
		}
	}
	if s.valueSiblingNameTakenLocked(ctx, s.q, row.SpaceID, dirID, row.Name, 0, row.ID, 0) {
		return Config{}, ErrValueAlreadyExists
	}
	if err := s.q.SetConfigValueDirectoryID(ctx, pq.SetConfigValueDirectoryIDParams{ValueDirectoryID: dirID, ID: row.ID}); err != nil {
		panic(fmt.Sprintf("SetConfigValueDirectoryID: %v", err))
	}
	row.ValueDirectoryID = dirID
	return row, nil
}

// MoveConfigSpace moves a config to another space, landing it in
// newDirectoryID there (0 = the destination space's root). Version rows are
// untouched, so every pinned reference survives. A space change appends to
// the config_spaces log with author as the acting user. Reference locality is
// the caller's law — the handler refuses the move while anything outside the
// destination space references the config.
func (s *Service) MoveConfigSpace(configID, newSpaceID, newDirectoryID, author int32) error {
	ctx := context.Background()
	s.Mu.Lock()
	defer s.Mu.Unlock()

	row, err := s.q.GetConfigRowByID(ctx, int64(configID))
	if err == sql.ErrNoRows {
		return ErrValueNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetConfigRowByID: %v", err))
	}
	spaceID := int64(normalizedUserSpaceID(newSpaceID))
	dirID := int64(newDirectoryID)
	if spaceID == row.SpaceID && dirID == row.ValueDirectoryID {
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
	if s.valueSiblingNameTakenLocked(ctx, s.q, spaceID, dirID, row.Name, 0, row.ID, 0) {
		return ErrValueAlreadyExists
	}
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		if spaceID != row.SpaceID {
			seq, err := q.NextGlobalSeq(ctx)
			if err != nil {
				panic(fmt.Sprintf("NextGlobalSeq: %v", err))
			}
			if err := q.InsertConfigSpaceRow(ctx, pq.InsertConfigSpaceRowParams{
				ConfigID:  row.ID,
				Author:    int64(author),
				CreatedAt: time.Now().UnixMilli(),
				SpaceID:   spaceID,
				GlobalSeq: seq,
			}); err != nil {
				panic(fmt.Sprintf("InsertConfigSpaceRow: %v", err))
			}
		}
		if err := q.SetConfigValueDirectoryID(ctx, pq.SetConfigValueDirectoryIDParams{ValueDirectoryID: dirID, ID: row.ID}); err != nil {
			panic(fmt.Sprintf("SetConfigValueDirectoryID: %v", err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("config space move tx: %v", err))
	}
	return nil
}

// DeleteConfig soft-deletes the config identity. Version rows and space
// history stay in place, so the delete is recoverable at the DB level; reads
// exclude the config from here on. Returns the deleted config (stamped
// DeletedAt) for notification, or false if absent.
func (s *Service) DeleteConfig(configID int32) (*apigen.Config, bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	c, ok := s.GetConfig(configID)
	if !ok {
		return nil, false
	}
	now := time.Now()
	if err := s.q.SoftDeleteConfigRow(context.Background(), pq.SoftDeleteConfigRowParams{
		DeletedAt: now.UnixMilli(),
		ID:        int64(configID),
	}); err != nil {
		panic(fmt.Sprintf("SoftDeleteConfigRow: %v", err))
	}
	c.DeletedAt = now
	return c, true
}

// ConfigVersionIDs returns every version row id of the config — the set a
// deployment env ref or setting could pin.
func (s *Service) ConfigVersionIDs(configID int32) []int32 {
	rows, err := s.q.ListConfigVersionIDsByConfigID(context.Background(), int64(configID))
	if err != nil {
		panic(fmt.Sprintf("ListConfigVersionIDsByConfigID: %v", err))
	}
	ids := make([]int32, 0, len(rows))
	for _, id := range rows {
		ids = append(ids, int32(id))
	}
	return ids
}

// ConfigVersionRef is one config version row joined with its identity.
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

// GetConfigVersionByID resolves a pinned config version row id.
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
