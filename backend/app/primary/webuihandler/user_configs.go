package webuihandler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
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

func (h *Handler) PostV1ConfigsList(ctx apigen.Context) (*apigen.ConfigList, error) {
	return &apigen.ConfigList{Items: h.filterConfigs(ctx, h.Store.ListConfigs())}, nil
}

func (h *Handler) PostV1ConfigsCreate(ctx apigen.Context, req *apigen.ConfigCreateRequest) (*apigen.Config, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, UserConfigNameRequiredErr
	}
	if err := h.requireAccess(ctx, vCreate, eConfig, valueSpace(req.SpaceID), 0); err != nil {
		return nil, err
	}
	meta, err := h.Store.CreateConfigWithVersion(name, req.SpaceID, req.ValueDirectoryID, requestUserID(ctx), req.Value)
	if err != nil {
		return nil, mapConfigStoreErr(err)
	}
	h.Store.NotifyConfigUpdate(meta)
	return meta, nil
}

func (h *Handler) PostV1ConfigsSet(ctx apigen.Context, req *apigen.ConfigSetRequest) (*apigen.Config, error) {
	if req.ConfigID == 0 {
		return nil, UserConfigIDRequiredErr
	}
	if existing, ok := h.Store.GetConfig(req.ConfigID); !ok {
		return nil, UserConfigNotFoundErr
	} else if err := h.requireEntityAccess(ctx, vUpdate, eConfig, int64(existing.SpaceID()), int64(existing.ID), UserConfigNotFoundErr); err != nil {
		return nil, err
	}
	expected, err := requestedDeploymentVersions(req.UpdateReferencingDeployments, req.ReferencingDeployments)
	if err != nil {
		return nil, err
	}
	defer h.Store.GlobalLock()()
	meta, _, err := h.Store.AppendConfigVersionWithDeploymentUpdatesLocked(
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
	h.Store.NotifyConfigUpdate(meta)
	return meta, nil
}

func (h *Handler) PostV1ConfigsRename(ctx apigen.Context, req *apigen.ConfigRenameRequest) (*apigen.Config, error) {
	if req.ConfigID == 0 {
		return nil, UserConfigIDRequiredErr
	}
	if strings.TrimSpace(req.NewName) == "" {
		return nil, UserConfigNameRequiredErr
	}
	if existing, ok := h.Store.GetConfig(req.ConfigID); !ok {
		return nil, UserConfigNotFoundErr
	} else if err := h.requireEntityAccess(ctx, vUpdate, eConfig, int64(existing.SpaceID()), int64(existing.ID), UserConfigNotFoundErr); err != nil {
		return nil, err
	}
	meta, err := h.Store.RenameConfig(req.ConfigID, strings.TrimSpace(req.NewName))
	if err != nil {
		return nil, mapConfigStoreErr(err)
	}
	h.Store.NotifyConfigUpdate(meta)
	return meta, nil
}

// PostV1ConfigsMove relocates a config within its space's folder tree, or —
// when space_id names another space — moves it there. Version rows and every
// pinned reference are untouched either way. A cross-space move is allowed
// only while nothing outside the destination space references the config:
// deployments must be able to keep their pins within their own space, and a
// settings reference pins the value to the global space.
func (h *Handler) PostV1ConfigsMove(ctx apigen.Context, req *apigen.ConfigMoveRequest) (*apigen.Config, error) {
	if req.ConfigID == 0 {
		return nil, UserConfigIDRequiredErr
	}
	existing, ok := h.Store.GetConfig(req.ConfigID)
	if !ok {
		return nil, UserConfigNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vUpdate, eConfig, int64(existing.SpaceID()), int64(existing.ID), UserConfigNotFoundErr); err != nil {
		return nil, err
	}
	destSpace := state.NormalizedUserSpaceID(req.SpaceID)
	spaceChanging := req.SpaceID != 0 && destSpace != existing.SpaceID()
	// Moving into another space also needs the right to create a config there.
	if spaceChanging {
		if err := h.requireAccess(ctx, vCreate, eConfig, valueSpace(req.SpaceID), 0); err != nil {
			return nil, err
		}
	}
	if spaceChanging {
		// Deployment writes hold the same lock, so no new reference can appear
		// between the locality check and the move.
		defer h.Store.GlobalLock()()
		ids := int32Set(h.Store.ConfigVersionIDs(req.ConfigID))
		if h.settingsUseConfigID(ids) && destSpace != state.DefaultSpaceID {
			return nil, MoveReferencesOutsideSpaceErr
		}
		if destSpace != state.DefaultSpaceID && referencesOutsideSpace(h.Store.LiveState(), ids, runtimeinputs.ConfigRefs, destSpace) {
			return nil, MoveReferencesOutsideSpaceErr
		}
		if err := h.Store.MoveConfigSpaceLocked(req.ConfigID, req.SpaceID, req.ValueDirectoryID, ctx.AttributionUserID()); err != nil {
			return nil, mapConfigStoreErr(err)
		}
		// Tombstone for clients that saw the old space but cannot see the new
		// one — updates a user cannot view are dropped, and nothing else says
		// "gone". The update below re-adds the row where the destination is
		// visible.
		h.Store.NotifyConfigDeleted(existing)
	} else if _, err := h.Store.MoveConfigDirectory(req.ConfigID, req.ValueDirectoryID); err != nil {
		return nil, mapConfigStoreErr(err)
	}
	meta, ok := h.Store.GetConfig(req.ConfigID)
	if !ok {
		return nil, UserConfigNotFoundErr
	}
	h.Store.NotifyConfigUpdate(meta)
	return meta, nil
}

func (h *Handler) PostV1ConfigsDelete(ctx apigen.Context, req *apigen.ConfigDeleteRequest) error {
	if req.ConfigID == 0 {
		return UserConfigIDRequiredErr
	}
	if existing, ok := h.Store.GetConfig(req.ConfigID); !ok {
		return UserConfigNotFoundErr
	} else if err := h.requireEntityAccess(ctx, vDelete, eConfig, int64(existing.SpaceID()), int64(existing.ID), UserConfigNotFoundErr); err != nil {
		return err
	}
	defer h.Store.GlobalLock()()
	ids := int32Set(h.Store.ConfigVersionIDs(req.ConfigID))
	if len(ids) == 0 {
		return UserConfigNotFoundErr
	}
	details := append(h.settingsConfigRefDetails(ids), h.deploymentRefDetails(h.Store.LiveState(), ids, runtimeinputs.ConfigRefs)...)
	if len(details) > 0 {
		return referenceInUseDetailErr("Config", details)
	}
	meta, ok := h.Store.DeleteConfigLocked(req.ConfigID)
	if !ok {
		return UserConfigNotFoundErr
	}
	h.Store.NotifyConfigUpdate(meta)
	return nil
}
