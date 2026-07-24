import {assetEditorOverlay} from "./assetEditor.js";

export function assetPreviewOverlay(assetMeta, loadAsset, onClose) {
    return assetEditorOverlay({
        mode: "read",
        assetRef: {key: assetMeta.key, version: assetMeta.version || 0},
        latestVersion: assetMeta.version || 0,
        spaceId: assetMeta.spaceId || 0,
        loadAsset,
        onClose,
    });
}
