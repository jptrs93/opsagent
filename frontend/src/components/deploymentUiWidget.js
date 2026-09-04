import van from "vanjs-core";
import {assetPreviewOverlay} from "./assetPreviewOverlay.js";
import {
    assetMountEditorOverlay,
    assetMountsPane,
    collapsibleSection,
    commandPane,
    deploymentForm,
    envVarsPane,
    networkingPane,
    resourcesPane,
    upgradeStrategyPane,
    volumeMountsPane,
} from "./deploymentForm.js";

const {div, span, button} = van.tags;

const stateValue = (value) => value && typeof value === 'object' && 'val' in value ? value.val : value;

export function deploymentUiWidget(args) {
    const {
        mode,
        form,
        deploymentRow,
        deploymentUpdate,
        canEditState,
        spaces,
        nodes,
        nodesLoaded,
        assets,
        secretRefs,
        configRefs,
        deployments,
        loadAsset,
        createAsset,
        saveVersion,
    } = args;
    const previewAsset = van.state(null);
    // {mountID, asset} for the mount whose asset is open in the overlay editor.
    const editAssetTarget = van.state(null);

    return div(
        {class: 'flex flex-1 min-h-0 min-w-0'},
        div(
            {class: 'app-scroll flex-1 min-h-0 overflow-auto px-3 py-3.5 flex flex-col gap-[1.125rem]'},
            deploymentForm(form, {
                identityLocked: mode === 'update',
                spaceOptions: spaces,
                nodeOptions: nodes,
                nodeOptionsLoaded: nodesLoaded,
                assets: stateValue(assets) || [],
                enableAssetEditor: true,
                showRunnerSummary: mode !== 'create',
                sourceController: deploymentUpdate,
                deployments: stateValue(deployments) || [],
            }),
            () => mode === 'create' || deploymentRow?.variant ? versionSummarySection({form, deploymentUpdate}) : '',
            canEditState ? deploymentStateSection(form, deploymentUpdate) : '',
        ),
        envVarsPane(form, {
            assets,
            secretRefs,
            configRefs,
            deployments,
            spaces,
            deploymentId: mode === 'update' ? deploymentRow?.id : null,
            previewAsset: asset => { previewAsset.val = asset; },
        }),
        commandPane(form),
        volumeMountsPane(form, {deployments, spaces}),
        assetMountsPane(form, {
            assets,
            spaces,
            enableAssetEditor: true,
            previewAsset: asset => { previewAsset.val = asset; },
            editAsset: target => { editAssetTarget.val = target; },
        }),
        upgradeStrategyPane(form),
        resourcesPane(form),
        networkingPane(form),
        () => editAssetTarget.val
            ? assetMountEditorOverlay(form, editAssetTarget.val, {
                assets,
                loadAsset,
                createAsset,
                saveVersion,
                onClose: () => { editAssetTarget.val = null; },
            })
            : '',
        () => previewAsset.val
            ? assetPreviewOverlay(previewAsset.val, loadAsset, () => { previewAsset.val = null; })
            : '',
    );
}

export function deploymentUiHasOpenPane(form) {
    return (
        form.envPaneOpen.val
        || form.commandPaneOpen.val
        || form.assetMountsPaneOpen.val
        || form.volumeMountsPaneOpen.val
        || form.upgradeStrategyPaneOpen.val
        || form.resourcesPaneOpen.val
        || form.networkingPaneOpen.val
    );
}

// Read-only summary of the selected version; selection itself lives in the
// editor footer. With the version's list entry loaded it carries the same
// context as the footer (branch, short id, message, date) and falls back to
// the bare version otherwise.
function versionSummarySection(args) {
    const update = args.deploymentUpdate;
    const explicitVersion = update.explicitImageVersion();
    const selected = update.selectedTargetVersion();
    const entry = update.versionEntry();
    const branch = update.isImage() ? '' : update.nixDockerBuild.selectedBranch.val;
    const shortID = selected.length > 7 && /^[0-9a-f]+$/i.test(selected) ? selected.slice(0, 7) : selected;
    const date = entry?.time instanceof Date && entry.time.getTime() > 0 ? entry.time.toISOString().slice(0, 10) : '';
    const parts = [];
    if (!selected) {
        parts.push(span({class: 'text-gray-500'}, 'No version selected'));
    } else {
        if (entry && branch) parts.push(span({class: 'text-gray-300'}, branch));
        parts.push(span({class: 'font-mono text-gray-100', title: selected}, shortID));
        if (entry?.label) parts.push(span({class: 'text-gray-300'}, entry.label));
        if (date) parts.push(span({class: 'font-mono text-gray-500'}, date));
        if (explicitVersion) parts.push(span({class: 'text-gray-500'}, explicitVersion.startsWith('sha256:') ? 'digest from the image reference' : 'tag from the image reference'));
    }
    const separated = parts.flatMap((part, index) => index ? [span({class: 'text-gray-600'}, '·'), part] : [part]);
    return collapsibleSection(
        'Version',
        args.form.versionSectionOpen,
        div(
            {class: 'flex flex-wrap items-baseline gap-x-2 gap-y-1 text-xs', 'data-testid': 'deployment-version-summary'},
            ...separated,
            explicitVersion ? '' : span({class: 'ml-1 text-gray-500'}, 'Select or refresh versions in the footer.'),
        ),
    );
}

function deploymentStateSection(form, deploymentUpdate) {
    return collapsibleSection(
        'State',
        form.stateSectionOpen,
        div(
            {class: 'flex items-center gap-2'},
            span({class: 'text-xs text-gray-300'}, () => deploymentUpdate.desiredRunning.val ? 'Running' : 'Stopped'),
            button(
                {
                    type: 'button',
                    role: 'switch',
                    'data-testid': 'deployment-desired-state-toggle',
                    'aria-checked': () => String(deploymentUpdate.desiredRunning.val),
                    'aria-label': () => deploymentUpdate.desiredRunning.val ? 'Set deployment to stopped' : 'Set deployment to running',
                    class: () => `relative h-5 w-9 shrink-0 rounded-full border transition-colors cursor-pointer ${deploymentUpdate.desiredRunning.val ? 'border-blue-500 bg-blue-600' : 'border-gray-600 bg-gray-700'}`,
                    onclick: () => deploymentUpdate.setDesiredRunning(!deploymentUpdate.desiredRunning.val),
                },
                span({class: () => `absolute top-0.5 h-4 w-4 rounded-full bg-white shadow-sm transition-all ${deploymentUpdate.desiredRunning.val ? 'left-4' : 'left-0.5'}`}),
            ),
        ),
    );
}
