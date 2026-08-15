package webuihandler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var AssetDirectoryKeyRequiredErr = apigen.NewApiErr("Folder name is required", "asset_directory_key_required", http.StatusBadRequest)
var AssetDirectoryIDRequiredErr = apigen.NewApiErr("Folder id is required", "asset_directory_id_required", http.StatusBadRequest)
var AssetDirectoryNotFoundErr = apigen.NewApiErr("Folder not found", "asset_directory_not_found", http.StatusNotFound)
var AssetDirectoryNotEmptyErr = apigen.NewApiErr("Folder is not empty", "asset_directory_not_empty", http.StatusBadRequest)
var AssetDirectoryCycleErr = apigen.NewApiErr("Folder cannot be moved inside itself", "asset_directory_cycle", http.StatusBadRequest)
var AssetDirectoryKeyTakenErr = apigen.NewApiErr("An asset or folder with this name already exists here", "asset_directory_key_exists", http.StatusBadRequest)
var AssetSpaceMoveUnsupportedErr = apigen.NewApiErr("Moving between spaces is not supported", "asset_space_move_unsupported", http.StatusBadRequest)

func mapAssetDirectoryErr(err error) error {
	switch {
	case errors.Is(err, state.ErrDirectoryNotFound):
		return AssetDirectoryNotFoundErr
	case errors.Is(err, state.ErrDirectoryNotEmpty):
		return AssetDirectoryNotEmptyErr
	case errors.Is(err, state.ErrDirectoryCycle):
		return AssetDirectoryCycleErr
	case errors.Is(err, state.ErrSpaceMoveUnsupported):
		return AssetSpaceMoveUnsupportedErr
	case errors.Is(err, state.ErrAssetAlreadyExists):
		return AssetDirectoryKeyTakenErr
	case errors.Is(err, state.ErrAssetKeyInvalid):
		return AssetKeyInvalidErr
	}
	return err
}

// notifyAssetDirectory pushes the directory's current row into the state stream.
func (h *Handler) notifyAssetDirectory(directoryID int32) {
	if dir, ok := h.Store.GetAssetDirectoryMeta(directoryID); ok {
		h.Store.NotifyAssetDirectoryUpdate(*dir)
	}
}

func (h *Handler) PostV1AssetDirectoriesList(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.AssetDirectoryList, error) {
	return &apigen.AssetDirectoryList{Items: h.filterAssetDirectories(ctx, h.Store.ListAssetDirectories())}, nil
}

func (h *Handler) PostV1AssetDirectoriesCreate(ctx apigen.Context, req *apigen.AssetDirectoryCreateRequest) (*apigen.AssetDirectory, error) {
	key := strings.TrimSpace(req.Key)
	if key == "" {
		return nil, AssetDirectoryKeyRequiredErr
	}
	if err := h.requireAccess(ctx, vCreate, eAsset, valueSpace(req.SpaceID), 0); err != nil {
		return nil, err
	}
	row, err := h.Store.CreateDirectory(req.SpaceID, req.ParentID, key, requestUserID(ctx))
	if err != nil {
		return nil, mapAssetDirectoryErr(err)
	}
	h.notifyAssetDirectory(int32(row.ID))
	dir, ok := h.Store.GetAssetDirectoryMeta(int32(row.ID))
	if !ok {
		return nil, AssetDirectoryNotFoundErr
	}
	return dir, nil
}

func (h *Handler) PostV1AssetDirectoriesMove(ctx apigen.Context, req *apigen.AssetDirectoryMoveRequest) (*apigen.AssetDirectory, error) {
	if req.DirectoryID == 0 {
		return nil, AssetDirectoryIDRequiredErr
	}
	existing, ok := h.Store.GetAssetDirectoryMeta(req.DirectoryID)
	if !ok {
		return nil, AssetDirectoryNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vUpdate, eAsset, int64(existing.SpaceID), 0, AssetDirectoryNotFoundErr); err != nil {
		return nil, err
	}
	if req.SpaceID != 0 && req.SpaceID != existing.SpaceID {
		if err := h.requireAccess(ctx, vCreate, eAsset, valueSpace(req.SpaceID), 0); err != nil {
			return nil, err
		}
	}
	// The space gate runs first: a rejected cross-space move must not leave the
	// directory reparented into its own space's root as a side effect.
	if req.SpaceID != 0 {
		if err := h.Store.MoveDirectorySpace(req.DirectoryID, req.SpaceID); err != nil {
			return nil, mapAssetDirectoryErr(err)
		}
	}
	row, err := h.Store.MoveDirectory(req.DirectoryID, req.NewParentID)
	if err != nil {
		return nil, mapAssetDirectoryErr(err)
	}
	h.notifyAssetDirectory(int32(row.ID))
	dir, ok := h.Store.GetAssetDirectoryMeta(int32(row.ID))
	if !ok {
		return nil, AssetDirectoryNotFoundErr
	}
	return dir, nil
}

func (h *Handler) PostV1AssetDirectoriesRename(ctx apigen.Context, req *apigen.AssetDirectoryRenameRequest) (*apigen.AssetDirectory, error) {
	if req.DirectoryID == 0 {
		return nil, AssetDirectoryIDRequiredErr
	}
	if strings.TrimSpace(req.NewKey) == "" {
		return nil, AssetDirectoryKeyRequiredErr
	}
	if existing, ok := h.Store.GetAssetDirectoryMeta(req.DirectoryID); !ok {
		return nil, AssetDirectoryNotFoundErr
	} else if err := h.requireEntityAccess(ctx, vUpdate, eAsset, int64(existing.SpaceID), 0, AssetDirectoryNotFoundErr); err != nil {
		return nil, err
	}
	row, err := h.Store.RenameDirectory(req.DirectoryID, strings.TrimSpace(req.NewKey))
	if err != nil {
		return nil, mapAssetDirectoryErr(err)
	}
	h.notifyAssetDirectory(int32(row.ID))
	dir, ok := h.Store.GetAssetDirectoryMeta(int32(row.ID))
	if !ok {
		return nil, AssetDirectoryNotFoundErr
	}
	return dir, nil
}

func (h *Handler) PostV1AssetDirectoriesDelete(ctx apigen.Context, req *apigen.AssetDirectoryDeleteRequest) error {
	if req.DirectoryID == 0 {
		return AssetDirectoryIDRequiredErr
	}
	if existing, ok := h.Store.GetAssetDirectoryMeta(req.DirectoryID); !ok {
		return AssetDirectoryNotFoundErr
	} else if err := h.requireEntityAccess(ctx, vDelete, eAsset, int64(existing.SpaceID), 0, AssetDirectoryNotFoundErr); err != nil {
		return err
	}
	if err := h.Store.DeleteDirectory(req.DirectoryID); err != nil {
		return mapAssetDirectoryErr(err)
	}
	h.Store.NotifyAssetDirectoryUpdate(apigen.AssetDirectory{ID: req.DirectoryID, Deleted: true})
	return nil
}
