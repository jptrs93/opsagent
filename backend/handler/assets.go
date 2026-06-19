package handler

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/assetstore"
	"github.com/jptrs93/opsagent/backend/secrets"
)

var (
	AssetKeyRequiredErr = apigen.NewApiErr("Asset key is required", "asset_key_required", http.StatusBadRequest)
	AssetNotFoundErr    = apigen.NewApiErr("Asset not found", "asset_not_found", http.StatusNotFound)
)

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
	key := strings.TrimSpace(query.Get("key"))
	if key == "" {
		return AssetKeyRequiredErr
	}
	format := strings.TrimSpace(query.Get("format"))
	if format == "" {
		format = "text"
	}
	var spaceID int32
	if rawSpaceID := strings.TrimSpace(query.Get("space_id")); rawSpaceID != "" {
		parsed, err := strconv.ParseInt(rawSpaceID, 10, 32)
		if err != nil {
			return apigen.NewApiErr("Asset space ID is invalid", "asset_space_id_invalid", http.StatusBadRequest)
		}
		spaceID = int32(parsed)
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
