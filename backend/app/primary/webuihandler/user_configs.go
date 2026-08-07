package webuihandler

import (
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

var UserConfigNameRequiredErr = apigen.NewApiErr("Config name is required", "user_config_name_required", http.StatusBadRequest)
var UserConfigAlreadyExistsErr = apigen.NewApiErr("Config name already exists", "user_config_name_exists", http.StatusBadRequest)
var UserConfigNotFoundErr = apigen.NewApiErr("Config not found", "user_config_not_found", http.StatusNotFound)

func (h *Handler) PostV1ConfigsList(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.ConfigList, error) {
	return &apigen.ConfigList{Items: h.Store.ListUserConfigs()}, nil
}

func (h *Handler) PostV1ConfigsSet(ctx apigen.Context, req *apigen.ConfigSetRequest) (*apigen.Config, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, UserConfigNameRequiredErr
	}
	unlockReferences := h.ConfigService.LockReferences()
	defer unlockReferences()
	var updatedBy int32
	if ctx.User != nil {
		updatedBy = ctx.User.ID
	}
	expected, err := requestedDeploymentVersions(req.UpdateReferencingDeployments, req.ReferencingDeployments)
	if err != nil {
		return nil, err
	}
	cfg, _, err := h.Store.SetUserConfigWithDeploymentUpdates(
		name,
		req.Value,
		updatedBy,
		req.SpaceID,
		req.UpdateReferencingDeployments,
		expected,
	)
	if err != nil {
		return nil, versionedValueSetError(err)
	}
	return cfg, nil
}

func (h *Handler) PostV1ConfigsRename(ctx apigen.Context, req *apigen.ConfigRenameRequest) (*apigen.Config, error) {
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

func (h *Handler) PostV1ConfigsDelete(ctx apigen.Context, req *apigen.ConfigDeleteRequest) error {
	unlockReferences := h.ConfigService.LockReferences()
	defer unlockReferences()
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
