package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/assetstore"
	"github.com/jptrs93/opsagent/backend/secrets"
)

var (
	AssetKeyRequiredErr = apigen.NewApiErr("Asset key is required", "asset_key_required", http.StatusBadRequest)
	AssetNotFoundErr    = apigen.NewApiErr("Asset not found", "asset_not_found", http.StatusNotFound)
)

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
	if errors.Is(err, secrets.ErrLocked) {
		return nil, SecretsLockedErr
	}
	if errors.Is(err, assetstore.ErrLargeAssetS3Config) {
		return nil, apigen.NewApiErr(err.Error(), "large_asset_s3_config_required", http.StatusBadRequest)
	}
	return asset, err
}

func (h *Handler) PostV1AssetsDelete(ctx apigen.Context, req *apigen.AssetDeleteRequest) error {
	key := strings.TrimSpace(req.Key)
	if key == "" {
		return AssetKeyRequiredErr
	}
	if err := h.Assets.DeleteAsset(ctx, key); err != nil {
		if errors.Is(err, secrets.ErrLocked) {
			return SecretsLockedErr
		}
		if errors.Is(err, assetstore.ErrLargeAssetS3Config) {
			return apigen.NewApiErr(err.Error(), "large_asset_s3_config_required", http.StatusBadRequest)
		}
		return err
	}
	return nil
}
