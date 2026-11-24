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
    assetDirectoriesS,
    assetMetasS,
    deploymentsS,
    nodesS,
    secretRefsS,
    spacesS,
    userConfigRefsS,
    valueDirectoriesS,
} from "../state/deployments.js";
import {editIcon, plusIcon} from "../lib/icons.js";
import {tabButton, tabPanel, tabStrip} from "../components/tabs.js";
import {spaceHue} from "../lib/valueExplorer.js";

const {div, span} = van.tags;

const OVERVIEW = "overview";

const defaultEditorActions = () => ({
    validateSource: request => capi.postV1ReposValidate(request),
    loadDeploymentVersions: request => capi.postV1DeploymentsVersions(request),
    loadAsset: loadAssetPreview,
    createAsset: request => uploadAsset({key: request.key, space_id: Number(request.spaceId || 0)}, request.blob),
    saveVersion: request => uploadAsset({asset_id: Number(request.assetId)}, request.blob),
    createDeployment: request => capi.postV1DeploymentsCreate(request),
    updateDeployment: request => capi.postV1DeploymentsUpdate(request),
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
        valueDirectories: valueDirectoriesS,
        assetDirectories: assetDirectoriesS,
    });

    const tabs = van.state([]);
    const activeTab = van.state(OVERVIEW);
    const panelsHost = div({class: "page-body"});
    let nextCreateTab = 1;

    const mountPanel = (id, content) => {
        const panel = tabPanel(activeTab, id, content);
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
        const sourceName = opts.sourceDeployment?.deployment?.name || opts.sourceDeploymentRow?.name || "";
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

    // Fixed height and no overflow handling: an overflow-x strip would also
    // start scrolling vertically over the active tab's 1px border overlap.
    // Crowded tabs shrink and truncate instead of scrolling.
    const strip = tabStrip({label: "Deployments"},
        tabButton({id: OVERVIEW, activeTab, title: "Overview", testid: `deployments-tab-${OVERVIEW}`}),
        () => div({class: "contents"}, ...tabs.val.map(item => tabButton({
            id: item.id,
            activeTab,
            title: item.title,
            testid: `deployments-tab-${item.id}`,
            icon: item.create ? plusIcon : editIcon,
            lead: item.spaceId === null ? null : spaceDot(item.spaceId),
            mono: !item.create,
            deep: true,
            dirty: item.dirty,
            dirtyTestid: "deployments-tab-dirty",
            onClose: () => closeTab(item.id),
        }))),
    );

    mountPanel(OVERVIEW, statusPage(onOpenLogs, {onUpdate: openEditTab, onCreate: openCreateTab}));

    return div(
        {class: "page", "data-testid": "deployments-page"},
        strip,
        panelsHost,
    );
}
