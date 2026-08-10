package webuihandler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var UserConfigNameRequiredErr = apigen.NewApiErr("Config name is required", "user_config_name_required", http.StatusBadRequest)
var UserConfigIDRequiredErr = apigen.NewApiErr("Config id is required", "user_config_id_required", http.StatusBadRequest)
var UserConfigNameInvalidErr = apigen.NewApiErr("Config name is not a valid file name", "user_config_name_invalid", http.StatusBadRequest)
var UserConfigAlreadyExistsErr = apigen.NewApiErr("Config name already exists", "user_config_name_exists", http.StatusBadRequest)
var UserConfigNotFoundErr = apigen.NewApiErr("Config not found", "user_config_not_found", http.StatusNotFound)

func mapConfigStoreErr(err error) error {
	switch {
	case errors.Is(err, state.ErrValueNotFound):
		return UserConfigNotFoundErr
	case errors.Is(err, state.ErrValueAlreadyExists):
		return UserConfigAlreadyExistsErr
	case errors.Is(err, state.ErrValueNameInvalid):
		return UserConfigNameInvalidErr
	case errors.Is(err, state.ErrValueDirectoryNotFound):
		return ValueDirectoryNotFoundErr
	case errors.Is(err, state.ErrSpaceMoveUnsupported):
		return ValueSpaceMoveUnsupportedErr
	}
	return err
}

func (h *Handler) PostV1ConfigsList(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.ConfigList, error) {
	return &apigen.ConfigList{Items: h.Store.ListConfigMetas()}, nil
}

func (h *Handler) PostV1ConfigsCreate(ctx apigen.Context, req *apigen.ConfigCreateRequest) (*apigen.ConfigMeta, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, UserConfigNameRequiredErr
	}
	meta, err := h.Store.CreateConfigWithVersion(name, req.SpaceID, req.ValueDirectoryID, requestUserID(ctx), req.Value)
	if err != nil {
		return nil, mapConfigStoreErr(err)
	}
	h.Store.NotifyConfigMetaUpdate(*meta)
	return meta, nil
}

func (h *Handler) PostV1ConfigsSet(ctx apigen.Context, req *apigen.ConfigSetRequest) (*apigen.ConfigMeta, error) {
	if req.ConfigID == 0 {
		return nil, UserConfigIDRequiredErr
	}
	unlockReferences := h.ConfigService.LockReferences()
	defer unlockReferences()
	expected, err := requestedDeploymentVersions(req.UpdateReferencingDeployments, req.ReferencingDeployments)
	if err != nil {
		return nil, err
	}
	meta, _, err := h.Store.AppendConfigVersionWithDeploymentUpdates(
		req.ConfigID,
		req.Value,
		requestUserID(ctx),
		req.UpdateReferencingDeployments,
		expected,
	)
	if err != nil {
		if errors.Is(err, state.ErrValueNotFound) {
			return nil, UserConfigNotFoundErr
		}
		return nil, versionedValueSetError(err)
	}
	h.Store.NotifyConfigMetaUpdate(*meta)
	return meta, nil
}

func (h *Handler) PostV1ConfigsRename(ctx apigen.Context, req *apigen.ConfigRenameRequest) (*apigen.ConfigMeta, error) {
	if req.ConfigID == 0 {
		return nil, UserConfigIDRequiredErr
	}
	if strings.TrimSpace(req.NewName) == "" {
		return nil, UserConfigNameRequiredErr
	}
	meta, err := h.Store.RenameConfig(req.ConfigID, strings.TrimSpace(req.NewName))
	if err != nil {
		return nil, mapConfigStoreErr(err)
	}
	h.Store.NotifyConfigMetaUpdate(*meta)
	return meta, nil
}

// PostV1ConfigsMove relocates a config within its space's folder tree. Version
// rows and every pinned reference are untouched — only the identity's
// directory changes.
func (h *Handler) PostV1ConfigsMove(ctx apigen.Context, req *apigen.ConfigMoveRequest) (*apigen.ConfigMeta, error) {
	if req.ConfigID == 0 {
		return nil, UserConfigIDRequiredErr
	}
	if _, err := h.Store.MoveConfigDirectory(req.ConfigID, req.ValueDirectoryID); err != nil {
		return nil, mapConfigStoreErr(err)
	}
	meta, ok := h.Store.GetConfigMeta(req.ConfigID)
	if !ok {
		return nil, UserConfigNotFoundErr
	}
	h.Store.NotifyConfigMetaUpdate(*meta)
	return meta, nil
}

func (h *Handler) PostV1ConfigsDelete(ctx apigen.Context, req *apigen.ConfigDeleteRequest) error {
	unlockReferences := h.ConfigService.LockReferences()
	defer unlockReferences()
	if req.ConfigID == 0 {
		return UserConfigIDRequiredErr
	}
	ids := int32Set(h.Store.ConfigVersionIDs(req.ConfigID))
	if len(ids) == 0 {
		return UserConfigNotFoundErr
	}
	if h.settingsUseConfigID(ids) || h.deploymentUsesConfigID(ids) {
		return ReferenceInUseErr
	}
	meta, ok := h.Store.DeleteConfig(req.ConfigID)
	if !ok {
		return UserConfigNotFoundErr
	}
	h.Store.NotifyConfigMetaUpdate(*meta)
	return nil
}
