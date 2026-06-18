package handler

import (
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const maxInlineAssetBytes = 10 * 1024 * 1024

var (
	AssetKeyRequiredErr = apigen.NewApiErr("Asset key is required", "asset_key_required", http.StatusBadRequest)
	AssetTooLargeErr    = apigen.NewApiErr("Asset blob must be smaller than 10 MiB", "asset_too_large", http.StatusBadRequest)
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
	asset, ok := h.Store.GetAsset(key, req.Version)
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
	if len(req.Blob) > maxInlineAssetBytes {
		return nil, AssetTooLargeErr
	}
	format := strings.TrimSpace(req.Format)
	if format == "" {
		format = "text"
	}
	return h.Store.SetAsset(key, format, req.Blob, req.SpaceID), nil
}

func (h *Handler) PostV1AssetsDelete(ctx apigen.Context, req *apigen.AssetDeleteRequest) error {
	key := strings.TrimSpace(req.Key)
	if key == "" {
		return AssetKeyRequiredErr
	}
	h.Store.DeleteAsset(key)
	return nil
}
