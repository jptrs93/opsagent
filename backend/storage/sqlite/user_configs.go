package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

// configMetaFromRow builds the wire meta: identity at the root, version facts
// only in VersionRefs (newest first). refs must be non-empty — every config
// has at least one version by construction.
func configMetaFromRow(c Config, refs []*apigen.ConfigVersionMeta) *apigen.ConfigMeta {
	return &apigen.ConfigMeta{
		ID:               int32(c.ID),
		Name:             c.Name,
		SpaceID:          int32(c.SpaceID),
		ValueDirectoryID: int32(c.ValueDirectoryID),
		CreatedAt:        time.UnixMilli(c.CreatedAt),
		CreatedBy:        int32(c.CreatedBy),
		VersionRefs:      refs,
	}
}

func configVersionMetaFromRow(v ConfigVersion) *apigen.ConfigVersionMeta {
	return &apigen.ConfigVersionMeta{
		ID:        int32(v.ID),
		Version:   int32(v.Version),
		Value:     v.Value,
		CreatedAt: time.UnixMilli(v.CreatedAt),
		CreatedBy: int32(v.CreatedBy),
	}
}

// ListConfigMetas returns every config with its full version index, newest
// version first, ordered by name.
func (s *PrimaryStorage) ListConfigMetas() []*apigen.ConfigMeta {
	ctx := context.Background()
	rows, err := s.q.ListConfigRows(ctx)
	if err != nil {
		panic(fmt.Sprintf("ListConfigRows: %v", err))
	}
	versions, err := s.q.ListConfigVersionRows(ctx)
	if err != nil {
		panic(fmt.Sprintf("ListConfigVersionRows: %v", err))
	}
	refsByConfig := make(map[int64][]*apigen.ConfigVersionMeta)
	for _, v := range versions {
		// ListConfigVersionRows is version ASC; prepend to end up newest first.
		refsByConfig[v.ConfigID] = append([]*apigen.ConfigVersionMeta{configVersionMetaFromRow(v)}, refsByConfig[v.ConfigID]...)
	}
	out := make([]*apigen.ConfigMeta, 0, len(rows))
	for _, c := range rows {
		refs := refsByConfig[c.ID]
		if len(refs) == 0 {
			continue
		}
		out = append(out, configMetaFromRow(c, refs))
	}
	return out
}

// GetConfigMeta returns one config with its full version index, newest first.
func (s *PrimaryStorage) GetConfigMeta(configID int32) (*apigen.ConfigMeta, bool) {
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
	refs := make([]*apigen.ConfigVersionMeta, 0, len(versions))
	for i := len(versions) - 1; i >= 0; i-- {
		refs = append(refs, configVersionMetaFromRow(versions[i]))
	}
	return configMetaFromRow(c, refs), true
}

// CreateConfigWithVersion creates a new config in the root directory of
// spaceID with its first version.
func (s *PrimaryStorage) CreateConfigWithVersion(name string, spaceID, createdBy int32, value string) (*apigen.ConfigMeta, error) {
	if !ValidValueName(name) {
		return nil, ErrValueNameInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	space := int64(normalizedUserSpaceID(spaceID))
	if s.valueSiblingNameTakenLocked(ctx, s.q, space, 0, name, 0, 0) {
		return nil, ErrValueAlreadyExists
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin create config tx: %v", err))
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)
	now := time.Now().UnixMilli()
	row, err := q.InsertConfigRow(ctx, InsertConfigRowParams{
		Name:             name,
		SpaceID:          space,
		ValueDirectoryID: 0,
		CreatedAt:        now,
		CreatedBy:        int64(createdBy),
	})
	if err != nil {
		panic(fmt.Sprintf("InsertConfigRow: %v", err))
	}
	version, err := q.InsertConfigVersion(ctx, InsertConfigVersionParams{
		ConfigID:  row.ID,
		Version:   1,
		Value:     value,
		CreatedAt: now,
		CreatedBy: int64(createdBy),
	})
	if err != nil {
		panic(fmt.Sprintf("InsertConfigVersion: %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit create config tx: %v", err))
	}
	return configMetaFromRow(row, []*apigen.ConfigVersionMeta{configVersionMetaFromRow(version)}), nil
}

// AppendConfigVersionWithDeploymentUpdates appends an immutable config version
// and optionally rolls the caller-asserted deployment references to the new
// row atomically.
func (s *PrimaryStorage) AppendConfigVersionWithDeploymentUpdates(configID int32, value string, updatedBy int32, updateDeployments bool, expected []storage.DeploymentConfigVersion) (*apigen.ConfigMeta, []int32, error) {
	ctx := context.Background()
	insert := func(q *Queries) (int32, error) {
		if _, err := q.GetConfigRowByID(ctx, int64(configID)); err == sql.ErrNoRows {
			return 0, ErrValueNotFound
		} else if err != nil {
			return 0, fmt.Errorf("get config row: %w", err)
		}
		version, err := q.GetNextConfigVersionNumber(ctx, int64(configID))
		if err != nil {
			return 0, fmt.Errorf("get next config version: %w", err)
		}
		row, err := q.InsertConfigVersion(ctx, InsertConfigVersionParams{
			ConfigID:  int64(configID),
			Version:   version,
			Value:     value,
			CreatedAt: time.Now().UnixMilli(),
			CreatedBy: int64(updatedBy),
		})
		if err != nil {
			return 0, fmt.Errorf("insert config version: %w", err)
		}
		return int32(row.ID), nil
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

// SetConfigByName is a create-or-append convenience for tests and seeding: it
// targets the root directory of the default space by name.
func (s *PrimaryStorage) SetConfigByName(name, value string, updatedBy int32) *apigen.ConfigMeta {
	row, err := s.q.GetConfigInDirectoryByName(context.Background(), GetConfigInDirectoryByNameParams{
		SpaceID:          int64(DefaultSpaceID),
		ValueDirectoryID: 0,
		Name:             name,
	})
	if err == sql.ErrNoRows {
		meta, createErr := s.CreateConfigWithVersion(name, DefaultSpaceID, updatedBy, value)
		if createErr != nil {
			panic(fmt.Sprintf("SetConfigByName create: %v", createErr))
		}
		return meta
	}
	if err != nil {
		panic(fmt.Sprintf("GetConfigInDirectoryByName: %v", err))
	}
	meta, _, appendErr := s.AppendConfigVersionWithDeploymentUpdates(int32(row.ID), value, updatedBy, false, nil)
	if appendErr != nil {
		panic(fmt.Sprintf("SetConfigByName append: %v", appendErr))
	}
	return meta
}

// RenameConfig renames the stable config identity. Versions are untouched.
func (s *PrimaryStorage) RenameConfig(configID int32, newName string) (*apigen.ConfigMeta, error) {
	if !ValidValueName(newName) {
		return nil, ErrValueNameInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	row, err := s.q.GetConfigRowByID(ctx, int64(configID))
	if err == sql.ErrNoRows {
		return nil, ErrValueNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetConfigRowByID: %v", err))
	}
	if row.Name != newName {
		if s.valueSiblingNameTakenLocked(ctx, s.q, row.SpaceID, row.ValueDirectoryID, newName, 0, row.ID) {
			return nil, ErrValueAlreadyExists
		}
		if err := s.q.RenameConfigRow(ctx, RenameConfigRowParams{Name: newName, ID: row.ID}); err != nil {
			panic(fmt.Sprintf("RenameConfigRow: %v", err))
		}
		slog.Info("renamed config", "id", configID, "name", row.Name, "newName", newName)
	}
	meta, ok := s.GetConfigMeta(configID)
	if !ok {
		return nil, ErrValueNotFound
	}
	return meta, nil
}

// DeleteConfig removes the config identity and all its versions. Returns the
// deleted meta (marked Deleted) for notification, or false if absent.
func (s *PrimaryStorage) DeleteConfig(configID int32) (*apigen.ConfigMeta, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, ok := s.GetConfigMeta(configID)
	if !ok {
		return nil, false
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin delete config tx: %v", err))
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)
	if err := q.DeleteConfigVersionsByConfigID(ctx, int64(configID)); err != nil {
		panic(fmt.Sprintf("DeleteConfigVersionsByConfigID: %v", err))
	}
	if err := q.DeleteConfigRow(ctx, int64(configID)); err != nil {
		panic(fmt.Sprintf("DeleteConfigRow: %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit delete config tx: %v", err))
	}
	meta.Deleted = true
	return meta, true
}

// ConfigVersionIDs returns every version row id of the config — the set a
// deployment env ref or setting could pin.
func (s *PrimaryStorage) ConfigVersionIDs(configID int32) []int32 {
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
	CreatedBy int32
}

// GetConfigVersionByID resolves a pinned config version row id.
func (s *PrimaryStorage) GetConfigVersionByID(id int32) (ConfigVersionRef, bool) {
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
		CreatedBy: int32(r.CreatedBy),
	}, true
}

func (s *PrimaryStorage) ResolveConfig(id int32) (string, bool) {
	ref, ok := s.GetConfigVersionByID(id)
	if !ok {
		return "", false
	}
	return ref.Value, true
}

func (s *PrimaryStorage) ResolveConfigs(ids []int32) (map[int32]string, error) {
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
