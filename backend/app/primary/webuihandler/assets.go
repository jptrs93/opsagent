package webuihandler

import (
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/assetstore"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

var (
	AssetKeyRequiredErr   = apigen.NewApiErr("Asset key is required", "asset_key_required", http.StatusBadRequest)
	AssetNotFoundErr      = apigen.NewApiErr("Asset not found", "asset_not_found", http.StatusNotFound)
	AssetAlreadyExistsErr = apigen.NewApiErr("Asset key already exists", "asset_key_exists", http.StatusBadRequest)
)

func validateUploadAssetName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", apigen.NewApiErr("Asset name is required", "asset_name_required", http.StatusBadRequest)
	}
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return "", apigen.NewApiErr("Asset name is invalid", "asset_name_invalid", http.StatusBadRequest)
	}
	return name, nil
}

// uploadTargetKey returns the explicit key an upload is aimed at, or "" when the
// caller sent none and wants a new asset instead.
func uploadTargetKey(query url.Values) (string, error) {
	raw := strings.TrimSpace(query.Get("key"))
	if raw == "" {
		return "", nil
	}
	return validateUploadAssetName(raw)
}

func (h *Handler) uniqueAssetName(name, allowedExistingKey string) string {
	if _, ok := h.Store.GetAsset(name, 0); !ok || name == allowedExistingKey {
		return name
	}
	for suffix := 1; ; suffix++ {
		candidate := name + strconv.Itoa(suffix)
		if _, ok := h.Store.GetAsset(candidate, 0); !ok || candidate == allowedExistingKey {
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
	return err
}

func (h *Handler) PostV1AssetsList(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.AssetList, error) {
	return &apigen.AssetList{Items: h.Store.ListAssets()}, nil
}

func (h *Handler) PostV1AssetsGet(ctx apigen.Context, req *apigen.AssetGetRequest) (*apigen.Asset, error) {
	key := strings.TrimSpace(req.Key)
	if key == "" {
		return nil, AssetKeyRequiredErr
	}
	asset, ok, err := h.Assets.GetAssetForPreview(key, req.Version)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, AssetNotFoundErr
	}
	return asset, nil
}

func (h *Handler) PostV1AssetsSet(ctx apigen.Context, req *apigen.AssetSetRequest) (*apigen.Asset, error) {
	key := strings.TrimSpace(req.Key)
	if key == "" {
		return nil, AssetKeyRequiredErr
	}
	format := strings.TrimSpace(req.Format)
	if format == "" {
		format = "text"
	}
	asset, err := h.Assets.SetAsset(ctx, key, format, req.Blob, req.SpaceID)
	if err != nil {
		return nil, mapAssetStoreErr(err)
	}
	return asset, nil
}

func (h *Handler) PostV1AssetsUpload(ctx apigen.Context, request *http.Request, writer http.ResponseWriter) error {
	query := request.URL.Query()
	// The two name params mean different things. "key" targets an exact asset and
	// writes the next version of it, so uploads can update. "name" asks for a new
	// asset and gets a numeric suffix if that name is taken, which is what the
	// web UI's file picker wants.
	key, err := uploadTargetKey(query)
	if err != nil {
		return err
	}
	if key == "" {
		name, nameErr := validateUploadAssetName(query.Get("name"))
		if nameErr != nil {
			return nameErr
		}
		key = h.uniqueAssetName(name, "")
	}
	format := strings.TrimSpace(query.Get("format"))
	var spaceID int32
	spaceIDGiven := false
	if rawSpaceID := strings.TrimSpace(query.Get("space_id")); rawSpaceID != "" {
		parsed, parseErr := strconv.ParseInt(rawSpaceID, 10, 32)
		if parseErr != nil {
			return apigen.NewApiErr("Asset space ID is invalid", "asset_space_id_invalid", http.StatusBadRequest)
		}
		spaceID = int32(parsed)
		spaceIDGiven = true
	}
	// A new version inherits the format and space of the one it replaces. Falling
	// through to the create-time defaults instead would retype an nginx asset as
	// text and move it to the default space, neither of which the caller asked
	// for by omitting a query param.
	if existing, ok := h.Store.GetAsset(key, 0); ok {
		if format == "" {
			format = existing.Format
		}
		if !spaceIDGiven {
			spaceID = existing.SpaceID
		}
	}
	if format == "" {
		format = "text"
	}
	if request.ContentLength < 0 {
		return apigen.NewApiErr("Asset upload requires a Content-Length header", "asset_upload_content_length_required", http.StatusBadRequest)
	}
	if request.ContentLength > math.MaxInt32 {
		return apigen.NewApiErr("Asset upload is too large", "asset_upload_too_large", http.StatusBadRequest)
	}
	asset, err := h.Assets.SetAssetFromReader(ctx, key, format, request.ContentLength, request.Body, spaceID)
	if err != nil {
		return mapAssetStoreErr(err)
	}
	apigen.Respond(ctx, request, writer, asset, nil)
	return nil
}

func (h *Handler) PostV1AssetsRename(ctx apigen.Context, req *apigen.AssetRenameRequest) (*apigen.Asset, error) {
	key := strings.TrimSpace(req.Key)
	newKey := strings.TrimSpace(req.NewKey)
	if key == "" || newKey == "" {
		return nil, AssetKeyRequiredErr
	}
	asset, err := h.Assets.RenameAsset(ctx, key, newKey)
	if errors.Is(err, sqlite.ErrAssetAlreadyExists) {
		return nil, AssetAlreadyExistsErr
	}
	if errors.Is(err, sqlite.ErrAssetNotFound) {
		return nil, AssetNotFoundErr
	}
	return asset, err
}

func (h *Handler) PostV1AssetsDelete(ctx apigen.Context, req *apigen.AssetDeleteRequest) error {
	key := strings.TrimSpace(req.Key)
	if key == "" {
		return AssetKeyRequiredErr
	}
	if err := h.Assets.DeleteAsset(ctx, key); err != nil {
		return mapAssetStoreErr(err)
	}
	return nil
}
