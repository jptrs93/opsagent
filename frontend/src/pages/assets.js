import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {handleErr} from "../capi/err.js";
import {decodeAsset} from "../capi/model.js";
import {closeIcon, trashIcon} from "../lib/icons.js";
import {loginS} from "../state/login.js";

const { div, h2, p, span, input, button, table, thead, tbody, tr, th, td, textarea } = van.tags;

const encoder = new TextEncoder();
const decoder = new TextDecoder();

const fmtDate = (d) => d instanceof Date && !Number.isNaN(d.getTime()) ? d.toLocaleString() : "";
const fmtSize = (n) => {
    if (!n) return "0 B";
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
    return `${(n / 1024 / 1024).toFixed(2)} MiB`;
};

const smallBtn = (text, onclick, cls, disabledWhen) => button({
    type: "button",
    disabled: disabledWhen,
    class: () => `text-xs px-3 py-1 rounded-md font-medium transition-colors ${cls} ${
        disabledWhen && disabledWhen() ? "opacity-50 cursor-not-allowed" : "cursor-pointer"}`,
    onclick: async (e) => { if (disabledWhen && disabledWhen()) return; await onclick(e); },
}, text);

async function uploadAssetFile(file, onProgress) {
    const params = new URLSearchParams({name: file.name});
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
    const rows = van.state(null);
    const error = van.state(null);
    const search = van.state("");
    const selected = van.state(null);
    const loadingAsset = van.state(false);
    const uploadSaving = van.state(false);
    const uploadProgressStatus = van.state("");
    const uploadProgressName = van.state("");
    const uploadProgressLoaded = van.state(0);
    const uploadProgressTotal = van.state(0);

    const draftKey = van.state("");
    const draftContent = van.state("");
    const draftVersion = van.state(0);
    const draftCreatedAt = van.state(null);
    const draftLarge = van.state(false);
    const draftSizeBytes = van.state(0);
    const original = {key: "", content: "", version: 0, large: false};

    const setOriginal = (asset) => {
        original.key = asset.key || "";
        original.large = !!asset.location;
        original.content = original.large ? "" : decoder.decode(asset.blob || new Uint8Array());
        original.version = asset.version || 0;
    };

    const setDraft = (asset) => {
        setOriginal(asset);
        draftKey.val = original.key;
        draftContent.val = original.content;
        draftVersion.val = original.version;
        draftCreatedAt.val = asset.createdAt || null;
        draftLarge.val = original.large;
        draftSizeBytes.val = asset.sizeBytes || (asset.blob ? asset.blob.length : 0);
    };

    const clearDraft = () => {
        selected.val = null;
        original.key = "";
        original.content = "";
        original.version = 0;
        original.large = false;
        draftKey.val = "";
        draftContent.val = "";
        draftVersion.val = 0;
        draftCreatedAt.val = null;
        draftLarge.val = false;
        draftSizeBytes.val = 0;
    };

    const isDirty = () => !draftLarge.val && (
        draftKey.val.trim() !== original.key
        || draftContent.val !== original.content
    );

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

    reload();

    const loadAsset = async (key, version = 0) => {
        try {
            error.val = null;
            loadingAsset.val = true;
            const asset = await capi.postV1AssetsGet({key, version});
            selected.val = asset.key;
            setDraft(asset);
        } catch (e) {
            error.val = e.message;
        } finally {
            loadingAsset.val = false;
        }
    };

    const addAsset = () => {
        clearDraft();
        draftKey.val = "nginx.conf";
        selected.val = "";
    };

    const uploadAsset = async (file) => {
        if (!file) { error.val = "Choose a file to upload"; return; }
        try {
            error.val = null;
            uploadSaving.val = true;
            uploadProgressStatus.val = "Uploading";
            uploadProgressName.val = file.name;
            uploadProgressLoaded.val = 0;
            uploadProgressTotal.val = file.size;
            const asset = await uploadAssetFile(file, (loaded, total) => {
                uploadProgressLoaded.val = loaded;
                uploadProgressTotal.val = total || file.size;
            });
            selected.val = asset.key;
            setDraft(asset);
            await reloadRows();
            uploadProgressStatus.val = "Uploaded";
        } catch (e) {
            uploadProgressStatus.val = "Upload failed";
            error.val = e.message;
        } finally {
            uploadSaving.val = false;
        }
    };

    const saveAsset = async () => {
        const key = draftKey.val.trim();
        if (!key) { error.val = "Asset key is required"; return; }
        try {
            error.val = null;
            const asset = await capi.postV1AssetsSet({
                key,
                format: "text",
                blob: encoder.encode(draftContent.val),
            });
            selected.val = asset.key;
            setDraft(asset);
            await reloadRows();
        } catch (e) {
            error.val = e.message;
        }
    };

    const deleteAsset = async (key) => {
        try {
            error.val = null;
            await capi.postV1AssetsDelete({key});
            if (selected.val === key) clearDraft();
            await reloadRows();
        } catch (e) {
            error.val = e.message;
        }
    };

    const filteredRows = () => {
        if (!rows.val) return rows.val;
        const query = search.val.trim().toLowerCase();
        if (!query) return rows.val;
        return rows.val.filter(row =>
            row.key.toLowerCase().includes(query));
    };

    const uploadProgressText = () => {
        const total = uploadProgressTotal.val || 0;
        const loaded = Math.min(uploadProgressLoaded.val, total || uploadProgressLoaded.val);
        return `${uploadProgressStatus.val} ${uploadProgressName.val}: ${loaded.toLocaleString()} / ${total.toLocaleString()} bytes`;
    };

    const uploadProgressPct = () => {
        const total = uploadProgressTotal.val || 0;
        if (!total) return "0%";
        return `${Math.min(100, Math.round((uploadProgressLoaded.val / total) * 100))}%`;
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
        () => uploadProgressName.val ? div({class: "flex flex-col gap-1 rounded-lg border border-gray-800 bg-surface/60 p-2"},
            p({class: "text-xs text-gray-400"}, uploadProgressText),
            div({class: "h-1.5 overflow-hidden rounded-full bg-gray-800"},
                div({class: "h-full rounded-full bg-brand transition-all", style: () => `width:${uploadProgressPct()}`})),
        ) : "",
        div({class: "flex-1 min-h-0 overflow-auto"}, () => {
            if (rows.val === null) return p({class: "text-gray-400 text-sm"}, "Loading...");
            const visibleRows = filteredRows();
            if (rows.val.length === 0) return p({class: "text-gray-400 text-sm"}, "No assets yet. Click Add asset.");
            if (visibleRows.length === 0) return p({class: "text-gray-400 text-sm"}, "No assets match your search.");
            return table(
                {class: "w-full text-sm"},
                thead(tr({class: "text-left text-gray-400 border-b border-gray-700"},
                    th({class: "pb-2 pr-3 font-medium"}, "Key"),
                    th({class: "pb-2 pr-3 font-medium"}, "Version"),
                    th({class: "pb-2 pr-3 font-medium"}, "Size"),
                    th({class: "pb-2 w-px"}, ""))),
                tbody(...visibleRows.map(row => tr(
                    {
                        class: () => `border-b border-gray-800 last:border-0 align-middle cursor-pointer ${
                            selected.val === row.key ? "bg-surface-hover" : "hover:bg-surface-hover"}`,
                        onclick: () => loadAsset(row.key),
                    },
                    td({class: "py-2 pr-3 min-w-0"},
                        div({class: "font-mono text-gray-100 truncate"}, row.key)),
                    td({class: "py-2 pr-3 text-gray-300"}, `v${row.version}`),
                    td({class: "py-2 pr-3 text-gray-400 whitespace-nowrap"}, fmtSize(row.sizeBytes || 0)),
                    td({class: "py-2 pl-2 text-right whitespace-nowrap w-px"},
                        button({
                            type: "button",
                            class: "p-1.5 rounded text-gray-400 hover:text-red-400 hover:bg-surface transition-colors cursor-pointer",
                            onclick: async (e) => { e.stopPropagation(); await deleteAsset(row.key); },
                        }, trashIcon())),
                ))),
            );
        }),
    );

    const editorPanel = () => div(
        {class: "card flex-1 min-w-0 min-h-0 self-stretch flex flex-col gap-4"},
        div({class: "flex items-start justify-between gap-3"},
            div({class: "min-w-0"},
                () => draftVersion.val ? h2({class: "text-base font-semibold"}, `Editing ${draftKey.val}`) : "",
                () => draftVersion.val
                    ? p({class: "text-xs text-gray-400"}, `Version ${draftVersion.val} created ${fmtDate(draftCreatedAt.val)}. Saving creates version ${draftVersion.val + 1}.`)
                    : ""),
            div({class: "flex items-center gap-2"},
                span({class: "text-xs text-gray-500 whitespace-nowrap"}, () => loadingAsset.val ? "Loading..." : ""),
                button({
                    type: "button",
                    title: "Close editor",
                    class: "p-1.5 rounded text-gray-400 hover:text-gray-100 hover:bg-surface transition-colors cursor-pointer",
                    onclick: clearDraft,
                }, closeIcon()))),
        div({class: "grid grid-cols-1 gap-3"},
            labelField("Key", input({
                class: "text-input font-mono",
                placeholder: "nginx.conf",
                value: draftKey,
                oninput: (e) => draftKey.val = e.target.value,
            }))),
        () => draftLarge.val ? div(
            {class: "text-input flex-1 min-h-0 flex items-center justify-center text-center text-sm text-gray-400"},
            div(
                p({class: "font-medium text-gray-300"}, "This asset is too large to show."),
                p({class: "text-xs text-gray-500 mt-1"}, "Large asset content is stored externally and is still available for deployment mounts."),
            ),
        ) : textarea({
            class: "text-input font-mono text-sm flex-1 min-h-0 resize-none leading-relaxed",
            spellcheck: "false",
            placeholder: "Paste config file contents here",
            value: draftContent,
            oninput: (e) => draftContent.val = e.target.value,
        }),
        div({class: "flex items-center justify-between gap-3"},
            p({class: "text-xs text-gray-500"}, () => draftLarge.val
                ? `${fmtSize(draftSizeBytes.val)} stored externally`
                : `${encoder.encode(draftContent.val).length} bytes inline`),
            div({class: "flex items-center gap-2"},
                smallBtn("Discard", () => {
                    if (original.key) {
                        draftKey.val = original.key;
                        draftContent.val = original.content;
                    } else {
                        clearDraft();
                    }
                }, "bg-gray-700 text-gray-200 hover:bg-gray-600", () => !isDirty()),
                smallBtn("Save new version", saveAsset, "bg-brand text-white hover:bg-blue-600",
                    () => !draftKey.val.trim() || !isDirty())),
        ),
    );

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
            await uploadAsset(file);
        },
    });

    return div(
        {class: "h-full min-h-0 overflow-hidden p-3 flex flex-col gap-3"},
        () => error.val ? p({class: "text-red-400"}, `Error: ${error.val}`) : "",
        uploadPicker,
        div({class: "flex-1 flex flex-col lg:flex-row gap-3 min-h-0"}, leftPane, () => selected.val === null ? "" : editorPanel()),
    );
}

function labelField(label, child) {
    return div({class: "flex flex-col gap-1"},
        span({class: "text-xs text-gray-400"}, label),
        child,
    );
}
