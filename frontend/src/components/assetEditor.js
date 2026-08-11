import van from "vanjs-core";
import {closeIcon} from "../lib/icons.js";

const {button, div, h2, input, p, span, textarea} = van.tags;
const encoder = new TextEncoder();
const decoder = new TextDecoder();
const utf8Decoder = new TextDecoder("utf-8", {fatal: true});

let codeEditorLoader;

const loadCodeEditor = () => {
    codeEditorLoader ||= import("./assetCodeEditor.js")
        .then(module => module.assetCodeEditor);
    return codeEditorLoader;
};

export function preloadAssetCodeEditor() {
    const preload = () => { void loadCodeEditor().catch(() => {}); };
    if (typeof requestIdleCallback === "function") requestIdleCallback(preload, {timeout: 2000});
    else setTimeout(preload, 0);
}

function lazyCodeEditor(args) {
    const editor = van.state("");
    const loadError = van.state("");
    loadCodeEditor()
        .then(codeEditor => { editor.val = codeEditor(args); })
        .catch(error => { loadError.val = error.message || "Unable to load editor"; });

    return div(
        {class: "flex-1 min-h-0"},
        () => editor.val || p({class: "p-4 text-sm text-red-400"}, () => loadError.val || "Loading editor..."),
    );
}

const decodeContent = (blob) => {
    const bytes = blob || new Uint8Array();
    try {
        return {content: utf8Decoder.decode(bytes), binary: false};
    } catch (_) {
        return {content: decoder.decode(bytes), binary: true};
    }
};

const formatDate = value => value instanceof Date && !Number.isNaN(value.getTime()) ? value.toLocaleString() : "";
const formatSize = (size) => {
    if (!size) return "0 B";
    if (size < 1000) return `${size} B`;
    if (size < 1000 * 1000) return `${(size / 1000).toFixed(1)} KB`;
    return `${(size / 1000 / 1000).toFixed(2)} MB`;
};
const normalizeValue = value => value.replace(/\r\n?/g, "\n");
const isYamlAsset = key => /\.ya?ml$/i.test(key || "");

export function assetEditor({
    mode = "edit",
    assetRef = null,          // {assetId, version}; version 0 = latest
    initialKey = "",
    latestVersion = 0,
    spaceId = 0,
    location = "",            // create mode: the target folder path, display only
    locationNode = null,      // create mode: replaces location when the caller
                              // wants the destination to be editable
    loadAsset,
    createAsset,              // ({key, spaceId, blob}) => AssetVersion
    saveVersion,              // ({assetId, blob}) => AssetVersion
    onSaved,
    onClose,
    class: className = "card flex-1 min-w-0 min-h-0 self-stretch flex flex-col gap-4",
}) {
    const creating = mode === "create";
    const readOnly = mode === "read";
    const loading = van.state(!creating);
    const saving = van.state(false);
    const error = van.state("");
    const key = van.state(initialKey);
    const content = van.state("");
    const assetId = van.state(Number(assetRef?.assetId || 0));
    const version = van.state(0);
    const currentLatestVersion = van.state(Number(latestVersion || 0));
    const createdAt = van.state(null);
    const large = van.state(false);
    const binary = van.state(false);
    const sizeBytes = van.state(0);
    const originalRevision = van.state(0);
    let originalContent = "";

    const hydrate = (asset) => {
        const isLarge = Boolean(asset?.location);
        const decoded = isLarge ? {content: "", binary: false} : decodeContent(asset?.blob);
        key.val = asset?.key || key.val;
        content.val = decoded.content;
        assetId.val = Number(asset?.assetId || assetId.val || 0);
        version.val = Number(asset?.version || 0);
        currentLatestVersion.val = Math.max(currentLatestVersion.val, version.val);
        createdAt.val = asset?.createdAt || null;
        large.val = isLarge;
        binary.val = decoded.binary;
        sizeBytes.val = Number(asset?.sizeBytes || asset?.blob?.length || 0);
        originalContent = decoded.content;
        originalRevision.val += 1;
    };

    if (!creating) {
        void (async () => {
            try {
                if (typeof loadAsset !== "function") throw new Error("Asset loading is unavailable");
                hydrate(await loadAsset({assetId: Number(assetRef?.assetId || 0), version: Number(assetRef?.version || 0)}));
            } catch (e) {
                error.val = e.message || "Failed to load asset";
            } finally {
                loading.val = false;
            }
        })();
    }

    const isDirty = () => {
        originalRevision.val;
        return normalizeValue(content.val) !== normalizeValue(originalContent);
    };

    const save = async () => {
        const assetKey = key.val.trim();
        if (!assetKey) { error.val = "Asset name is required"; return; }
        if (creating && !content.val) { error.val = "Asset content cannot be empty."; return; }
        if (saving.val || readOnly || binary.val || large.val || (!creating && !isDirty())) return;
        try {
            saving.val = true;
            error.val = "";
            // The first save of a new asset creates it; every later save
            // appends a version against the stable asset id.
            let saved;
            if (assetId.val) {
                if (typeof saveVersion !== "function") throw new Error("Asset saving is unavailable");
                saved = await saveVersion({assetId: assetId.val, blob: encoder.encode(content.val)});
            } else {
                if (typeof createAsset !== "function") throw new Error("Asset creation is unavailable");
                saved = await createAsset({
                    key: assetKey,
                    blob: encoder.encode(content.val),
                    spaceId: Number(spaceId || 0),
                });
            }
            hydrate(saved);
            if (onSaved) await onSaved(saved);
        } catch (e) {
            error.val = e.message || "Failed to save asset";
        } finally {
            saving.val = false;
        }
    };

    const contentSurface = () => {
        if (loading.val) return div({class: "flex flex-1 items-center justify-center text-sm text-gray-400"}, "Loading asset content...");
        if (error.val && !version.val && !creating) {
            return div({class: "flex flex-1 items-center justify-center p-6"}, p({class: "text-sm text-red-400"}, error));
        }
        if (large.val) return div(
            {class: "flex flex-1 min-h-0 items-center justify-center text-center text-sm text-gray-400"},
            div(
                p({class: "font-medium text-gray-300"}, "This asset is too large to show."),
                p({class: "mt-1 text-xs text-gray-500"}, "The full content remains available for deployment mounts."),
            ),
        );
        if (binary.val) return textarea({
            class: "min-h-0 flex-1 resize-none bg-gray-900 px-3 py-2 font-mono text-sm leading-[1.625] text-gray-100 outline-none",
            readOnly: true,
            spellcheck: "false",
            value: content,
            "aria-label": `Binary content for asset ${key.val}`,
        });
        return lazyCodeEditor({
            value: content,
            disabled: () => readOnly || saving.val,
            ariaLabel: `Content for asset ${key.val || "new asset"}`,
            yamlSyntax: isYamlAsset(key.val),
            bare: true,
        });
    };

    return div(
        {class: className},
        div({class: "flex min-w-0 items-center gap-3"},
            creating && locationNode
                ? locationNode
                : creating && location
                    ? span({class: "shrink-0 max-w-56 truncate font-mono text-xs text-gray-500", title: location}, location)
                    : "",
            () => creating ? input({
                class: "min-w-0 flex-1 rounded border border-transparent bg-transparent px-2 py-1 font-mono text-sm font-normal text-asset focus:border-brand focus:outline-none",
                placeholder: "asset name",
                value: key,
                disabled: saving,
                oninput: event => key.val = event.target.value,
                "aria-label": "New asset name",
            }) : h2({class: "min-w-0 flex-1 truncate px-2 py-1 font-mono text-sm font-normal text-asset"}, key),
            () => version.val ? span({class: "text-xs text-gray-400 whitespace-nowrap"},
                `Version ${version.val} created ${formatDate(createdAt.val)}.`) : "",
            () => loading.val ? span({class: "text-xs text-gray-500 whitespace-nowrap"}, "Loading...") : "",
            onClose ? button({
                type: "button",
                title: "Close editor",
                "aria-label": "Close editor",
                disabled: saving,
                class: "p-1.5 rounded text-gray-400 hover:text-gray-100 hover:bg-surface transition-colors cursor-pointer disabled:cursor-not-allowed disabled:opacity-50",
                onclick: onClose,
            }, closeIcon()) : "",
        ),
        contentSurface,
        div({class: "flex shrink-0 items-center justify-between gap-3"},
            p({class: "text-xs text-gray-500"}, () => large.val
                ? `${formatSize(sizeBytes.val)} large asset`
                : binary.val
                    ? `${formatSize(sizeBytes.val)} non-UTF-8 asset`
                    : `${encoder.encode(content.val).length} bytes inline`),
            readOnly ? "" : div({class: "flex items-center gap-3"},
                () => error.val ? p({class: "text-xs text-red-400"}, error) : "",
                () => creating && !content.val ? p({class: "text-xs text-orange-300"}, "Asset content cannot be empty.") : "",
                button({
                    type: "button",
                    disabled: () => saving.val || binary.val || large.val || !key.val.trim()
                        || (creating ? !content.val : !isDirty()),
                    class: () => `rounded-lg px-3 py-1.5 text-sm font-medium transition-colors ${saving.val
                        || binary.val || large.val || !key.val.trim() || (creating ? !content.val : !isDirty())
                        ? "cursor-not-allowed bg-gray-700 text-gray-400 opacity-50"
                        : "cursor-pointer bg-brand text-white hover:bg-blue-600"}`,
                    onclick: () => { void save(); },
                }, () => saving.val ? "Saving..." : creating
                    ? "Create asset"
                    : `Save version ${currentLatestVersion.val + 1}`),
            ),
        ),
    );
}

export function assetEditorOverlay(args) {
    const close = args.onClose;
    return div(
        div({class: "fixed inset-0 z-[60] bg-black/75", onclick: close}),
        div(
            {class: "fixed inset-0 z-[70] flex items-center justify-center p-3 md:p-6 pointer-events-none"},
            div(
                {
                    class: "w-full h-full max-w-5xl max-h-[85vh] pointer-events-auto",
                    role: "dialog",
                    "aria-modal": "true",
                    onclick: event => event.stopPropagation(),
                },
                assetEditor({
                    ...args,
                    class: "h-full min-w-0 min-h-0 rounded-xl border border-gray-700 bg-gray-900 p-4 shadow-2xl flex flex-col gap-4 overflow-hidden",
                }),
            ),
        ),
    );
}
