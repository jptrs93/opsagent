package webuihandler

import (
	"errors"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

var ValueDirectoryNameRequiredErr = apigen.NewApiErr("Folder name is required", "value_directory_name_required", http.StatusBadRequest)
var ValueDirectoryIDRequiredErr = apigen.NewApiErr("Folder id is required", "value_directory_id_required", http.StatusBadRequest)
var ValueDirectoryNameInvalidErr = apigen.NewApiErr("Folder name is not a valid file name", "value_directory_name_invalid", http.StatusBadRequest)
var ValueDirectoryNotFoundErr = apigen.NewApiErr("Folder not found", "value_directory_not_found", http.StatusNotFound)
var ValueDirectoryNotEmptyErr = apigen.NewApiErr("Folder is not empty", "value_directory_not_empty", http.StatusBadRequest)
var ValueDirectoryCycleErr = apigen.NewApiErr("Folder cannot be moved inside itself", "value_directory_cycle", http.StatusBadRequest)
var ValueDirectoryNameTakenErr = apigen.NewApiErr("A secret, config, or folder with this name already exists here", "value_directory_name_exists", http.StatusBadRequest)
var ValueSpaceMoveUnsupportedErr = apigen.NewApiErr("Moving between spaces is not supported", "value_space_move_unsupported", http.StatusBadRequest)

func mapValueDirectoryErr(err error) error {
	switch {
	case errors.Is(err, values.ErrDirectoryNotFound):
		return ValueDirectoryNotFoundErr
	case errors.Is(err, values.ErrDirectoryNotEmpty):
		return ValueDirectoryNotEmptyErr
	case errors.Is(err, values.ErrDirectoryCycle):
		return ValueDirectoryCycleErr
	case errors.Is(err, values.ErrSpaceMoveUnsupported):
		return ValueSpaceMoveUnsupportedErr
	case errors.Is(err, values.ErrAlreadyExists):
		return ValueDirectoryNameTakenErr
	case errors.Is(err, values.ErrNameInvalid):
		return ValueDirectoryNameInvalidErr
	}
	return err
}

func (h *Handler) PostV1ValueDirectoriesList(ctx apigen.Context) (*apigen.ValueDirectoryList, error) {
	return &apigen.ValueDirectoryList{Items: h.filterValueDirectories(ctx, values.ListDirectories(h.Store.Queries()))}, nil
}

func (h *Handler) PostV1ValueDirectoriesCreate(ctx apigen.Context, req *apigen.ValueDirectoryCreateRequest) (*apigen.ValueDirectory, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, ValueDirectoryNameRequiredErr
	}
	// Folders hold secrets and configs, so either create right suffices.
	if err := h.requireAnyAccess(ctx, vCreate, eValues, valueSpace(req.SpaceID), 0); err != nil {
		return nil, err
	}
	row, err := values.CreateDirectory(h.Store, req.SpaceID, req.ParentID, name, requestUserID(ctx))
	if err != nil {
		return nil, mapValueDirectoryErr(err)
	}

	dir, ok := values.DirectoryMeta(h.Store.Queries(), int32(row.ID))
	if !ok {
		return nil, ValueDirectoryNotFoundErr
	}
	return dir, nil
}

func (h *Handler) PostV1ValueDirectoriesMove(ctx apigen.Context, req *apigen.ValueDirectoryMoveRequest) (*apigen.ValueDirectory, error) {
	if req.DirectoryID == 0 {
		return nil, ValueDirectoryIDRequiredErr
	}
	existing, ok := values.DirectoryMeta(h.Store.Queries(), req.DirectoryID)
	if !ok {
		return nil, ValueDirectoryNotFoundErr
	}
	if err := h.requireAnyEntityAccess(ctx, vUpdate, eValues, int64(existing.SpaceID), 0, ValueDirectoryNotFoundErr); err != nil {
		return nil, err
	}
	if req.SpaceID != 0 && req.SpaceID != existing.SpaceID {
		if err := h.requireAnyAccess(ctx, vCreate, eValues, valueSpace(req.SpaceID), 0); err != nil {
			return nil, err
		}
	}
	// The space gate runs first: a rejected cross-space move must not leave the
	// directory reparented into its own space's root as a side effect.
	if req.SpaceID != 0 {
		if err := values.MoveDirectorySpace(h.Store, req.DirectoryID, req.SpaceID); err != nil {
			return nil, mapValueDirectoryErr(err)
		}
	}
	row, err := values.MoveDirectory(h.Store, req.DirectoryID, req.NewParentID)
	if err != nil {
		return nil, mapValueDirectoryErr(err)
	}

	dir, ok := values.DirectoryMeta(h.Store.Queries(), int32(row.ID))
	if !ok {
		return nil, ValueDirectoryNotFoundErr
	}
	return dir, nil
}

func (h *Handler) PostV1ValueDirectoriesRename(ctx apigen.Context, req *apigen.ValueDirectoryRenameRequest) (*apigen.ValueDirectory, error) {
	if req.DirectoryID == 0 {
		return nil, ValueDirectoryIDRequiredErr
	}
	if strings.TrimSpace(req.NewName) == "" {
		return nil, ValueDirectoryNameRequiredErr
	}
	if existing, ok := values.DirectoryMeta(h.Store.Queries(), req.DirectoryID); !ok {
		return nil, ValueDirectoryNotFoundErr
	} else if err := h.requireAnyEntityAccess(ctx, vUpdate, eValues, int64(existing.SpaceID), 0, ValueDirectoryNotFoundErr); err != nil {
		return nil, err
	}
	row, err := values.RenameDirectory(h.Store, req.DirectoryID, strings.TrimSpace(req.NewName))
	if err != nil {
		return nil, mapValueDirectoryErr(err)
	}

	dir, ok := values.DirectoryMeta(h.Store.Queries(), int32(row.ID))
	if !ok {
		return nil, ValueDirectoryNotFoundErr
	}
	return dir, nil
}

func (h *Handler) PostV1ValueDirectoriesDelete(ctx apigen.Context, req *apigen.ValueDirectoryDeleteRequest) error {
	if req.DirectoryID == 0 {
		return ValueDirectoryIDRequiredErr
	}
	if existing, ok := values.DirectoryMeta(h.Store.Queries(), req.DirectoryID); !ok {
		return ValueDirectoryNotFoundErr
	} else if err := h.requireAnyEntityAccess(ctx, vDelete, eValues, int64(existing.SpaceID), 0, ValueDirectoryNotFoundErr); err != nil {
		return err
	}
	if err := values.DeleteDirectory(h.Store, req.DirectoryID); err != nil {
		return mapValueDirectoryErr(err)
	}

	return nil
}
