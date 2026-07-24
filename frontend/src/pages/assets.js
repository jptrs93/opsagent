import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {handleErr} from "../capi/err.js";
import {decodeAsset} from "../capi/model.js";
import {assetEditor, preloadAssetCodeEditor} from "../components/assetEditor.js";
import {inlineEditableInput} from "../components/inlineEditableInput.js";
import {referenceUsageOverlay} from "../components/referenceUsageOverlay.js";
import {editIcon, trashIcon} from "../lib/icons.js";
import {formatDateTime} from "../lib/date.js";
import {deploymentUsages} from "../lib/referenceUsage.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {assetMetasS, deploymentsS, machinesS, spacesS} from "../state/deployments.js";
import {loginS} from "../state/login.js";
import {containerWorkload} from "../lib/deploymentConfig.js";

const { div, h2, p, span, input, button, table, thead, tbody, tr, th, td, colgroup, col } = van.tags;

export {preloadAssetCodeEditor};

const fmtSize = (n) => {
    if (!n) return "0 B";
    if (n < 1000) return `${n} B`;
    if (n < 1000 * 1000) return `${(n / 1000).toFixed(1)} KB`;
    return `${(n / 1000 / 1000).toFixed(2)} MB`;
};

const assetRefMatches = (assetKey, assetIDs, ref) => {
    if (!ref) return false;
    const refAssetId = Number(ref.assetId || 0);
    if (refAssetId) return assetIDs.has(refAssetId);
    return Boolean(assetKey && ref.asset === assetKey);
};

const assetMountRefMatches = (assetIDs, ref) => assetIDs.has(Number(ref?.assetId || 0));

const latestAssets = (items) => {
    const latest = new Map();
    for (const item of items || []) {
        if (!item?.key) continue;
        const current = latest.get(item.key);
        if (!current || Number(item.version || 0) > Number(current.version || 0)) latest.set(item.key, item);
    }
    return Array.from(latest.values()).sort((a, b) => (a.key || '').localeCompare(b.key || ''));
};

const smallBtn = (text, onclick, cls, disabledWhen) => button({
    type: "button",
    disabled: disabledWhen,
    class: () => `text-xs px-3 py-1 rounded-md font-medium transition-colors ${cls} ${
        disabledWhen && disabledWhen() ? "opacity-50 cursor-not-allowed" : "cursor-pointer"}`,
    onclick: async (e) => { if (disabledWhen && disabledWhen()) return; await onclick(e); },
}, text);

async function uploadAssetFile(file, name, onProgress) {
    const params = new URLSearchParams({name});
    return new Promise((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        xhr.open("POST", `/v1/assets/upload?${params}`);
        xhr.withCredentials = true;
        xhr.responseType = "arraybuffer";
        xhr.setRequestHeader("Accept", "application/x-protobuf");
        xhr.setRequestHeader("Content-Type", "application/octet-stream");
        const token = loginS.val?.token;
        if (token) xhr.setRequestHeader("Authorization", `Bearer ${token}`);

        xhr.upload.onprogress = (event) => {
            onProgress(event.loaded, event.lengthComputable ? event.total : file.size);
        };
        xhr.onload = async () => {
            if (xhr.status >= 200 && xhr.status < 300) {
                onProgress(file.size, file.size);
                resolve(decodeAsset(xhr.response));
                return;
            }
            try {
                await handleErr({
                    ok: false,
                    status: xhr.status,
                    arrayBuffer: async () => xhr.response || new ArrayBuffer(0),
                });
            } catch (e) {
                reject(e);
            }
        };
        xhr.onerror = () => reject(new Error("Asset upload failed"));
        xhr.onabort = () => reject(new Error("Asset upload cancelled"));
        xhr.send(file);
    });
}

export function assetsPage() {
    const rows = van.state(latestAssets(assetMetasS.val));
    const error = van.state(null);
    const search = van.state("");
    const selected = van.state(null);
    const uploadOverlayOpen = van.state(false);
    const uploadFile = van.state(null);
    const uploadSaving = van.state(false);
    const uploadError = van.state("");
    const uploadProgressStatus = van.state("");
    const uploadProgressName = van.state("");
    const uploadProgressLoaded = van.state(0);
    const uploadProgressTotal = van.state(0);
    const uploadedAssetKey = van.state("");
    const uploadedAssetName = van.state("");
    const deleteTarget = van.state(null);
    const deleteSaving = van.state(false);
    const usageTarget = van.state(null);
    const assetMutationKey = van.state("");
    const assetNameDrafts = new Map();

    const reloadRows = async () => {
        const res = await capi.postV1AssetsList({});
        rows.val = res.items || [];
    };

    const reload = async () => {
        try {
            error.val = null;
            await reloadRows();
        } catch (e) {
            error.val = e.message;
        }
    };

    van.derive(() => {
        rows.val = latestAssets(assetMetasS.val);
    });

    reload();

    const openAsset = (asset, version = asset.version || 0) => {
        selected.val = {
            mode: "edit",
            key: asset.key,
            version,
            latestVersion: Number(asset.version || version || 0),
            spaceId: Number(asset.spaceId || 0),
        };
    };

    const addAsset = () => {
        selected.val = {mode: "create", initialKey: "rename_me.yaml"};
    };

    const prepareAssetUpload = (file) => {
        if (!file) { error.val = "Choose a file to upload"; return; }
        error.val = null;
        uploadError.val = "";
        uploadOverlayOpen.val = true;
        uploadFile.val = file;
        uploadSaving.val = false;
        uploadProgressStatus.val = "Ready";
        uploadProgressName.val = file.name;
        uploadProgressLoaded.val = 0;
        uploadProgressTotal.val = file.size;
        uploadedAssetKey.val = "";
        uploadedAssetName.val = file.name;
    };

    const closeUploadOverlay = () => {
        if (uploadSaving.val) return;
        uploadOverlayOpen.val = false;
        uploadFile.val = null;
        uploadError.val = "";
    };

    const uploadAsset = async () => {
        const file = uploadFile.val;
        const name = uploadedAssetName.val.trim();
        if (!file) { uploadError.val = "Choose a file to upload"; return; }
        if (!name) { uploadError.val = "Asset name is required"; return; }
        try {
            error.val = null;
            uploadError.val = "";
            uploadSaving.val = true;
            uploadProgressStatus.val = "Uploading";
            uploadProgressName.val = file.name;
            uploadProgressLoaded.val = 0;
            uploadProgressTotal.val = file.size;
            uploadedAssetKey.val = "";
            const asset = await uploadAssetFile(file, name, (loaded, total) => {
                uploadProgressLoaded.val = loaded;
                uploadProgressTotal.val = total || file.size;
            });
            uploadProgressStatus.val = "Uploaded";
            uploadedAssetKey.val = asset.key;
            uploadedAssetName.val = asset.key;
            try {
                await reloadRows();
            } catch (e) {
                error.val = e.message;
            }
        } catch (e) {
            uploadProgressStatus.val = "Upload failed";
            uploadError.val = e.message;
        } finally {
            uploadSaving.val = false;
        }
    };

    const saveEditorAsset = async (request) => {
        const key = request.key;
        if (assetMutationKey.val) throw new Error("Another asset change is in progress");
        assetMutationKey.val = key;
        try {
            error.val = null;
            return await capi.postV1AssetsSet(request);
        } finally {
            if (assetMutationKey.val === key) assetMutationKey.val = "";
        }
    };

    const renameAsset = async (key, newKey, draft) => {
        if (assetMutationKey.val) return null;
        assetMutationKey.val = key;
        try {
            error.val = null;
            const asset = await capi.postV1AssetsRename({key, newKey});
            if (selected.val?.key === key) {
                selected.val = {...selected.val, key: asset.key};
            }
            draft.originalName.val = asset.key;
            draft.name.val = asset.key;
            assetNameDrafts.delete(key);
            assetNameDrafts.set(asset.key, draft);
            rows.val = (rows.val || []).map(row => row.key === key ? {...row, ...asset, key: asset.key} : row);
            try {
                await reloadRows();
            } catch (e) {
                error.val = e.message;
            }
            return asset;
        } catch (e) {
            error.val = e.message;
            return null;
        } finally {
            if (assetMutationKey.val === key) assetMutationKey.val = "";
        }
    };

    const requestDeleteAsset = (asset) => {
        if (assetMutationKey.val) return;
        deleteTarget.val = asset;
    };

    const cancelDeleteAsset = () => {
        if (deleteSaving.val) return;
        deleteTarget.val = null;
    };

    const confirmDeleteAsset = async () => {
        const target = deleteTarget.val;
        if (!target || deleteSaving.val) return;
        try {
            deleteSaving.val = true;
            error.val = null;
            await capi.postV1AssetsDelete({key: target.key});
            assetNameDrafts.delete(target.key);
            if (selected.val?.key === target.key) selected.val = null;
            await reloadRows();
            deleteTarget.val = null;
        } catch (e) {
            error.val = e.message;
        } finally {
            deleteSaving.val = false;
        }
    };

    const filteredRows = () => {
        if (!rows.val) return rows.val;
        const query = search.val.trim().toLowerCase();
        if (!query) return rows.val;
        return rows.val.filter(row =>
            row.key.toLowerCase().includes(query));
    };

    const assetReferenceIDs = (asset) => new Set([
        Number(asset.id || 0),
        ...(assetMetasS.val || [])
            .filter(item => item?.key === asset.key)
            .map(item => Number(item.id || 0)),
    ].filter(Boolean));

    const deploymentUsesAsset = (deployment, asset, assetIDs = assetReferenceIDs(asset)) => {
        const cfg = deployment?.config;
        if (!cfg || cfg.deleted) return false;
        const runtime = containerWorkload(cfg)?.runtime || {};
        const envRefs = Object.values(runtime.envVars || {});
        const mountRefs = runtime.assetMounts || [];
        return envRefs.some(ref => assetRefMatches(asset.key, assetIDs, ref))
            || mountRefs.some(ref => assetMountRefMatches(assetIDs, ref));
    };

    const usageForAsset = (asset) => {
        const assetIDs = assetReferenceIDs(asset);
        return deploymentUsages(
            deploymentsS.val,
            spacesS.val,
            machinesS.val,
            deployment => deploymentUsesAsset(deployment, asset, assetIDs),
        );
    };

    const usageButton = (asset) => {
        const deployments = usageForAsset(asset);
        if (!deployments.length) return "0";
        return button({
            type: "button",
            class: "cursor-pointer text-brand hover:text-blue-300 hover:underline",
            "aria-label": `Show usage for asset ${asset.key}`,
            onclick: (e) => {
                e.stopPropagation();
                usageTarget.val = {resourceName: asset.key, deployments};
            },
        }, String(deployments.length));
    };

    const uploadProgressText = () => {
        const total = uploadProgressTotal.val || 0;
        const loaded = Math.min(uploadProgressLoaded.val, total || uploadProgressLoaded.val);
        return `${uploadProgressStatus.val} ${uploadProgressName.val}: ${fmtSize(loaded)} / ${fmtSize(total)}`;
    };

    const uploadSuccessText = () => `Upload successful. Asset created. Size ${fmtSize(uploadProgressTotal.val)}.`;

    const uploadProgressPct = () => {
        const total = uploadProgressTotal.val || 0;
        if (!total) return "0%";
        return `${Math.min(100, Math.round((uploadProgressLoaded.val / total) * 100))}%`;
    };

    const assetNameEditor = (asset) => {
        let draft = assetNameDrafts.get(asset.key);
        if (!draft) {
            draft = {originalName: van.state(asset.key), name: van.state(asset.key)};
            assetNameDrafts.set(asset.key, draft);
        }
        const {originalName, name} = draft;
        return inlineEditableInput({
            value: name,
            dirty: () => name.val !== originalName.val,
            valid: () => Boolean(name.val.trim()),
            disabled: () => Boolean(assetMutationKey.val),
            oninput: event => { name.val = event.target.value; },
            onSave: async () => {
                const nextName = name.val.trim();
                if (nextName === originalName.val) {
                    name.val = originalName.val;
                    return;
                }
                const previousName = originalName.val;
                await renameAsset(previousName, nextName, draft);
            },
            onDiscard: () => { name.val = originalName.val; },
            inputClass: "w-full bg-transparent px-2 py-1 rounded border border-transparent hover:border-gray-700 focus:border-brand focus:outline-none font-mono font-normal text-asset",
            placeholder: "asset name",
            ariaLabel: `Asset name ${asset.key}`,
            saveAriaLabel: `Save asset name ${asset.key}`,
            discardAriaLabel: `Discard asset name change for ${asset.key}`,
        });
    };

    const assetActionClass = "flex items-center gap-1 whitespace-nowrap text-sm px-3 py-1.5 rounded-lg bg-gray-700 " +
        "text-gray-200 hover:bg-gray-600 transition-colors cursor-pointer";

    const listPanel = () => div(
        {class: "card flex-1 flex flex-col gap-3 min-w-0 min-h-0"},
        div({class: "flex flex-wrap items-center justify-between gap-3"},
            input({
                class: "text-input search-input",
                type: "search",
                placeholder: "Search assets",
                value: search,
                oninput: (e) => search.val = e.target.value,
            }),
            div({class: "flex items-center gap-2"},
                button({
                    type: "button",
                    class: assetActionClass,
                    onclick: addAsset,
                }, "Add asset"),
                button({
                    type: "button",
                    disabled: uploadSaving,
                    class: () => `${assetActionClass} ${uploadSaving.val ? "opacity-50 cursor-not-allowed" : ""}`,
                    onclick: () => { if (!uploadSaving.val) uploadPicker.click(); },
                }, "Upload asset"))),
        div({class: "deployment-table-scroll flex-1 min-h-0 overflow-auto"}, () => {
            if (rows.val === null) return p({class: "text-gray-400 text-sm"}, "Loading...");
            const visibleRows = filteredRows();
            if (rows.val.length === 0) return p({class: "text-gray-400 text-sm"}, "No assets yet. Click Add asset.");
            if (visibleRows.length === 0) return p({class: "text-gray-400 text-sm"}, "No assets match your search.");
            return table(
                {class: "w-full table-fixed text-sm"},
                colgroup(
                    col({style: "width:32%"}),
                    col({style: "width:24%"}),
                    col({style: "width:11%"}),
                    col({style: "width:10%"}),
                    col({style: "width:12%"}),
                    col({style: "width:11%"}),
                ),
                thead(tr({class: "text-left text-gray-400 border-b border-gray-700"},
                    th({class: "pb-2 pr-3 font-medium"}, "Key"),
                    th({class: "pb-2 pr-3 font-medium"}, "Created"),
                    th({class: "pb-2 pr-3 font-medium"}, "In use by"),
                    th({class: "pb-2 pr-3 font-medium"}, "Version"),
                    th({class: "pb-2 pr-3 font-medium"}, "Size"),
                    th({class: "pb-2 w-px"}, ""))),
                tbody(...visibleRows.map(row => tr(
                    {class: "border-b border-gray-800 last:border-0 align-middle"},
                    td({class: "py-1 pr-3 min-w-0"}, assetNameEditor(row)),
                    td({class: "py-1 pr-3 text-gray-400 truncate", title: formatDateTime(row.createdAt, "-")}, formatDateTime(row.createdAt, "-")),
                    td({class: "py-1 pr-3 text-gray-400 whitespace-nowrap tabular-nums"}, () => usageButton(row)),
                    td({class: "py-1 pr-3 text-gray-300"}, `v${row.version}`),
                    td({class: "py-1 pr-3 text-gray-400 truncate"}, fmtSize(row.sizeBytes || 0)),
                    td({class: "py-1 pl-2 text-right whitespace-nowrap w-px"},
                        div({class: "flex items-center justify-end gap-1"},
                            button({
                            type: "button",
                            title: `Edit asset ${row.key}`,
                            "aria-label": `Edit asset ${row.key}`,
                            disabled: () => Boolean(assetMutationKey.val),
                            class: () => `inline-flex h-7 w-7 items-center justify-center rounded text-gray-400 ` +
                                `hover:bg-surface hover:text-gray-100 transition-colors ${assetMutationKey.val
                                    ? "cursor-not-allowed opacity-50" : "cursor-pointer"}`,
                            onclick: () => openAsset(row),
                        }, editIcon()),
                            button({
                            type: "button",
                            title: `Delete asset ${row.key}`,
                            "aria-label": `Delete asset ${row.key}`,
                            disabled: () => Boolean(assetMutationKey.val),
                            class: () => `inline-flex h-7 w-7 items-center justify-center rounded text-gray-400 ` +
                                `hover:bg-surface hover:text-red-400 transition-colors ${assetMutationKey.val
                                    ? "cursor-not-allowed opacity-50" : "cursor-pointer"}`,
                            onclick: () => requestDeleteAsset(row),
                        }, trashIcon()))),
                ))),
            );
        }),
    );

    const deleteOverlay = () => {
        const target = deleteTarget.val;
        if (!target) return "";
        return div(
            {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"},
            div(
                {class: "card w-full max-w-md flex flex-col gap-4 shadow-2xl"},
                h2({class: "text-base font-semibold"}, "Confirm delete"),
                p({class: "text-sm text-gray-300"}, `Are you sure you want to delete asset ${target.key}?`),
                div({class: "flex items-center justify-end gap-2"},
                    smallBtn("Cancel", cancelDeleteAsset, "bg-gray-700 text-gray-200 hover:bg-gray-600", () => deleteSaving.val),
                    spinnerButton("Confirm", confirmDeleteAsset,
                        "text-xs px-3 py-1 rounded-md font-medium bg-red-600 text-white hover:bg-red-500",
                        "button", () => deleteSaving.val),
                ),
            ),
        );
    };

    const editorPanel = () => {
        const target = selected.val;
        return assetEditor({
            mode: target.mode,
            assetRef: target.mode === "create" ? null : {key: target.key, version: target.version},
            initialKey: target.initialKey || "",
            latestVersion: target.latestVersion || 0,
            spaceId: target.spaceId || 0,
            loadAsset: request => capi.postV1AssetsGet(request),
            saveAsset: saveEditorAsset,
            onSaved: reloadRows,
            onClose: () => { selected.val = null; },
        });
    };

    const leftPane = () => div(
        {class: () => `flex flex-col min-w-0 min-h-0 ${selected.val === null ? "flex-1" : "lg:w-[28rem]"}`},
        listPanel,
    );

    const uploadPicker = input({
        class: "hidden",
        type: "file",
        onchange: async (e) => {
            const file = e.target.files?.[0] || null;
            e.target.value = "";
            prepareAssetUpload(file);
        },
    });

    const uploadOverlay = () => uploadOverlayOpen.val ? div(
        {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"},
        div({class: "card w-full max-w-lg flex flex-col gap-4 shadow-2xl"},
            h2({class: "text-base font-semibold"}, "Upload asset"),
            () => uploadedAssetKey.val && !uploadSaving.val && uploadProgressStatus.val === "Uploaded"
                ? p({class: "text-sm font-medium text-green-300"}, uploadSuccessText)
                : "",
            () => uploadSaving.val ? div({class: "flex flex-col gap-2"},
                p({class: "text-xs text-gray-400"}, uploadProgressText),
                div({class: "h-2 overflow-hidden rounded-full bg-gray-800"},
                    div({class: "h-full rounded-full bg-brand transition-all", style: () => `width:${uploadProgressPct()}`})),
            ) : "",
            () => !uploadedAssetKey.val && !uploadSaving.val ? div({class: "flex flex-col gap-3"},
                p({class: "text-xs text-gray-400"}, () => `Selected ${uploadProgressName.val}, ${fmtSize(uploadProgressTotal.val)}.`),
                labelField("Name", input({
                    class: "text-input font-mono",
                    value: uploadedAssetName,
                    oninput: (e) => uploadedAssetName.val = e.target.value,
                })),
            ) : "",
            () => uploadedAssetKey.val && !uploadSaving.val
                ? labelField("Name", div({class: "text-input font-mono text-gray-300"}, uploadedAssetName))
                : "",
            () => uploadError.val ? p({class: "text-sm text-red-400"}, uploadError) : "",
            () => !uploadedAssetKey.val && !uploadSaving.val ? div({class: "flex items-center justify-end gap-2"},
                smallBtn("Cancel", closeUploadOverlay, "bg-gray-700 text-gray-200 hover:bg-gray-600"),
                smallBtn(uploadProgressStatus.val === "Upload failed" ? "Retry upload" : "Upload", uploadAsset,
                    "bg-brand text-white hover:bg-blue-600", () => !uploadedAssetName.val.trim()),
            ) : "",
            () => uploadedAssetKey.val && !uploadSaving.val ? div({class: "flex justify-end pt-1"},
                button({
                    type: "button",
                    class: "text-sm px-3 py-1.5 rounded-lg bg-gray-700 text-gray-200 hover:bg-gray-600 transition-colors cursor-pointer",
                    onclick: closeUploadOverlay,
                }, "Close"),
            ) : "",
        ),
    ) : "";

    const usageOverlay = () => {
        const target = usageTarget.val;
        if (!target) return "";
        return referenceUsageOverlay(
            "asset",
            target.resourceName,
            target.deployments,
            [],
            () => usageTarget.val = null,
        );
    };

    return div(
        {class: "h-full min-h-0 overflow-hidden p-3 flex flex-col gap-3"},
        () => error.val ? p({class: "text-red-400"}, `Error: ${error.val}`) : "",
        uploadPicker,
        div({class: "flex-1 flex flex-col lg:flex-row gap-3 min-h-0"}, leftPane, () => selected.val === null ? "" : editorPanel()),
        uploadOverlay,
        deleteOverlay,
        usageOverlay,
    );
}

function labelField(label, child) {
    return div({class: "flex flex-col gap-1"},
        span({class: "text-xs text-gray-400"}, label),
        child,
    );
}
