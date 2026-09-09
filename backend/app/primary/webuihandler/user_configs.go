package webuihandler

import (
	"errors"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
)

var UserConfigNameRequiredErr = apigen.NewApiErr("Config name is required", "user_config_name_required", http.StatusBadRequest)
var UserConfigIDRequiredErr = apigen.NewApiErr("Config id is required", "user_config_id_required", http.StatusBadRequest)
var UserConfigNameInvalidErr = apigen.NewApiErr("Config name is not a valid file name", "user_config_name_invalid", http.StatusBadRequest)
var UserConfigAlreadyExistsErr = apigen.NewApiErr("Config name already exists", "user_config_name_exists", http.StatusBadRequest)
var UserConfigNotFoundErr = apigen.NewApiErr("Config not found", "user_config_not_found", http.StatusNotFound)

func mapConfigStoreErr(err error) error {
	switch {
	case errors.Is(err, values.ErrNotFound):
		return UserConfigNotFoundErr
	case errors.Is(err, values.ErrAlreadyExists):
		return UserConfigAlreadyExistsErr
	case errors.Is(err, values.ErrNameInvalid):
		return UserConfigNameInvalidErr
	case errors.Is(err, values.ErrDirectoryNotFound):
		return ValueDirectoryNotFoundErr
	case errors.Is(err, values.ErrSpaceMoveUnsupported):
		return ValueSpaceMoveUnsupportedErr
	}
	return err
}

func (h *Handler) PostV1ConfigsList(ctx apigen.Context) (*apigen.ConfigEventList, error) {
	return &apigen.ConfigEventList{Items: h.filterConfigs(ctx, values.ListConfigs(h.Store.Queries()))}, nil
}

func (h *Handler) PostV1ConfigsCreate(ctx apigen.Context, req *apigen.ConfigCreateRequest) (*apigen.ConfigEvent, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, UserConfigNameRequiredErr
	}
	if err := h.requireAccess(ctx, vCreate, eConfig, valueSpace(req.SpaceID), 0); err != nil {
		return nil, err
	}
	meta, err := values.CreateConfig(h.Store, name, req.SpaceID, req.ValueDirectoryID, requestUserID(ctx), req.Value)
	if err != nil {
		return nil, mapConfigStoreErr(err)
	}

	return meta, nil
}

func (h *Handler) PostV1ConfigsSet(ctx apigen.Context, req *apigen.ConfigSetRequest) (*apigen.ConfigEvent, error) {
	if req.ConfigID == 0 {
		return nil, UserConfigIDRequiredErr
	}
	if existing, ok := values.GetConfig(h.Store.Queries(), req.ConfigID); !ok {
		return nil, UserConfigNotFoundErr
	} else if err := h.requireEntityAccess(ctx, vUpdate, eConfig, int64(existing.SpaceID()), int64(existing.ConfigID), UserConfigNotFoundErr); err != nil {
		return nil, err
	}
	expected, err := requestedDeploymentVersions(req.UpdateReferencingDeployments, req.ReferencingDeployments)
	if err != nil {
		return nil, err
	}
	meta, _, err := values.AppendConfigVersion(h.Store,
		req.ConfigID,
		req.Value,
		requestUserID(ctx),
		req.UpdateReferencingDeployments,
		expected,
	)
	if err != nil {
		if errors.Is(err, values.ErrNotFound) {
			return nil, UserConfigNotFoundErr
		}
		return nil, versionedValueSetError(err)
	}

	return meta, nil
}

func (h *Handler) PostV1ConfigsRename(ctx apigen.Context, req *apigen.ConfigRenameRequest) (*apigen.ConfigEvent, error) {
	if req.ConfigID == 0 {
		return nil, UserConfigIDRequiredErr
	}
	if strings.TrimSpace(req.NewName) == "" {
		return nil, UserConfigNameRequiredErr
	}
	if existing, ok := values.GetConfig(h.Store.Queries(), req.ConfigID); !ok {
		return nil, UserConfigNotFoundErr
	} else if err := h.requireEntityAccess(ctx, vUpdate, eConfig, int64(existing.SpaceID()), int64(existing.ConfigID), UserConfigNotFoundErr); err != nil {
		return nil, err
	}
	meta, err := values.RenameConfig(h.Store, req.ConfigID, strings.TrimSpace(req.NewName))
	if err != nil {
		return nil, mapConfigStoreErr(err)
	}

	return meta, nil
}

// PostV1ConfigsMove relocates a config within its space's folder tree, or —
// when space_id names another space — moves it there. Version rows and every
// pinned reference are untouched either way. A cross-space move is allowed
// only while nothing outside the destination space references the config:
// deployments must be able to keep their pins within their own space, and a
// settings reference pins the value to the global space.
func (h *Handler) PostV1ConfigsMove(ctx apigen.Context, req *apigen.ConfigMoveRequest) (*apigen.ConfigEvent, error) {
	if req.ConfigID == 0 {
		return nil, UserConfigIDRequiredErr
	}
	existing, ok := values.GetConfig(h.Store.Queries(), req.ConfigID)
	if !ok {
		return nil, UserConfigNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vUpdate, eConfig, int64(existing.SpaceID()), int64(existing.ConfigID), UserConfigNotFoundErr); err != nil {
		return nil, err
	}
	destSpace := nodes.NormalizedUserSpaceID(req.SpaceID)
	spaceChanging := req.SpaceID != 0 && destSpace != existing.SpaceID()
	// Moving into another space also needs the right to create a config there.
	if spaceChanging {
		if err := h.requireAccess(ctx, vCreate, eConfig, valueSpace(req.SpaceID), 0); err != nil {
			return nil, err
		}
	}
	if spaceChanging {
		validate := func(q *pq.Queries) error {
			if destSpace == nodes.DefaultSpaceID {
				return nil
			}
			ids := deployments.Int32Set(values.ConfigVersionIDs(h.Store.Queries(), req.ConfigID))
			if h.settingsUseConfigID(ids) {
				return deployments.MoveReferencesOutsideSpaceErr
			}
			live, err := nodes.ReadLiveState(ctx, q)
			if err != nil {
				return err
			}
			if deployments.ReferencesOutsideSpace(live, ids, runtimeinputs.ConfigRefs, destSpace) {
				return deployments.MoveReferencesOutsideSpaceErr
			}
			return nil
		}
		if err := values.MoveConfigSpace(h.Store, req.ConfigID, req.SpaceID, req.ValueDirectoryID, ctx.AttributionUserID(), validate); err != nil {
			return nil, mapConfigStoreErr(err)
		}
		// Tombstone for clients that saw the old space but cannot see the new
		// one — updates a user cannot view are dropped, and nothing else says
		// "gone". The update below re-adds the row where the destination is
		// visible.

	} else if err := values.MoveConfigDirectory(h.Store, req.ConfigID, req.ValueDirectoryID); err != nil {
		return nil, mapConfigStoreErr(err)
	}
	meta, ok := values.GetConfig(h.Store.Queries(), req.ConfigID)
	if !ok {
		return nil, UserConfigNotFoundErr
	}

	return meta, nil
}

func (h *Handler) PostV1ConfigsDelete(ctx apigen.Context, req *apigen.ConfigDeleteRequest) error {
	if req.ConfigID == 0 {
		return UserConfigIDRequiredErr
	}
	if existing, ok := values.GetConfig(h.Store.Queries(), req.ConfigID); !ok {
		return UserConfigNotFoundErr
	} else if err := h.requireEntityAccess(ctx, vDelete, eConfig, int64(existing.SpaceID()), int64(existing.ConfigID), UserConfigNotFoundErr); err != nil {
		return err
	}
	validate := func(q *pq.Queries) error {
		ids := deployments.Int32Set(values.ConfigVersionIDs(h.Store.Queries(), req.ConfigID))
		if len(ids) == 0 {
			return UserConfigNotFoundErr
		}
		live, err := nodes.ReadLiveState(ctx, q)
		if err != nil {
			return err
		}
		details := append(h.settingsConfigRefDetails(ids), deployments.RefDetails(ctx, q, live, ids, runtimeinputs.ConfigRefs)...)
		if len(details) > 0 {
			return deployments.ReferenceInUseDetailErr("Config", details)
		}
		return nil
	}
	if _, err := values.DeleteConfig(h.Store, req.ConfigID, validate); err != nil {
		return mapConfigStoreErr(err)
	}

	return nil
}
