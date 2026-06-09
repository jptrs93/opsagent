package handler

import (
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

var UserConfigNameRequiredErr = apigen.NewApiErr("Config name is required", "user_config_name_required", http.StatusBadRequest)

func (h *Handler) PostV1UserConfigsList(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.UserConfigList, error) {
	return &apigen.UserConfigList{Items: h.Store.ListUserConfigs()}, nil
}

func (h *Handler) PostV1UserConfigsSet(ctx apigen.Context, req *apigen.UserConfigSetRequest) (*apigen.UserConfig, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, UserConfigNameRequiredErr
	}
	var updatedBy int32
	if ctx.User != nil {
		updatedBy = ctx.User.ID
	}
	return h.Store.SetUserConfig(name, strings.TrimSpace(req.Group), req.Value, updatedBy), nil
}

func (h *Handler) PostV1UserConfigsDelete(ctx apigen.Context, req *apigen.UserConfigDeleteRequest) error {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return UserConfigNameRequiredErr
	}
	h.Store.DeleteUserConfig(name)
	return nil
}
