import van from "vanjs-core";
import {capi} from "../capi/index.js";

const { div, h2, p, span, input, button, table, thead, tbody, tr, th, td, textarea } = van.tags;
const { svg, path, line } = van.tags("http://www.w3.org/2000/svg");

const encoder = new TextEncoder();
const decoder = new TextDecoder();

const svgBase = {
    viewBox: "0 0 24 24", fill: "none", stroke: "currentColor",
    "stroke-width": "2", "stroke-linecap": "round", "stroke-linejoin": "round",
    class: "w-4 h-4",
};

const plusIcon = () => svg(svgBase, line({x1: "12", y1: "5", x2: "12", y2: "19"}), line({x1: "5", y1: "12", x2: "19", y2: "12"}));
const closeIcon = () => svg(svgBase, line({x1: "18", y1: "6", x2: "6", y2: "18"}), line({x1: "6", y1: "6", x2: "18", y2: "18"}));
const trashIcon = () => svg(svgBase,
    path({d: "M3 6h18"}),
    path({d: "M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"}),
    path({d: "M10 11v6M14 11v6"}),
    path({d: "M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"}));

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

export function assetsPage() {
    const rows = van.state(null);
    const error = van.state(null);
    const search = van.state("");
    const selected = van.state(null);
    const loadingAsset = van.state(false);

    const draftKey = van.state("");
    const draftFormat = van.state("text");
    const draftContent = van.state("");
    const draftVersion = van.state(0);
    const draftCreatedAt = van.state(null);
    const original = {key: "", format: "", content: "", version: 0};

    const setOriginal = (asset) => {
        original.key = asset.key || "";
        original.format = asset.format || "text";
        original.content = decoder.decode(asset.blob || new Uint8Array());
        original.version = asset.version || 0;
    };

    const setDraft = (asset) => {
        setOriginal(asset);
        draftKey.val = original.key;
        draftFormat.val = original.format;
        draftContent.val = original.content;
        draftVersion.val = original.version;
        draftCreatedAt.val = asset.createdAt || null;
    };

    const clearDraft = () => {
        selected.val = null;
        original.key = "";
        original.format = "text";
        original.content = "";
        original.version = 0;
        draftKey.val = "";
        draftFormat.val = "text";
        draftContent.val = "";
        draftVersion.val = 0;
        draftCreatedAt.val = null;
    };

    const isDirty = () => draftKey.val.trim() !== original.key
        || draftFormat.val.trim() !== original.format
        || draftContent.val !== original.content;

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

    const saveAsset = async () => {
        const key = draftKey.val.trim();
        if (!key) { error.val = "Asset key is required"; return; }
        try {
            error.val = null;
            const asset = await capi.postV1AssetsSet({
                key,
                format: draftFormat.val.trim() || "text",
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
            row.key.toLowerCase().includes(query) ||
            (row.format || "").toLowerCase().includes(query));
    };

    const listPanel = () => div(
        {class: "card flex-1 flex flex-col gap-3 min-w-0 min-h-0"},
        div({class: "flex flex-wrap items-center justify-between gap-3"},
            p({class: "text-xs text-gray-400"},
                "Assets are immutable config files for future read-only container mounts. Saving creates a new version."),
            div({class: "flex items-center gap-2"},
                input({
                    class: "text-input w-64",
                    type: "search",
                    placeholder: "Search assets",
                    value: search,
                    oninput: (e) => search.val = e.target.value,
                }),
                button({
                    type: "button",
                    class: "flex items-center gap-1 whitespace-nowrap text-sm px-3 py-1.5 rounded-lg bg-gray-700 " +
                        "text-gray-200 hover:bg-gray-600 transition-colors cursor-pointer",
                    onclick: addAsset,
                }, plusIcon(), "Add asset"))),
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
                        div({class: "font-mono text-gray-100 truncate"}, row.key),
                        div({class: "text-xs text-gray-500 truncate"}, row.format || "text")),
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
                h2({class: "text-base font-semibold"}, () => draftVersion.val ? `Editing ${draftKey.val}` : "New asset"),
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
        div({class: "grid grid-cols-1 md:grid-cols-[1fr_12rem] gap-3"},
            labelField("Key", input({
                class: "text-input font-mono",
                placeholder: "nginx.conf",
                value: draftKey,
                oninput: (e) => draftKey.val = e.target.value,
            })),
            labelField("Format", input({
                class: "text-input font-mono",
                placeholder: "text",
                value: draftFormat,
                oninput: (e) => draftFormat.val = e.target.value,
            }))),
        textarea({
            class: "text-input font-mono text-sm flex-1 min-h-0 resize-none leading-relaxed",
            spellcheck: "false",
            placeholder: "Paste config file contents here",
            value: draftContent,
            oninput: (e) => draftContent.val = e.target.value,
        }),
        div({class: "flex items-center justify-between gap-3"},
            p({class: "text-xs text-gray-500"}, () => `${encoder.encode(draftContent.val).length} bytes inline`),
            div({class: "flex items-center gap-2"},
                smallBtn("Discard", () => {
                    if (original.key) {
                        draftKey.val = original.key;
                        draftFormat.val = original.format;
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

    return div(
        {class: "h-full min-h-0 overflow-hidden p-3 flex flex-col gap-3"},
        () => error.val ? p({class: "text-red-400"}, `Error: ${error.val}`) : "",
        div({class: "flex-1 flex flex-col lg:flex-row gap-3 min-h-0"}, leftPane, () => selected.val === null ? "" : editorPanel()),
    );
}

function labelField(label, child) {
    return div({class: "flex flex-col gap-1"},
        span({class: "text-xs text-gray-400"}, label),
        child,
    );
}
