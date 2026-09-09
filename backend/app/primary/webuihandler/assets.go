package webuihandler

import (
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/assets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
)

var (
	AssetIDRequiredErr    = apigen.NewApiErr("Asset id is required", "asset_id_required", http.StatusBadRequest)
	AssetKeyRequiredErr   = apigen.NewApiErr("Asset key is required", "asset_key_required", http.StatusBadRequest)
	AssetKeyInvalidErr    = apigen.NewApiErr("Asset key must be a valid file name", "asset_key_invalid", http.StatusBadRequest)
	AssetNotFoundErr      = apigen.NewApiErr("Asset not found", "asset_not_found", http.StatusNotFound)
	AssetAlreadyExistsErr = apigen.NewApiErr("Asset key already exists", "asset_key_exists", http.StatusBadRequest)
)

// uniqueAssetName suffixes name until it is free among the assets of the
// target directory (0 = spaceID's root).
func (h *Handler) uniqueAssetName(name string, spaceID, directoryID int32) string {
	if _, ok := assets.GetAssetInDirectory(h.Store.Queries(), spaceID, directoryID, name); !ok {
		return name
	}
	for suffix := 1; ; suffix++ {
		candidate := name + strconv.Itoa(suffix)
		if _, ok := assets.GetAssetInDirectory(h.Store.Queries(), spaceID, directoryID, candidate); !ok {
			return candidate
		}
	}
}

func mapAssetStoreErr(err error) error {
	if errors.Is(err, secrets.ErrLocked) {
		return SecretsLockedErr
	}
	if errors.Is(err, assets.ErrLargeAssetS3Config) {
		return apigen.NewApiErr(err.Error(), "large_asset_s3_config_required", http.StatusBadRequest)
	}
	if errors.Is(err, assets.ErrAssetNotFound) {
		return AssetNotFoundErr
	}
	if errors.Is(err, assets.ErrAssetAlreadyExists) {
		return AssetAlreadyExistsErr
	}
	if errors.Is(err, assets.ErrAssetKeyInvalid) {
		return AssetKeyInvalidErr
	}
	if errors.Is(err, assets.ErrDirectoryNotFound) {
		return AssetDirectoryNotFoundErr
	}
	if errors.Is(err, assets.ErrSpaceMoveUnsupported) {
		return AssetSpaceMoveUnsupportedErr
	}
	return err
}

func requestUserID(ctx apigen.Context) int32 {
	return ctx.AttributionUserID()
}

func (h *Handler) PostV1AssetsList(ctx apigen.Context) (*apigen.AssetEventList, error) {
	return &apigen.AssetEventList{Items: h.filterAssets(ctx, assets.ListAssets(h.Store.Queries()))}, nil
}

func (h *Handler) requireAssetAccess(ctx apigen.Context, verb apigen.AuthzVerb, assetID int32) error {
	asset, ok := assets.GetAsset(h.Store.Queries(), assetID)
	if !ok {
		return AssetNotFoundErr
	}
	return h.requireEntityAccess(ctx, verb, eAsset, int64(asset.SpaceID()), int64(asset.AssetID), AssetNotFoundErr)
}

// GetV1AssetsContent streams the raw bytes of one content version
// (?content_version_id=N). Metadata travels on the asset shapes; this route is
// the only way content leaves the server on the web API.
func (h *Handler) GetV1AssetsContent(ctx apigen.Context, request *http.Request, writer http.ResponseWriter) error {
	rawID := strings.TrimSpace(request.URL.Query().Get("content_version_id"))
	parsed, err := strconv.ParseInt(rawID, 10, 32)
	if err != nil || parsed <= 0 {
		return apigen.NewApiErr("Content version id is required", "asset_content_version_id_required", http.StatusBadRequest)
	}
	joined, ok := assets.GetAssetVersionJoined(h.Store.Queries(), int32(parsed))
	if !ok {
		return AssetNotFoundErr
	}
	if err := h.requireAssetAccess(ctx, vView, int32(joined.Asset.ID)); err != nil {
		return err
	}
	sizeBytes, body, err := h.Assets.OpenAsset(ctx, int32(parsed))
	if err != nil {
		return mapAssetStoreErr(err)
	}
	defer body.Close()
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Content-Length", strconv.FormatInt(sizeBytes, 10))
	if _, err := io.Copy(writer, body); err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("stream asset content %d failed", parsed), "err", err)
	}
	return nil
}

func (h *Handler) PostV1AssetsUpload(ctx apigen.Context, request *http.Request, writer http.ResponseWriter) error {
	asset, err := h.uploadAsset(ctx, request)
	if err != nil {
		return err
	}
	apigen.Respond(ctx, request, writer, asset, nil)
	return nil
}

func (h *Handler) uploadAsset(ctx apigen.Context, request *http.Request) (*apigen.AssetEvent, error) {
	query := request.URL.Query()
	if request.ContentLength < 0 {
		return nil, apigen.NewApiErr("Asset upload requires a Content-Length header", "asset_upload_content_length_required", http.StatusBadRequest)
	}
	if request.ContentLength > math.MaxInt32 {
		return nil, apigen.NewApiErr("Asset upload is too large", "asset_upload_too_large", http.StatusBadRequest)
	}

	if rawAssetID := strings.TrimSpace(query.Get("asset_id")); rawAssetID != "" {
		parsed, parseErr := strconv.ParseInt(rawAssetID, 10, 32)
		if parseErr != nil || parsed <= 0 {
			return nil, apigen.NewApiErr("Asset id is invalid", "asset_id_invalid", http.StatusBadRequest)
		}
		if err := h.requireAssetAccess(ctx, vUpdate, int32(parsed)); err != nil {
			return nil, err
		}
		asset, err := h.Assets.AppendAssetVersionFromReader(ctx, int32(parsed), requestUserID(ctx), request.ContentLength, request.Body)
		if err != nil {
			return nil, mapAssetStoreErr(err)
		}
		return asset, nil
	}

	key := strings.TrimSpace(query.Get("key"))
	if key == "" {
		return nil, AssetKeyRequiredErr
	}
	if !assets.ValidAssetKey(key) {
		return nil, AssetKeyInvalidErr
	}
	var spaceID int32
	if rawSpaceID := strings.TrimSpace(query.Get("space_id")); rawSpaceID != "" {
		parsed, parseErr := strconv.ParseInt(rawSpaceID, 10, 32)
		if parseErr != nil {
			return nil, apigen.NewApiErr("Asset space ID is invalid", "asset_space_id_invalid", http.StatusBadRequest)
		}
		spaceID = int32(parsed)
	}
	var directoryID int32
	if rawDirectoryID := strings.TrimSpace(query.Get("directory_id")); rawDirectoryID != "" {
		parsed, parseErr := strconv.ParseInt(rawDirectoryID, 10, 32)
		if parseErr != nil || parsed < 0 {
			return nil, apigen.NewApiErr("Asset directory ID is invalid", "asset_directory_id_invalid", http.StatusBadRequest)
		}
		directoryID = int32(parsed)
	}
	if err := h.requireAccess(ctx, vCreate, eAsset, valueSpace(spaceID), 0); err != nil {
		return nil, err
	}
	if uniqueKey, _ := strconv.ParseBool(query.Get("unique_key")); uniqueKey {
		key = h.uniqueAssetName(key, spaceID, directoryID)
	}
	asset, err := h.Assets.CreateAssetFromReader(ctx, key, spaceID, directoryID, requestUserID(ctx), request.ContentLength, request.Body)
	if err != nil {
		return nil, mapAssetStoreErr(err)
	}
	return asset, nil
}

func (h *Handler) PostV1AssetsRename(ctx apigen.Context, req *apigen.AssetRenameRequest) (*apigen.AssetEvent, error) {
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
func (h *Handler) PostV1AssetsMove(ctx apigen.Context, req *apigen.AssetMoveRequest) (*apigen.AssetEvent, error) {
	if req.AssetID <= 0 {
		return nil, AssetIDRequiredErr
	}
	existing, ok := assets.GetAsset(h.Store.Queries(), req.AssetID)
	if !ok {
		return nil, AssetNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vUpdate, eAsset, int64(existing.SpaceID()), int64(existing.AssetID), AssetNotFoundErr); err != nil {
		return nil, err
	}
	destSpace := nodes.NormalizedUserSpaceID(req.SpaceID)
	spaceChanging := req.SpaceID != 0 && destSpace != existing.SpaceID()
	// Moving into another space also needs the right to create an asset there.
	if spaceChanging {
		if err := h.requireAccess(ctx, vCreate, eAsset, valueSpace(req.SpaceID), 0); err != nil {
			return nil, err
		}
	}
	if spaceChanging {
		validate := func(q *pq.Queries) error {
			if destSpace == nodes.DefaultSpaceID {
				return nil
			}
			live, err := nodes.ReadLiveState(ctx, q)
			if err != nil {
				return err
			}
			if deployments.ReferencesOutsideSpace(live, h.assetVersionIDSet(req.AssetID), deployments.AssetRefIDs, destSpace) {
				return deployments.MoveReferencesOutsideSpaceErr
			}
			return nil
		}
		if err := assets.MoveAssetSpace(h.Store, req.AssetID, req.SpaceID, req.AssetDirectoryID, ctx.AttributionUserID(), validate); err != nil {
			return nil, mapAssetStoreErr(err)
		}
	} else if _, err := assets.MoveAssetDirectory(h.Store, req.AssetID, req.AssetDirectoryID); err != nil {
		return nil, mapAssetStoreErr(err)
	}
	asset, ok := assets.GetAsset(h.Store.Queries(), req.AssetID)
	if !ok {
		return nil, AssetNotFoundErr
	}

	return asset, nil
}

func (h *Handler) assetVersionIDSet(assetID int32) map[int32]struct{} {
	return deployments.Int32Set(assets.AssetVersionIDs(h.Store.Queries(), assetID))
}

func (h *Handler) PostV1AssetsDelete(ctx apigen.Context, req *apigen.AssetDeleteRequest) error {
	if req.AssetID <= 0 {
		return AssetIDRequiredErr
	}
	if err := h.requireAssetAccess(ctx, vDelete, req.AssetID); err != nil {
		return err
	}
	// Held across the reference check and the delete so a deployment cannot pin
	// a version of this asset in between; the asset operation lock orders ahead
	// of the global lock, matching the upload and reclaim paths.
	assetOps := h.Assets.AssetOperationLocker()
	assetOps.Lock()
	defer assetOps.Unlock()
	validate := func(q *pq.Queries) error {
		live, err := nodes.ReadLiveState(ctx, q)
		if err != nil {
			return err
		}
		if details := deployments.RefDetails(ctx, q, live, h.assetVersionIDSet(req.AssetID), deployments.AssetRefIDs); len(details) > 0 {
			return deployments.ReferenceInUseDetailErr("Asset", details)
		}
		return nil
	}
	if err := h.Assets.DeleteAssetLocked(ctx, req.AssetID, validate); err != nil {
		return mapAssetStoreErr(err)
	}
	return nil
}
