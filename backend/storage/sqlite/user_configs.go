package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const userConfigDefaultGroup = "default"

func userConfigRowToProto(r UserConfig) *apigen.UserConfig {
	return &apigen.UserConfig{
		ID:        int32(r.ID),
		Name:      r.Name,
		SpaceID:   int32(r.SpaceID),
		Group:     userConfigDefaultGroup,
		Value:     r.Value,
		CreatedAt: time.UnixMilli(r.CreatedAt),
		UpdatedAt: time.UnixMilli(r.UpdatedAt),
		UpdatedBy: int32(r.UpdatedBy),
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

func (s *PrimaryStorage) SetUserConfig(name, group, value string, updatedBy int32, spaceID int32) *apigen.UserConfig {
	now := time.Now().UnixMilli()
	spaceID = normalizedUserSpaceID(spaceID)
	id := s.nextUserConfigID(name)
	if err := s.q.UpsertUserConfig(context.Background(), UpsertUserConfigParams{
		ID:          int64(id),
		Name:        name,
		SpaceID:     int64(spaceID),
		ConfigGroup: userConfigDefaultGroup,
		Value:       value,
		CreatedAt:   now,
		UpdatedAt:   now,
		UpdatedBy:   int64(updatedBy),
	}); err != nil {
		panic(fmt.Sprintf("UpsertUserConfig: %v", err))
	}
	r, err := s.q.GetUserConfig(context.Background(), name)
	if err != nil {
		panic(fmt.Sprintf("GetUserConfig after upsert: %v", err))
	}
	cfg := userConfigRowToProto(r)
	s.userConfigSubs.Notify(apigen.UserConfigReference{ID: cfg.ID, Name: cfg.Name, SpaceID: cfg.SpaceID})
	s.userConfigValueSubs.Notify(*cfg)
	return cfg
}

func (s *PrimaryStorage) nextUserConfigID(name string) int32 {
	if existing, err := s.q.GetUserConfig(context.Background(), name); err == nil {
		return int32(existing.ID)
	} else if err != sql.ErrNoRows {
		panic(fmt.Sprintf("GetUserConfig: %v", err))
	}
	id, err := s.q.GetNextUserConfigID(context.Background())
	if err != nil {
		panic(fmt.Sprintf("GetNextUserConfigID: %v", err))
	}
	return int32(id)
}

func (s *PrimaryStorage) DeleteUserConfig(name string) {
	var update *apigen.UserConfigReference
	var valueUpdate *apigen.UserConfig
	if r, err := s.q.GetUserConfig(context.Background(), name); err == nil {
		update = &apigen.UserConfigReference{ID: int32(r.ID), Name: r.Name, SpaceID: int32(r.SpaceID), Deleted: true}
		valueUpdate = userConfigRowToProto(r)
		valueUpdate.Deleted = true
	} else if err != sql.ErrNoRows {
		panic(fmt.Sprintf("GetUserConfig before delete: %v", err))
	}
	if err := s.q.DeleteUserConfig(context.Background(), name); err != nil {
		panic(fmt.Sprintf("DeleteUserConfig: %v", err))
	}
	if update != nil {
		s.userConfigSubs.Notify(*update)
	}
	if valueUpdate != nil {
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

func (s *PrimaryStorage) ResolveConfigByName(name string) (string, bool) {
	r, err := s.q.GetUserConfig(context.Background(), name)
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
