package webuihandler

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/assetstore"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var (
	AssetIDRequiredErr    = apigen.NewApiErr("Asset id is required", "asset_id_required", http.StatusBadRequest)
	AssetKeyRequiredErr   = apigen.NewApiErr("Asset key is required", "asset_key_required", http.StatusBadRequest)
	AssetKeyInvalidErr    = apigen.NewApiErr("Asset key must be a valid file name", "asset_key_invalid", http.StatusBadRequest)
	AssetNotFoundErr      = apigen.NewApiErr("Asset not found", "asset_not_found", http.StatusNotFound)
	AssetAlreadyExistsErr = apigen.NewApiErr("Asset key already exists", "asset_key_exists", http.StatusBadRequest)
)

func validateUploadAssetName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", apigen.NewApiErr("Asset name is required", "asset_name_required", http.StatusBadRequest)
	}
	if !state.ValidAssetKey(name) {
		return "", apigen.NewApiErr("Asset name is invalid", "asset_name_invalid", http.StatusBadRequest)
	}
	return name, nil
}

// uniqueAssetName suffixes name until it is free among the assets of the
// target directory (0 = spaceID's root).
func (h *Handler) uniqueAssetName(name string, spaceID, directoryID int32) string {
	if _, ok := h.Store.GetAssetInDirectory(spaceID, directoryID, name); !ok {
		return name
	}
	for suffix := 1; ; suffix++ {
		candidate := name + strconv.Itoa(suffix)
		if _, ok := h.Store.GetAssetInDirectory(spaceID, directoryID, candidate); !ok {
			return candidate
		}
	}
}

func mapAssetStoreErr(err error) error {
	if errors.Is(err, secrets.ErrLocked) {
		return SecretsLockedErr
	}
	if errors.Is(err, assetstore.ErrLargeAssetS3Config) {
		return apigen.NewApiErr(err.Error(), "large_asset_s3_config_required", http.StatusBadRequest)
	}
	if errors.Is(err, state.ErrAssetNotFound) {
		return AssetNotFoundErr
	}
	if errors.Is(err, state.ErrAssetAlreadyExists) {
		return AssetAlreadyExistsErr
	}
	if errors.Is(err, state.ErrAssetKeyInvalid) {
		return AssetKeyInvalidErr
	}
	if errors.Is(err, state.ErrDirectoryNotFound) {
		return AssetDirectoryNotFoundErr
	}
	if errors.Is(err, state.ErrSpaceMoveUnsupported) {
		return AssetSpaceMoveUnsupportedErr
	}
	return err
}

func requestUserID(ctx apigen.Context) int32 {
	return ctx.AttributionUserID()
}

func (h *Handler) PostV1AssetsList(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.AssetList, error) {
	return &apigen.AssetList{Items: h.filterAssetMetas(ctx, h.Store.ListAssets())}, nil
}

func (h *Handler) requireAssetAccess(ctx apigen.Context, verb apigen.AuthzVerb, assetID int32) error {
	meta, ok := h.Store.GetAssetMeta(assetID)
	if !ok {
		return AssetNotFoundErr
	}
	return h.requireEntityAccess(ctx, verb, eAsset, int64(meta.SpaceID), int64(meta.ID), AssetNotFoundErr)
}

func (h *Handler) PostV1AssetsGet(ctx apigen.Context, req *apigen.AssetGetRequest) (*apigen.AssetVersion, error) {
	if req.AssetID <= 0 {
		return nil, AssetIDRequiredErr
	}
	if err := h.requireAssetAccess(ctx, vView, req.AssetID); err != nil {
		return nil, err
	}
	asset, ok, err := h.Assets.GetAssetForPreview(req.AssetID, req.Version)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, AssetNotFoundErr
	}
	return asset, nil
}

func (h *Handler) PostV1AssetsCreate(ctx apigen.Context, req *apigen.AssetCreateRequest) (*apigen.AssetVersion, error) {
	key := strings.TrimSpace(req.Key)
	if key == "" {
		return nil, AssetKeyRequiredErr
	}
	if err := h.requireAccess(ctx, vCreate, eAsset, valueSpace(req.SpaceID), 0); err != nil {
		return nil, err
	}
	asset, err := h.Assets.CreateAsset(ctx, key, req.SpaceID, req.AssetDirectoryID, requestUserID(ctx), req.Blob)
	if err != nil {
		return nil, mapAssetStoreErr(err)
	}
	return asset, nil
}

func (h *Handler) PostV1AssetsSet(ctx apigen.Context, req *apigen.AssetSetRequest) (*apigen.AssetVersion, error) {
	if req.AssetID <= 0 {
		return nil, AssetIDRequiredErr
	}
	if err := h.requireAssetAccess(ctx, vUpdate, req.AssetID); err != nil {
		return nil, err
	}
	asset, err := h.Assets.AppendAssetVersion(ctx, req.AssetID, requestUserID(ctx), req.Blob)
	if err != nil {
		return nil, mapAssetStoreErr(err)
	}
	return asset, nil
}

func (h *Handler) PostV1AssetsUpload(ctx apigen.Context, request *http.Request, writer http.ResponseWriter) error {
	query := request.URL.Query()
	if request.ContentLength < 0 {
		return apigen.NewApiErr("Asset upload requires a Content-Length header", "asset_upload_content_length_required", http.StatusBadRequest)
	}
	if request.ContentLength > math.MaxInt32 {
		return apigen.NewApiErr("Asset upload is too large", "asset_upload_too_large", http.StatusBadRequest)
	}

	// The two target params mean different things. "asset_id" appends the next
	// version of that exact asset. "name" asks for a new asset and gets a
	// numeric suffix if that name is taken in the target space's root, which is
	// what the web UI's file picker wants.
	var (
		asset *apigen.AssetVersion
		err   error
	)
	if rawAssetID := strings.TrimSpace(query.Get("asset_id")); rawAssetID != "" {
		parsed, parseErr := strconv.ParseInt(rawAssetID, 10, 32)
		if parseErr != nil || parsed <= 0 {
			return apigen.NewApiErr("Asset id is invalid", "asset_id_invalid", http.StatusBadRequest)
		}
		if accessErr := h.requireAssetAccess(ctx, vUpdate, int32(parsed)); accessErr != nil {
			return accessErr
		}
		asset, err = h.Assets.AppendAssetVersionFromReader(ctx, int32(parsed), requestUserID(ctx), request.ContentLength, request.Body)
	} else {
		name, nameErr := validateUploadAssetName(query.Get("name"))
		if nameErr != nil {
			return nameErr
		}
		var spaceID int32
		if rawSpaceID := strings.TrimSpace(query.Get("space_id")); rawSpaceID != "" {
			parsed, parseErr := strconv.ParseInt(rawSpaceID, 10, 32)
			if parseErr != nil {
				return apigen.NewApiErr("Asset space ID is invalid", "asset_space_id_invalid", http.StatusBadRequest)
			}
			spaceID = int32(parsed)
		}
		var directoryID int32
		if rawDirectoryID := strings.TrimSpace(query.Get("directory_id")); rawDirectoryID != "" {
			parsed, parseErr := strconv.ParseInt(rawDirectoryID, 10, 32)
			if parseErr != nil || parsed < 0 {
				return apigen.NewApiErr("Asset directory ID is invalid", "asset_directory_id_invalid", http.StatusBadRequest)
			}
			directoryID = int32(parsed)
		}
		if accessErr := h.requireAccess(ctx, vCreate, eAsset, valueSpace(spaceID), 0); accessErr != nil {
			return accessErr
		}
		key := h.uniqueAssetName(name, spaceID, directoryID)
		asset, err = h.Assets.CreateAssetFromReader(ctx, key, spaceID, directoryID, requestUserID(ctx), request.ContentLength, request.Body)
	}
	if err != nil {
		return mapAssetStoreErr(err)
	}
	apigen.Respond(ctx, request, writer, asset, nil)
	return nil
}

func (h *Handler) PostV1AssetsRename(ctx apigen.Context, req *apigen.AssetRenameRequest) (*apigen.AssetMeta, error) {
	if req.AssetID <= 0 {
		return nil, AssetIDRequiredErr
	}
	newKey := strings.TrimSpace(req.NewKey)
	if newKey == "" {
		return nil, AssetKeyRequiredErr
	}
	if err := h.requireAssetAccess(ctx, vUpdate, req.AssetID); err != nil {
		return nil, err
	}
	meta, err := h.Assets.RenameAsset(ctx, req.AssetID, newKey)
	if err != nil {
		return nil, mapAssetStoreErr(err)
	}
	return meta, nil
}

// PostV1AssetsMove relocates an asset within its space's folder tree, or —
// when space_id names another space — moves it there. Version rows and every
// pinned mount and reference are untouched either way. A cross-space move is
// allowed only while no deployment outside the destination space references
// the asset.
func (h *Handler) PostV1AssetsMove(ctx apigen.Context, req *apigen.AssetMoveRequest) (*apigen.AssetMeta, error) {
	if req.AssetID <= 0 {
		return nil, AssetIDRequiredErr
	}
	existing, ok := h.Store.GetAssetMeta(req.AssetID)
	if !ok {
		return nil, AssetNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vUpdate, eAsset, int64(existing.SpaceID), int64(existing.ID), AssetNotFoundErr); err != nil {
		return nil, err
	}
	destSpace := state.NormalizedUserSpaceID(req.SpaceID)
	spaceChanging := req.SpaceID != 0 && destSpace != existing.SpaceID
	// Moving into another space also needs the right to create an asset there.
	if spaceChanging {
		if err := h.requireAccess(ctx, vCreate, eAsset, valueSpace(req.SpaceID), 0); err != nil {
			return nil, err
		}
	}
	if spaceChanging {
		// Deployment writes hold the same lock, so no new reference can appear
		// between the locality check and the move.
		unlockReferences := h.ConfigService.LockReferences()
		defer unlockReferences()
		if destSpace != state.DefaultSpaceID && h.referencesOutsideSpace(h.assetVersionIDSet(req.AssetID), assetRefIDs, destSpace) {
			return nil, MoveReferencesOutsideSpaceErr
		}
		if err := h.Store.MoveAssetSpace(req.AssetID, req.SpaceID, req.AssetDirectoryID, ctx.AttributionUserID()); err != nil {
			return nil, mapAssetStoreErr(err)
		}
		// Tombstone for clients that saw the old space but cannot see the new
		// one — updates a user cannot view are dropped, and nothing else says
		// "gone". The update below re-adds the row where the destination is
		// visible.
		h.Store.NotifyAssetDeleted(existing)
	} else if _, err := h.Store.MoveAssetDirectory(req.AssetID, req.AssetDirectoryID); err != nil {
		return nil, mapAssetStoreErr(err)
	}
	meta, ok := h.Store.GetAssetMeta(req.AssetID)
	if !ok {
		return nil, AssetNotFoundErr
	}
	h.Store.NotifyAssetUpdate(meta)
	return meta, nil
}

func (h *Handler) assetVersionIDSet(assetID int32) map[int32]struct{} {
	versions := h.Store.ListAssetVersions(assetID)
	ids := make([]int32, 0, len(versions))
	for _, v := range versions {
		ids = append(ids, v.ID)
	}
	return int32Set(ids)
}

func (h *Handler) PostV1AssetsDelete(ctx apigen.Context, req *apigen.AssetDeleteRequest) error {
	// Held across the reference check and the delete so a deployment cannot pin
	// a version of this asset in between. Secrets and configs have refused
	// referenced deletes this way all along; assets simply lacked the reverse
	// lookup until the space-move work added one.
	unlockReferences := h.ConfigService.LockReferences()
	defer unlockReferences()
	if req.AssetID <= 0 {
		return AssetIDRequiredErr
	}
	if err := h.requireAssetAccess(ctx, vDelete, req.AssetID); err != nil {
		return err
	}
	if h.deploymentUsesAssetID(h.assetVersionIDSet(req.AssetID)) {
		return ReferenceInUseErr
	}
	if err := h.Assets.DeleteAsset(ctx, req.AssetID); err != nil {
		return mapAssetStoreErr(err)
	}
	return nil
}
