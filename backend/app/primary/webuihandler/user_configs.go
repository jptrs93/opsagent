package webuihandler

import (
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

var UserConfigNameRequiredErr = apigen.NewApiErr("Config name is required", "user_config_name_required", http.StatusBadRequest)
var UserConfigAlreadyExistsErr = apigen.NewApiErr("Config name already exists", "user_config_name_exists", http.StatusBadRequest)
var UserConfigNotFoundErr = apigen.NewApiErr("Config not found", "user_config_not_found", http.StatusNotFound)

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
	return h.Store.SetUserConfig(name, req.Value, updatedBy, req.SpaceID), nil
}

func (h *Handler) PostV1UserConfigsRename(ctx apigen.Context, req *apigen.UserConfigRenameRequest) (*apigen.UserConfig, error) {
	name := strings.TrimSpace(req.Name)
	newName := strings.TrimSpace(req.NewName)
	if name == "" || newName == "" {
		return nil, UserConfigNameRequiredErr
	}
	cfg, ok := h.Store.RenameUserConfig(name, newName)
	if !ok {
		if existing, exists := h.Store.GetLatestUserConfig(newName); exists && existing.Name == newName {
			return nil, UserConfigAlreadyExistsErr
		}
		return nil, UserConfigNotFoundErr
	}
	return cfg, nil
}

func (h *Handler) PostV1UserConfigsDelete(ctx apigen.Context, req *apigen.UserConfigDeleteRequest) error {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return UserConfigNameRequiredErr
	}
	ids := int32Set(h.Store.UserConfigIDsByName(name))
	if h.settingsUseConfigID(ids) || h.deploymentUsesConfigID(ids) {
		return ReferenceInUseErr
	}
	h.Store.DeleteUserConfig(name)
	return nil
}
