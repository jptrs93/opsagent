import van from "vanjs-core";
import {refreshIcon} from "../lib/icons.js";
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
import {imageVersionFromReference, SOURCE_DOCKER_IMAGE} from "./deploymentSource.js";

const {div, span, select, option, button, label, input} = van.tags;

const stateValue = (value) => value && typeof value === 'object' && 'val' in value ? value.val : value;

export function deploymentConfigUiWidget(args) {
    const {
        mode,
        form,
        deployment,
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
        onRefresh,
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
            () => mode === 'create' || deployment?.variant ? versionSection({
                mode,
                form,
                deployment,
                deploymentUpdate,
                loadingVersions: deploymentUpdate.loadingVersions,
                onRefresh,
            }) : '',
            canEditState ? deploymentStateSection(form, deploymentUpdate) : '',
        ),
        envVarsPane(form, {
            assets,
            secretRefs,
            configRefs,
            deployments,
            spaces,
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

export function deploymentConfigUiHasOpenPane(form) {
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

function versionSection(args) {
    if (args.form.sourceType.val === SOURCE_DOCKER_IMAGE) return imageVersionSection(args);
    return nixVersionSection(args);
}

function imageVersionSection(args) {
    const explicitVersion = imageVersionFromReference(args.form.containerImage.val);
    const status = args.deploymentUpdate.imageStatus();
    const tags = args.deploymentUpdate.containerImage.tags.val;
    const selectedTag = args.deploymentUpdate.containerImage.selectedTag.val;
    const tagOptions = selectedTag && !tags.some(item => item.id === selectedTag)
        ? [{id: selectedTag, label: 'Selected in Code'}, ...tags]
        : tags;
    if (explicitVersion) {
        return collapsibleSection(
            'Version',
            args.form.versionSectionOpen,
            div(
                {class: 'grid grid-cols-1 items-end gap-x-3 gap-y-2'},
                label(
                    {class: 'flex flex-col gap-1 text-xs text-gray-400'},
                    span(explicitVersion.startsWith('sha256:') ? 'Digest from image' : 'Tag from image'),
                    input({class: selectClass(), disabled: true, value: explicitVersion}),
                ),
            ),
        );
    }

    const ready = status.status === 'ok';
    const placeholder = args.loadingVersions.val
        ? 'Loading tags...'
        : (ready && tagOptions.length ? 'Select a tag...' : (ready ? 'No tags loaded' : 'Valid image path needed to load versions'));
    return collapsibleSection(
        'Version',
        args.form.versionSectionOpen,
        div(
            {class: 'flex flex-col gap-2'},
            div(
                {class: 'grid grid-cols-1 items-end gap-x-3 gap-y-2 md:grid-cols-[minmax(0,1fr)_auto]'},
                label(
                    {class: 'flex flex-col gap-1 text-xs text-gray-400'},
                    span('Tag'),
                    select({
                        class: selectClass(),
                        disabled: !ready || args.loadingVersions.val || tagOptions.length === 0
                            || (args.mode === 'update' && !args.deploymentUpdate.desiredRunning.val),
                        value: selectedTag,
                        onchange: e => {
                            args.deploymentUpdate.containerImage.selectedTag.val = e.target.value;
                        },
                    },
                        option({value: '', disabled: true, selected: !selectedTag}, placeholder),
                        ...tagOptions.map(v => option({value: v.id, selected: v.id === selectedTag}, versionLabel(v))),
                    ),
                ),
                refreshButton(args, !ready),
            ),
        ),
    );
}

function nixVersionSection(args) {
    const deploymentUpdate = args.deploymentUpdate;
    const repositoryStatus = deploymentUpdate.repositoryStatus();
    const commitStatus = deploymentUpdate.nixDockerBuild.commitDiscovery.val;
    const branches = deploymentUpdate.nixDockerBuild.branches.val;
    const branch = deploymentUpdate.nixDockerBuild.selectedBranch.val;
    const commits = deploymentUpdate.nixDockerBuild.commits.val;
    const deployedVersion = args.deployment?.deployedVersion || '';
    const selectedCommit = deploymentUpdate.nixDockerBuild.selectedCommit.val;
    const hasSelectedCommit = Boolean(selectedCommit && commits.some(v => v?.id === selectedCommit));
    const selectedLabel = deploymentUpdate.documentRevision.val > 0
        ? 'Selected in Code'
        : (selectedCommit === deployedVersion ? 'Current version (not found on branch)' : 'Selected commit (not found on branch)');
    const commitOptions = selectedCommit && !hasSelectedCommit
        ? [{id: selectedCommit, label: selectedLabel}, ...commits]
        : commits;
    const repositoryReady = repositoryStatus.status === 'ok';
    const branchPlaceholder = args.loadingVersions.val
        ? 'Loading branches...'
        : (repositoryReady && branches.length ? 'Select a branch...' : (repositoryReady ? 'No branches loaded' : 'Valid repository path needed to load versions'));
    const commitPlaceholder = args.loadingVersions.val
        ? 'Loading commits...'
        : (!repositoryReady
            ? 'Valid repository path needed to load versions'
            : (!branch ? 'Select a branch to load commits' : (commitOptions.length ? 'Select a commit...' : 'No commits loaded')));
    return collapsibleSection(
        'Version',
        args.form.versionSectionOpen,
        div(
            {class: 'flex flex-col gap-2'},
            div(
                {class: 'grid grid-cols-1 items-end gap-x-3 gap-y-2 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]'},
                label(
                    {class: 'flex flex-col gap-1 text-xs text-gray-400'},
                    span('Branch'),
                    select({
                        class: selectClass(),
                        value: branch,
                        disabled: repositoryStatus.status !== 'ok' || branches.length === 0 || args.loadingVersions.val
                            || (args.mode === 'update' && !deploymentUpdate.desiredRunning.val),
                        onchange: e => { void deploymentUpdate.selectBranch(e.target.value); },
                    },
                        option({value: '', selected: !branch}, branchPlaceholder),
                        ...branches.map(value => option({value, selected: value === branch}, value)),
                    ),
                ),
                label(
                    {class: 'flex flex-col gap-1 text-xs text-gray-400'},
                    span('Commit'),
                    select({
                        class: selectClass(),
                        value: selectedCommit,
                        disabled: commitStatus.status !== 'ok' || args.loadingVersions.val || commitOptions.length === 0
                            || (args.mode === 'update' && !deploymentUpdate.desiredRunning.val),
                        onchange: e => deploymentUpdate.selectCommit(e.target.value),
                    },
                        option({value: '', disabled: true, selected: !selectedCommit}, commitPlaceholder),
                        ...commitOptions.map(v => option({value: v.id, selected: v.id === selectedCommit}, v.label || versionLabel(v))),
                    ),
                ),
                refreshButton(args, !repositoryReady),
            ),
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

function refreshButton(args, disabled = false) {
    return div(
        {class: 'flex items-end'},
        button({
            class: 'inline-flex h-9 items-center justify-center gap-1.5 px-3 rounded-lg text-xs text-gray-300 bg-gray-800 border border-gray-600 hover:bg-gray-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors cursor-pointer',
            disabled: args.loadingVersions.val || disabled,
            onclick: args.onRefresh,
            type: 'button',
            title: 'Refresh available versions',
        }, refreshIcon({size: 12}), 'Refresh'),
    );
}

function versionLabel(version) {
    const date = version.time instanceof Date && version.time.getTime() > 0
        ? version.time.toISOString().substring(0, 10)
        : '';
    const shortID = version.id.length > 7 && /^[0-9a-f]+$/i.test(version.id) ? version.id.substring(0, 7) : version.id;
    const labelText = (version.label || '').substring(0, 30);
    const ellipsis = (version.label || '').length > 30 ? '...' : '';
    return `${date}\t\t${shortID}\t\t${labelText}${ellipsis}`;
}

function selectClass() {
    return 'w-full h-9 px-3 rounded-sm bg-gray-800 text-gray-100 border border-gray-700 focus:outline-none focus:ring-1 focus:ring-brand';
}
