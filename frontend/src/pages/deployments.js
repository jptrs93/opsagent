// Deployments page: a top tab bar with the status table pinned as "Overview"
// and one full-page editor tab per deployment being updated, forked,
// restored, or created. Panels stay mounted while hidden so drafts survive
// switching tabs.

import van from "vanjs-core";
import {statusPage} from "./status.js";
import {deploymentEditorWidget} from "../components/deploymentEditorWidget.js";
import {capi} from "../capi/index.js";
import {loadAssetPreview, uploadAsset} from "../lib/assetContent.js";
import {
    assetMetasS,
    deploymentsS,
    nodesS,
    secretRefsS,
    spacesS,
    userConfigRefsS,
} from "../state/deployments.js";
import {closeIcon, editIcon, plusIcon} from "../lib/icons.js";
import {spaceHue} from "../lib/valueExplorer.js";

const {button, div, span} = van.tags;

const OVERVIEW = "overview";

const defaultEditorActions = () => ({
    validateSource: request => capi.postV1ReposValidate(request),
    loadDeploymentVersions: request => capi.postV1DeploymentsVersions(request),
    loadAsset: loadAssetPreview,
    createAsset: request => uploadAsset({key: request.key, space_id: Number(request.spaceId || 0)}, request.blob),
    saveVersion: request => uploadAsset({asset_id: Number(request.assetId)}, request.blob),
    createDeployment: request => capi.postV1DeploymentsCreate(request),
    updateDeployment: request => capi.postV2DeploymentsUpdate(request),
});

// deploymentsPage(onOpenLogs, {actions}) — actions override the editor's API
// calls (the fixture passes in-memory ones); everything else reads the live
// state stores.
export function deploymentsPage(onOpenLogs = () => {}, options = {}) {
    const actions = {...defaultEditorActions(), ...(options.actions || {})};
    const catalogs = () => ({
        spaces: spacesS,
        nodes: nodesS,
        nodesLoaded: true,
        deployments: deploymentsS,
        assets: assetMetasS,
        secretRefs: secretRefsS,
        configRefs: userConfigRefsS,
    });

    const tabs = van.state([]);
    const activeTab = van.state(OVERVIEW);
    const panelsHost = div({class: "relative flex-1 min-h-0 min-w-0"});
    let nextCreateTab = 1;

    const mountPanel = (id, content) => {
        const panel = div({class: () => `h-full min-h-0 min-w-0 ${activeTab.val === id ? "" : "hidden"}`}, content);
        panelsHost.append(panel);
        return panel;
    };

    const removeTab = tab => {
        const index = tabs.val.indexOf(tab);
        tab.panel.remove();
        tabs.val = tabs.val.filter(item => item !== tab);
        if (activeTab.val === tab.id) {
            const neighbour = tabs.val[index] || tabs.val[index - 1];
            activeTab.val = neighbour ? neighbour.id : OVERVIEW;
        }
    };

    // closeTab asks before dropping unsaved edits; the editor's own Cancel
    // and a successful save close without asking.
    const closeTab = (id, {confirmDirty = true} = {}) => {
        const tab = tabs.val.find(item => item.id === id);
        if (!tab) return;
        if (confirmDirty && tab.dirty.val && !window.confirm(`Discard unsaved changes to ${tab.title}?`)) return;
        removeTab(tab);
    };

    const openEditTab = (row, rawConfig) => {
        const id = `edit-${row.id}`;
        if (tabs.val.some(item => item.id === id)) {
            activeTab.val = id;
            return;
        }
        if (!rawConfig) return;
        const dirty = van.state(false);
        const editor = deploymentEditorWidget({
            mode: "update",
            layout: "page",
            deploymentRow: row,
            deployment: rawConfig,
            catalogs: catalogs(),
            actions,
            dirty,
            onCancel: () => closeTab(id, {confirmDirty: false}),
            onSuccess: () => closeTab(id, {confirmDirty: false}),
        });
        const panel = mountPanel(id, editor);
        tabs.val = [...tabs.val, {id, title: row.name, spaceId: row.spaceId, dirty, panel}];
        activeTab.val = id;
    };

    // Add deployment, Fork, and restoring a recently deleted deployment all
    // land here with the status page's create options. Each opens its own
    // tab; there is nothing to de-duplicate on.
    const openCreateTab = (opts = {}) => {
        const id = `create-${nextCreateTab++}`;
        const sourceName = opts.sourceDeployment?.def?.name || opts.sourceDeploymentRow?.name || "";
        const title = opts.retainIdentity && sourceName ? `Restore ${sourceName}`
            : sourceName ? `Fork of ${sourceName}`
                : "New deployment";
        const dirty = van.state(false);
        const editor = deploymentEditorWidget({
            mode: "create",
            layout: "page",
            deploymentRow: opts.sourceDeploymentRow || null,
            deployment: opts.sourceDeployment || null,
            fork: Boolean(opts.sourceDeployment),
            retainIdentity: Boolean(opts.retainIdentity),
            catalogs: catalogs(),
            actions,
            dirty,
            onCancel: () => closeTab(id, {confirmDirty: false}),
            onSuccess: () => closeTab(id, {confirmDirty: false}),
        });
        const panel = mountPanel(id, editor);
        tabs.val = [...tabs.val, {id, title, spaceId: null, create: true, dirty, panel}];
        activeTab.val = id;
    };

    const spaceDot = spaceId => span({
        class: "inline-block w-[7px] h-[7px] rounded-full flex-none",
        style: `background:${spaceHue(spaceId)}`,
    });

    // panelTone is the active tab's background: it must match the panel it
    // sits on so the tab reads as attached to it. The Overview page is
    // surface-toned, the editor is gray-900.
    const tabButton = ({id, title, pinned = false, spaceId = null, panelTone = "bg-surface", create = false, dirty = null}) => {
        const active = () => activeTab.val === id;
        return div(
            {
                role: "tab",
                "aria-selected": () => String(active()),
                "data-testid": `deployments-tab-${id}`,
                tabindex: 0,
                class: () => `group relative -mb-px flex h-8 max-w-[16rem] cursor-pointer select-none items-center gap-1.5 rounded-t-md border border-b-0 px-3 text-xs transition-colors ${pinned ? "shrink-0" : "min-w-0 shrink"} ` + (active()
                    ? `border-gray-700 ${panelTone} text-white`
                    : "border-transparent text-gray-400 hover:bg-white/5 hover:text-gray-200"),
                onclick: () => { activeTab.val = id; },
                onkeydown: event => {
                    if (event.key === "Enter" || event.key === " ") { event.preventDefault(); activeTab.val = id; }
                    if (!pinned && (event.key === "Delete" || event.key === "Backspace")) closeTab(id);
                },
                onauxclick: event => {
                    if (!pinned && event.button === 1) { event.preventDefault(); closeTab(id); }
                },
            },
            pinned ? "" : (create ? plusIcon : editIcon)({class: () => `w-3 h-3 flex-none ${active() ? "text-brand" : "text-gray-500"}`}),
            pinned || spaceId === null ? "" : spaceDot(spaceId),
            span({class: `truncate ${pinned || create ? "font-medium" : "font-mono"}`}, title),
            dirty ? span({
                class: () => dirty.val ? "inline-block h-1.5 w-1.5 flex-none rounded-full bg-amber-300" : "hidden",
                title: "Unsaved changes",
                "data-testid": "deployments-tab-dirty",
            }) : "",
            pinned ? "" : button({
                type: "button",
                "aria-label": `Close ${title}`,
                title: "Close tab",
                class: () => "ml-1 -mr-1.5 inline-flex h-4 w-4 flex-none items-center justify-center rounded transition-colors " + (active()
                    ? "text-gray-400 hover:bg-white/10 hover:text-white"
                    : "text-transparent group-hover:text-gray-500 hover:!text-gray-100 hover:bg-white/10"),
                onclick: event => { event.stopPropagation(); closeTab(id); },
            }, closeIcon({class: "w-3 h-3"})),
        );
    };

    const tabStrip = div(
        // Fixed height and no overflow handling: an overflow-x strip would
        // also start scrolling vertically over the active tab's 1px border
        // overlap. Crowded tabs shrink and truncate instead of scrolling.
        {class: "flex h-[34px] flex-none flex-nowrap items-end gap-0.5 border-b border-gray-700 bg-gray-900/80 px-2 pt-0.5", role: "tablist", "aria-label": "Deployments"},
        tabButton({id: OVERVIEW, title: "Overview", pinned: true}),
        () => div({class: "contents"}, ...tabs.val.map(tab => tabButton({
            id: tab.id,
            title: tab.title,
            spaceId: tab.spaceId,
            create: Boolean(tab.create),
            dirty: tab.dirty,
            panelTone: "bg-gray-900",
        }))),
    );

    mountPanel(OVERVIEW, statusPage(onOpenLogs, {onUpdate: openEditTab, onCreate: openCreateTab}));

    return div(
        {class: "flex h-full min-h-0 min-w-0 flex-col bg-surface", "data-testid": "deployments-page"},
        tabStrip,
        panelsHost,
    );
}
