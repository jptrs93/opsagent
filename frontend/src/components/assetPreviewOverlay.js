import {assetEditorOverlay} from "./assetEditor.js";

// `asset` is an option or meta carrying the stable asset id as `assetId` (form
// options) or `id` (raw AssetMeta).
export function assetPreviewOverlay(asset, loadAsset, onClose) {
    return assetEditorOverlay({
        mode: "read",
        assetRef: {assetId: Number(asset.assetId || asset.id || 0), version: asset.version || 0},
        latestVersion: asset.version || 0,
        spaceId: asset.spaceId || 0,
        loadAsset,
        onClose,
    });
}
