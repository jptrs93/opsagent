package webuihandler

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var InvalidSpaceErr = apigen.NewApiErr("Invalid space", "invalid_space", http.StatusBadRequest)
var SpaceNotFoundErr = apigen.NewApiErr("Space not found", "space_not_found", http.StatusNotFound)
var SpaceInUseErr = apigen.NewApiErr("Space has active deployments", "space_in_use", http.StatusConflict)

func (h *Handler) PostV1SpacesCreate(ctx apigen.Context, req *apigen.SpaceSetRequest) (*apigen.Space, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, InvalidSpaceErr
	}
	// Space creation is cluster-level: checked in the opendeploy space because
	// the new space has no id to check against yet.
	if err := h.requireAccess(ctx, vCreate, eSpace, 0, 0); err != nil {
		return nil, err
	}
	space, err := h.Store.CreateSpace(name)
	if err != nil {
		return nil, err
	}
	return space, nil
}

func (h *Handler) PostV1SpacesUpdate(ctx apigen.Context, req *apigen.SpaceSetRequest) (*apigen.Space, error) {
	name := strings.TrimSpace(req.Name)
	if isSeededSpace(req.ID) || req.ID < 0 || name == "" {
		return nil, InvalidSpaceErr
	}
	if err := h.requireEntityAccess(ctx, vEdit, eSpace, int64(req.ID), int64(req.ID), SpaceNotFoundErr); err != nil {
		return nil, err
	}
	space, err := h.Store.UpdateSpace(req.ID, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, SpaceNotFoundErr
	}
	if err != nil {
		return nil, err
	}
	return space, nil
}

func (h *Handler) PostV1SpacesDelete(ctx apigen.Context, req *apigen.SpaceDeleteRequest) error {
	if isSeededSpace(req.ID) || req.ID < 0 {
		return InvalidSpaceErr
	}
	if err := h.requireEntityAccess(ctx, vDelete, eSpace, int64(req.ID), int64(req.ID), SpaceNotFoundErr); err != nil {
		return err
	}
	count, err := h.Store.CountDeploymentsForSpace(req.ID)
	if err != nil {
		return err
	}
	if count > 0 {
		return SpaceInUseErr
	}
	if err := h.Store.DeleteSpace(req.ID); err != nil {
		return err
	}
	return nil
}

func isSeededSpace(id int32) bool {
	return id == state.OpendeploySpaceID || id == state.DefaultSpaceID
}
