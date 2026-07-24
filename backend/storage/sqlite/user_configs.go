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

func userConfigRowToProto(r Config) *apigen.UserConfig {
	return &apigen.UserConfig{
		ID:        int32(r.ID),
		Name:      r.Name,
		SpaceID:   int32(r.SpaceID),
		Value:     r.Value,
		CreatedAt: time.UnixMilli(r.CreatedAt),
		UpdatedBy: int32(r.UpdatedBy),
		Version:   int32(r.Version),
	}
}

func (s *PrimaryStorage) ListUserConfigs() []*apigen.UserConfig {
	rows, err := s.q.ListUserConfigs(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListUserConfigs: %v", err))
	}
	out := make([]*apigen.UserConfig, 0, len(rows))
	for _, r := range rows {
		out = append(out, userConfigRowToProto(r))
	}
	return out
}

func (s *PrimaryStorage) ListAllUserConfigs() []*apigen.UserConfig {
	rows, err := s.q.ListAllUserConfigs(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListAllUserConfigs: %v", err))
	}
	out := make([]*apigen.UserConfig, 0, len(rows))
	for _, r := range rows {
		out = append(out, userConfigRowToProto(r))
	}
	return out
}

func (s *PrimaryStorage) SetUserConfig(name, value string, updatedBy int32, spaceID int32) *apigen.UserConfig {
	cfg, _, err := s.SetUserConfigWithDeploymentUpdates(name, value, updatedBy, spaceID, false, nil)
	if err != nil {
		panic(fmt.Sprintf("SetUserConfig: %v", err))
	}
	return cfg
}

func (s *PrimaryStorage) SetUserConfigWithDeploymentUpdates(name, value string, updatedBy int32, spaceID int32, updateDeployments bool, expected []storage.DeploymentConfigVersion) (*apigen.UserConfig, []int32, error) {
	spaceID = normalizedUserSpaceID(spaceID)
	var row Config
	updatedDeployments, err := s.setVersionedValueWithDeploymentUpdates(
		configValueReference,
		name,
		updateDeployments,
		expected,
		updatedBy,
		func(q *Queries) (int32, error) {
			version, err := q.GetNextUserConfigVersion(context.Background(), name)
			if err != nil {
				return 0, fmt.Errorf("get next config version: %w", err)
			}
			row, err = q.InsertUserConfig(context.Background(), InsertUserConfigParams{
				Name:      name,
				Version:   version,
				SpaceID:   int64(spaceID),
				Value:     value,
				CreatedAt: time.Now().UnixMilli(),
				UpdatedBy: int64(updatedBy),
			})
			if err != nil {
				return 0, fmt.Errorf("insert config: %w", err)
			}
			return int32(row.ID), nil
		},
		func(_ []int32) {
			cfg := userConfigRowToProto(row)
			s.userConfigSubs.Notify(apigen.UserConfigReference{ID: cfg.ID, Name: cfg.Name, SpaceID: cfg.SpaceID, Version: cfg.Version})
			s.userConfigValueSubs.Notify(*cfg)
		},
	)
	if err != nil {
		return nil, nil, err
	}
	cfg := userConfigRowToProto(row)
	return cfg, updatedDeployments, nil
}

func (s *PrimaryStorage) RenameUserConfig(name, newName string) (*apigen.UserConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == newName {
		r, err := s.q.GetUserConfig(context.Background(), name)
		if err == sql.ErrNoRows {
			return nil, false
		}
		if err != nil {
			panic(fmt.Sprintf("GetUserConfig unchanged rename: %v", err))
		}
		return userConfigRowToProto(r), true
	}
	if _, err := s.q.GetUserConfig(context.Background(), newName); err == nil {
		return nil, false
	} else if err != sql.ErrNoRows {
		panic(fmt.Sprintf("GetUserConfig new name before rename: %v", err))
	}
	if _, err := s.q.GetUserConfig(context.Background(), name); err == sql.ErrNoRows {
		return nil, false
	} else if err != nil {
		panic(fmt.Sprintf("GetUserConfig before rename: %v", err))
	}
	if err := s.q.RenameUserConfig(context.Background(), RenameUserConfigParams{Name: newName, Name_2: name}); err != nil {
		panic(fmt.Sprintf("RenameUserConfig: %v", err))
	}
	slog.Info("renamed user config", "name", name, "newName", newName)
	cfg, ok := s.GetLatestUserConfig(newName)
	if ok {
		for _, ref := range s.UserConfigReferencesByName(newName) {
			s.userConfigSubs.Notify(ref)
		}
		s.userConfigValueSubs.Notify(*cfg)
	}
	return cfg, ok
}

func (s *PrimaryStorage) GetLatestUserConfig(name string) (*apigen.UserConfig, bool) {
	r, err := s.q.GetUserConfig(context.Background(), name)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetUserConfig: %v", err))
	}
	return userConfigRowToProto(r), true
}

func (s *PrimaryStorage) GetUserConfigByID(id int32) (*apigen.UserConfig, bool) {
	r, err := s.q.GetUserConfigByID(context.Background(), int64(id))
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetUserConfigByID: %v", err))
	}
	return userConfigRowToProto(r), true
}

func (s *PrimaryStorage) UserConfigIDsByName(name string) []int32 {
	rows, err := s.q.ListUserConfigVersionsByName(context.Background(), name)
	if err != nil {
		panic(fmt.Sprintf("ListUserConfigVersionsByName: %v", err))
	}
	ids := make([]int32, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, int32(row.ID))
	}
	return ids
}

func (s *PrimaryStorage) UserConfigReferencesByName(name string) []apigen.UserConfigReference {
	rows, err := s.q.ListUserConfigVersionsByName(context.Background(), name)
	if err != nil {
		panic(fmt.Sprintf("ListUserConfigVersionsByName: %v", err))
	}
	out := make([]apigen.UserConfigReference, 0, len(rows))
	for _, row := range rows {
		out = append(out, apigen.UserConfigReference{ID: int32(row.ID), Name: row.Name, SpaceID: int32(row.SpaceID), Version: int32(row.Version)})
	}
	return out
}

func (s *PrimaryStorage) DeleteUserConfig(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var updates []apigen.UserConfigReference
	var valueUpdates []*apigen.UserConfig
	if rows, err := s.q.ListUserConfigVersionsByName(context.Background(), name); err == nil {
		updates = s.UserConfigReferencesByName(name)
		valueUpdates = make([]*apigen.UserConfig, 0, len(rows))
		for _, row := range rows {
			cfg := userConfigRowToProto(row)
			cfg.Deleted = true
			valueUpdates = append(valueUpdates, cfg)
		}
	} else if err != sql.ErrNoRows {
		panic(fmt.Sprintf("ListUserConfigVersionsByName before delete: %v", err))
	}
	if err := s.q.DeleteUserConfig(context.Background(), name); err != nil {
		panic(fmt.Sprintf("DeleteUserConfig: %v", err))
	}
	for _, update := range updates {
		update.Deleted = true
		s.userConfigSubs.Notify(update)
	}
	for _, valueUpdate := range valueUpdates {
		s.userConfigValueSubs.Notify(*valueUpdate)
	}
}

func (s *PrimaryStorage) ResolveConfig(id int32) (string, bool) {
	r, err := s.q.GetUserConfigByID(context.Background(), int64(id))
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		panic(fmt.Sprintf("GetUserConfig: %v", err))
	}
	return r.Value, true
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
		r, err := s.q.GetUserConfigByID(context.Background(), int64(id))
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("config not found: id %d", id)
		}
		if err != nil {
			return nil, fmt.Errorf("GetUserConfigByID %d: %w", id, err)
		}
		out[id] = r.Value
	}
	return out, nil
}
