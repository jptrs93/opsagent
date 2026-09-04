// Editor footer widgets for source validation and version selection, bound to
// DeploymentCreationUpdate: a status pill opening the per-layer panel with a
// Validate action, and a version button opening a filterable dropdown (with a
// branch select for Nix) beside a refresh button. Both surfaces share them;
// in Code mode a pick is also written into the HCL text by the caller.

import van from "vanjs-core";
import {checkIcon, chevronDownIcon, refreshIcon, searchIcon} from "../lib/icons.js";
import {LAYER_NAMES} from "./deploymentCreationUpdate.js";

const {button, div, input, option, p, select, span} = van.tags;

const shortID = id => (id && id.length > 7 && /^[0-9a-f]+$/i.test(id) ? id.slice(0, 7) : id || "");
const dateOf = version => (version?.time instanceof Date && version.time.getTime() > 0 ? version.time.toISOString().slice(0, 10) : "");

export const STATUS_DOT = {
    trusted: "bg-emerald-400",
    ok: "bg-emerald-400",
    checking: "bg-blue-400 animate-pulse",
    unvalidated: "bg-gray-500",
    error: "bg-red-400",
};
export const STATUS_TEXT = {
    trusted: "text-emerald-300",
    ok: "text-emerald-300",
    checking: "text-blue-300",
    unvalidated: "text-gray-400",
    error: "text-red-300",
};
const OVERALL_LABEL = {
    trusted: "Source unchanged",
    ok: "Source valid",
    checking: "Validating...",
    unvalidated: "Source not validated",
    error: "Source invalid",
};

const footerButtonClass = (extra = "") => `inline-flex h-[30px] max-w-[26rem] items-center gap-2 rounded-md border border-gray-700 bg-gray-900 px-2.5 text-xs text-gray-200 transition-colors hover:border-gray-600 hover:bg-gray-800 disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:bg-gray-900 cursor-pointer ${extra}`;

// selectionLabel describes the selected version with whatever context the
// loaded lists add: `branch · sha7 · message` or `tag · date`, else the bare
// version, else nothing.
export function selectionLabel(model) {
    const id = model.selectedTargetVersion();
    if (model.explicitImageVersion()) return `${id} · from image reference`;
    if (!id) return "";
    const entry = model.versionEntry();
    if (model.isImage()) return entry ? [entry.id, dateOf(entry)].filter(Boolean).join(" · ") : id;
    return entry
        ? [model.nixDockerBuild.selectedBranch.val, shortID(id), entry.label].filter(Boolean).join(" · ")
        : shortID(id);
}

// sourceFooterWidgets returns [sourceWidget, versionWidget, backdrop].
// onSelectVersion(id) runs after a dropdown pick reaches the model.
export function sourceFooterWidgets({deploymentUpdate: model, onSelectVersion}) {
    const openPanel = van.state(null);
    const filter = van.state("");
    const toggle = name => {
        openPanel.val = openPanel.val === name ? null : name;
        if (openPanel.val === "version") {
            filter.val = "";
            void model.ensureVersionsLoaded();
        }
    };
    const close = () => { openPanel.val = null; };
    const overall = () => model.overallStatus();
    const versionLocked = () => Boolean(model.explicitImageVersion());

    const dot = status => span({class: `inline-block h-2 w-2 flex-none rounded-full ${STATUS_DOT[status] || STATUS_DOT.unvalidated}`});

    const validateButton = () => button({
        type: "button",
        "data-testid": "source-validate-button",
        class: "rounded-md bg-blue-500/15 border border-blue-500/40 px-2.5 py-1 text-[11px] font-medium text-blue-100 hover:bg-blue-500/30 disabled:opacity-40 disabled:cursor-not-allowed cursor-pointer",
        disabled: () => overall() === "checking",
        onclick: () => { void model.validate(); },
    }, () => overall() === "unvalidated" ? "Validate" : "Re-validate");

    const sourcePanel = () => div(
        {class: "absolute bottom-full left-0 z-40 mb-1.5 w-[26rem] rounded-md border border-gray-700 bg-gray-900 p-1 text-xs shadow-[0_12px_40px_rgba(0,0,0,0.55)]", "data-testid": "source-status-panel"},
        ...Object.entries(model.layers.val).map(([name, item]) => div(
            {class: "grid grid-cols-[auto_7rem_minmax(0,1fr)] items-start gap-2 rounded px-2 py-1.5"},
            span({class: "mt-1"}, dot(item.status)),
            span({class: "text-gray-300"}, LAYER_NAMES[name] || name),
            span({class: `break-words ${STATUS_TEXT[item.status] || ""}`}, item.message),
        )),
        div(
            {class: "mt-1 flex items-center justify-between gap-2 border-t border-gray-800 px-2 pt-1.5"},
            span({class: "text-[11px] text-gray-500"}, () => overall() === "trusted"
                ? "Validated when the deployment was saved."
                : "Checks run only when you ask."),
            validateButton(),
        ),
    );

    const sourceWidget = div(
        {class: "relative z-40 flex items-center gap-1.5"},
        button({
            type: "button",
            "data-testid": "source-status-button",
            class: footerButtonClass(),
            "aria-expanded": () => String(openPanel.val === "source"),
            onclick: () => toggle("source"),
        },
            () => dot(overall()),
            span({class: () => STATUS_TEXT[overall()] || ""}, () => OVERALL_LABEL[overall()]),
            chevronDownIcon({class: "h-3 w-3 text-gray-500 rotate-180"}),
        ),
        () => (overall() === "unvalidated" || overall() === "error") ? validateButton() : "",
        () => openPanel.val === "source" ? sourcePanel() : "",
    );

    const rowClass = active => `grid w-full grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 rounded px-2 py-1 text-left cursor-pointer ${active ? "bg-blue-500/15 text-white" : "text-gray-300 hover:bg-white/5 hover:text-white"}`;

    const versionRows = () => {
        const versions = model.versions.val;
        const query = filter.val.trim().toLowerCase();
        const current = model.selectedTargetVersion();
        const deployed = model.deployedVersion();
        if (versions.loading) return [p({class: "px-2 py-2 text-gray-500"}, "Loading versions...")];
        if (versions.error) return [p({class: "px-2 py-2 text-red-300"}, versions.error)];
        const items = model.isImage() ? model.containerImage.tags.val : model.nixDockerBuild.commits.val;
        const rows = items
            .filter(item => !query || `${item.id} ${item.label || ""}`.toLowerCase().includes(query))
            .map(item => button({
                type: "button",
                "data-testid": "version-option",
                "data-version": item.id,
                class: rowClass(item.id === current),
                title: item.id,
                onclick: () => {
                    model.selectVersion(item.id);
                    onSelectVersion?.(item.id);
                    close();
                },
            },
                span({class: "w-4"}, item.id === current ? checkIcon({class: "h-3 w-3 text-blue-300"}) : ""),
                span({class: "flex min-w-0 items-baseline gap-2"},
                    span({class: "font-mono text-gray-100"}, model.isImage() ? item.id : shortID(item.id)),
                    span({class: "truncate text-gray-400"}, item.label || ""),
                    item.id === deployed ? span({class: "rounded bg-emerald-500/15 px-1 text-[10px] text-emerald-300"}, "current") : ""),
                span({class: "font-mono text-[10px] text-gray-500"}, dateOf(item)),
            ));
        if (!rows.length) return [p({class: "px-2 py-2 text-gray-500"}, items.length ? "No matches." : "No versions loaded.")];
        return rows;
    };

    const versionPanel = () => div(
        {class: "absolute bottom-full left-0 z-40 mb-1.5 flex w-[30rem] flex-col rounded-md border border-gray-700 bg-gray-900 text-xs shadow-[0_12px_40px_rgba(0,0,0,0.55)]", "data-testid": "version-select-panel"},
        div(
            {class: "flex items-center gap-2 border-b border-gray-800 p-1.5"},
            model.isNix() ? select({
                class: "input h-7 max-w-[11rem] py-0 text-xs",
                "aria-label": "Branch",
                disabled: () => model.versions.val.loading,
                onchange: event => { void model.selectBranch(event.target.value); },
            }, () => div({class: "contents"}, ...model.nixDockerBuild.branches.val.map(name => option({value: name, selected: name === model.nixDockerBuild.selectedBranch.val}, name)))) : "",
            div({class: "relative flex-1"},
                searchIcon({class: "pointer-events-none absolute left-2 top-1/2 h-3 w-3 -translate-y-1/2 text-gray-500"}),
                input({
                    class: "input search-input-iconed h-7 w-full py-0 text-xs",
                    "data-testid": "version-filter-input",
                    placeholder: model.isNix() ? "Filter commits by sha or message" : "Filter tags",
                    value: filter,
                    oninput: event => { filter.val = event.target.value; },
                })),
        ),
        div({class: "app-scroll max-h-72 overflow-y-auto p-1"}, () => div({class: "contents"}, ...versionRows())),
    );

    const versionWidget = div(
        {class: "relative z-40 flex items-center gap-1"},
        button({
            type: "button",
            "data-testid": "version-select-button",
            class: footerButtonClass(),
            disabled: () => !model.sourceValid() || versionLocked(),
            title: () => versionLocked()
                ? "The image reference pins its own version"
                : (model.sourceValid() ? "" : "Validate the source to load versions"),
            "aria-expanded": () => String(openPanel.val === "version"),
            onclick: () => toggle("version"),
        },
            span({class: "text-gray-500"}, "Version"),
            () => selectionLabel(model)
                ? span({class: "truncate font-mono text-gray-100", "data-testid": "version-selection"}, selectionLabel(model))
                : span({class: "text-gray-500"}, () => model.sourceValid() ? "Select a version" : "Validate source first"),
            chevronDownIcon({class: "h-3 w-3 text-gray-500 rotate-180"}),
        ),
        button({
            type: "button",
            "data-testid": "version-refresh-button",
            title: "Refresh available versions",
            "aria-label": "Refresh available versions",
            class: "inline-flex h-[30px] w-[30px] items-center justify-center rounded-md border border-gray-700 bg-gray-900 text-gray-400 hover:border-gray-600 hover:text-gray-100 disabled:cursor-not-allowed disabled:opacity-40 cursor-pointer",
            disabled: () => !model.sourceValid() || model.versions.val.loading || versionLocked(),
            onclick: () => { void model.refreshVersions(); },
        }, refreshIcon({class: () => `h-3.5 w-3.5 ${model.versions.val.loading ? "animate-spin" : ""}`})),
        () => openPanel.val === "version" ? versionPanel() : "",
    );

    const backdrop = () => openPanel.val ? div({class: "fixed inset-0 z-30", onclick: close}) : "";

    return [sourceWidget, versionWidget, backdrop];
}
