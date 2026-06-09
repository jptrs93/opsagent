package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func userConfigRowToProto(r UserConfig) *apigen.UserConfig {
	return &apigen.UserConfig{
		Name:      r.Name,
		Group:     r.ConfigGroup,
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

func (s *PrimaryStorage) SetUserConfig(name, group, value string, updatedBy int32) *apigen.UserConfig {
	now := time.Now().UnixMilli()
	if err := s.q.UpsertUserConfig(context.Background(), UpsertUserConfigParams{
		Name:        name,
		ConfigGroup: group,
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
	return userConfigRowToProto(r)
}

func (s *PrimaryStorage) DeleteUserConfig(name string) {
	if err := s.q.DeleteUserConfig(context.Background(), name); err != nil {
		panic(fmt.Sprintf("DeleteUserConfig: %v", err))
	}
}

func (s *PrimaryStorage) ResolveConfig(name string) (string, bool) {
	r, err := s.q.GetUserConfig(context.Background(), name)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		panic(fmt.Sprintf("GetUserConfig: %v", err))
	}
	return r.Value, true
}
