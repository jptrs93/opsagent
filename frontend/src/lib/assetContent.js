import {loginS} from "../state/login.js";
import {assetMetasS} from "../state/deployments.js";
import {PREVIEW_LIMIT_BYTES} from "./assetExplorer.js";
import {handleErr} from "../capi/err.js";
import {decodeAsset} from "../capi/model.js";

// fetchAssetContent streams one content version's raw bytes from
// GET /v1/assets/content. All asset metadata travels on the state stream;
// this is the only content path the web API has.
export async function fetchAssetContent(contentVersionId) {
    const token = loginS.val?.token;
    const response = await fetch(`/v1/assets/content?content_version_id=${Number(contentVersionId)}`, {
        headers: token ? {Authorization: `Bearer ${token}`} : {},
        credentials: "include",
    });
    if (!response.ok) throw new Error(`Asset content download failed (HTTP ${response.status})`);
    return new Uint8Array(await response.arrayBuffer());
}

export async function uploadAsset(params, blob) {
    const token = loginS.val?.token;
    const query = new URLSearchParams(params);
    const response = await fetch(`/v1/assets/upload?${query}`, {
        method: "POST",
        headers: {
            "Content-Type": "application/octet-stream",
            Accept: "application/x-protobuf",
            ...(token ? {Authorization: `Bearer ${token}`} : {}),
        },
        credentials: "include",
        body: blob,
    });
    if (!response.ok) await handleErr(response);
    return decodeAsset(await response.arrayBuffer());
}

// loadAssetPreview resolves {assetId, version} (version 0 = latest) against
// the live asset state and returns the editor's hydration shape. Content is
// fetched only for previewable sizes; `large` tells the editor why `blob` is
// absent.
export async function loadAssetPreview({assetId, version}) {
    const asset = (assetMetasS.val || []).find((a) => Number(a.id) === Number(assetId));
    const wanted = Number(version || 0);
    const cv = wanted
        ? (asset?.contentVersions || []).find((v) => Number(v.version) === wanted)
        : asset?.contentVersions?.[0];
    if (!asset || !cv) throw new Error("Asset not found");
    const sizeBytes = Number(cv.sizeBytes || 0);
    const large = sizeBytes > PREVIEW_LIMIT_BYTES;
    return {
        assetId: Number(asset.id),
        key: asset.key,
        version: Number(cv.version || 0),
        createdAt: cv.createdAt instanceof Date ? cv.createdAt : null,
        sizeBytes,
        large,
        blob: large ? null : await fetchAssetContent(cv.id),
    };
}
