import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {deploymentsS, spacesS} from "../state/deployments.js";
import {spinnerButton} from "./spinnerbutton.js";
import {refreshIcon} from "../lib/icons.js";
import {
    assetEditorPane,
    assetMountsPane,
    buildValidateSourceRequest,
    deploymentForm,
    envVarsPane,
    imageVersionFromReference,
    isFormValid,
    sectionDivider,
    volumeMountsPane,
} from "./deploymentForm.js";
import {DeploymentCreationUpdate, SOURCE_DOCKER_IMAGE, SOURCE_NIX_DOCKER} from "./deploymentCreationUpdate.js";

const { div, span, button, p, label, select, option, input } = van.tags;

export function createOverlay(onClose, onCreated, opts = {}) {
    const errorMsg = van.state('');
    const deploymentUpdate = new DeploymentCreationUpdate({
        deployment: opts.sourceDeployment || null,
        deploymentConfig: opts.sourceDeploymentConfig || null,
    });
    const form = deploymentUpdate.form;
    if (opts.sourceDeploymentConfig) {
        form.deploymentId.val = 0;
        form.name.val = '';
        form.spaceId.val = 1;
        form.machine.val = '';
    }
    const machines = van.state([]);
    const machinesLoaded = van.state(false);
    const assets = van.state([]);
    const loadingVersions = van.state(false);

    van.derive(() => {
        deploymentUpdate.syncVersionOptionsFromCheck(deploymentUpdate.activeRepoCheck());
    });

    const loadMachines = async () => {
        try {
            const res = await capi.getV1ClusterStatus();
            machines.val = (res.machines || []).map(m => m.name).filter(Boolean).sort();
            if (!form.machine.val && machines.val.length === 1) {
                form.machine.val = machines.val[0];
            }
        } catch (e) {
            errorMsg.val = e.message || 'Failed to load cluster machines';
            machines.val = [];
        }
        machinesLoaded.val = true;
    };

    const loadAssets = async () => {
        try {
            const res = await capi.postV1AssetsList({});
            assets.val = res.items || [];
        } catch (e) {
            assets.val = [];
        }
    };

    loadMachines();
    loadAssets();

    const doCreate = async () => {
        errorMsg.val = '';
        if (!isFormValid(form, {machineOptions: machines.val, deployments: deploymentsS.val})) {
            errorMsg.val = 'Name, machine, artifact source, and required execution fields must be set.';
            throw new Error(errorMsg.val);
        }

        try {
            const cfg = await capi.postV1DeploymentCreate({
                ...deploymentUpdate.toCreatePayload(),
            });
            const updatePayload = deploymentUpdate.toCreateUpdatePayload(cfg);
            if (updatePayload) {
                await capi.postV1DeploymentUpdate(updatePayload);
            }
        } catch (e) {
            errorMsg.val = e.message || 'Failed to create deployment';
            throw e;
        }
        if (onCreated) onCreated();
        onClose();
    };

    const createButton = spinnerButton("Create", doCreate, "btn-primary text-sm py-1.5 px-4", "button", () => !isFormValid(form, {machineOptions: machines.val, deployments: deploymentsS.val}));
    createButton.dataset.testid = "create-deployment-submit";

    const backdrop = div({
        class: "fixed inset-0 bg-black/60 z-40",
        onclick: onClose,
    });

    const dialog = div(
        {class: "fixed inset-0 z-50 flex items-center justify-center p-4 md:p-8 pointer-events-none", "data-testid": "create-deployment-dialog"},
        div(
            {class: "bg-gray-900 border border-gray-700 rounded-xl shadow-2xl flex flex-row overflow-hidden pointer-events-auto",
             style: () => `width: ${form.envPaneOpen.val || form.assetMountsPaneOpen.val || form.volumeMountsPaneOpen.val || form.assetEditorOpen.val ? 1560 : 1120}px; max-width: calc(100vw - 1rem); max-height: 88vh;`,
             onclick: (e) => e.stopPropagation()},
            div(
                {class: "flex-1 min-w-0 flex flex-col"},
                div(
                    {class: "flex-1 min-h-0 overflow-auto p-4 flex flex-col gap-5"},
                    () => deploymentForm(form, {
                        spaceOptions: spacesS.val,
                        machineOptions: machines.val,
                        machineOptionsLoaded: machinesLoaded.val,
                        assets: assets.val,
                        enableAssetEditor: true,
                        showRunnerSummary: false,
                    }),
                    () => createVersionSection({
                        deploymentUpdate,
                        loadingVersions,
                        onBranchChange: (branch) => loadSourceVersions(deploymentUpdate, loadingVersions, branch, {preserveSelection: false}),
                        onRefresh: () => loadSourceVersions(deploymentUpdate, loadingVersions, deploymentUpdate.nixDockerBuild.selectedBranch.val, {refreshAvailableBranches: true, preserveSelection: true}),
                    }),
                ),
                () => {
                    if (!errorMsg.val) return span();
                    return div(
                        {class: "px-4 pb-2"},
                        p({class: "text-xs text-red-400"}, errorMsg.val),
                    );
                },
                div(
                    {class: "flex items-center justify-end gap-3 px-4 py-3 border-t border-gray-700"},
                    button({
                        class: "text-sm text-gray-400 hover:text-gray-200 cursor-pointer px-3 py-1.5",
                        onclick: onClose,
                    }, "Cancel"),
                    createButton,
                ),
            ),
            () => envVarsPane(form, {assets: assets.val}),
            volumeMountsPane(form, {deployments: deploymentsS}),
            () => assetMountsPane(form, {assets: assets.val, enableAssetEditor: true}),
            assetEditorPane(form, {onSaved: loadAssets}),
        ),
    );

    return div(backdrop, dialog);
}

function createVersionSection(args) {
    const deploymentUpdate = args.deploymentUpdate;
    const form = deploymentUpdate.form;
    const sourceType = form.sourceType.val;
    if (sourceType === SOURCE_DOCKER_IMAGE) {
        const imageSet = Boolean(form.containerImage.val.trim());
        const explicitVersion = imageVersionFromReference(form.containerImage.val);
        const check = deploymentUpdate.activeRepoCheck();
        const ready = check.status === 'ok';
        const tags = deploymentUpdate.containerImage.tags.val;
        const selectedTag = deploymentUpdate.containerImage.selectedTagSourceKey.val === deploymentUpdate.sourceKey()
            ? deploymentUpdate.containerImage.selectedTag.val
            : '';
        if (explicitVersion) {
            return div(
                {class: "flex flex-col gap-3"},
                sectionDivider("Version"),
                versionMessage(imageSet && check.status !== 'idle' ? versionStatusMessage(deploymentUpdate, check) : (imageSet ? '' : 'Image not set')),
                label(
                    {class: "flex flex-col gap-1 text-xs text-gray-400"},
                    span(explicitVersion.startsWith('sha256:') ? "Digest from image" : "Tag from image"),
                    input({class: selectClass(), disabled: true, value: explicitVersion}),
                ),
            );
        }
        return div(
            {class: "flex flex-col gap-3"},
            sectionDivider("Version"),
            versionMessage(imageSet ? versionStatusMessage(deploymentUpdate, check) : 'Image not set'),
            div(
                {class: "grid grid-cols-1 md:grid-cols-[minmax(0,1fr)_auto] items-end gap-3"},
                label(
                    {class: "flex flex-col gap-1 text-xs text-gray-400"},
                    span("Tag"),
                    select({
                        class: selectClass(),
                        disabled: !ready || args.loadingVersions.val || tags.length === 0,
                        onchange: (e) => {
                            deploymentUpdate.containerImage.selectedTag.val = e.target.value;
                            deploymentUpdate.containerImage.selectedTagSourceKey.val = deploymentUpdate.sourceKey();
                        },
                    },
                        option({value: '', disabled: true, selected: !selectedTag}, versionPlaceholder(ready, args.loadingVersions.val, tags, "Image unavailable")),
                        ...tags.map(v => option({value: v.id, selected: v.id === selectedTag}, versionLabel(v))),
                    ),
                ),
                refreshRow(!imageSet || args.loadingVersions.val, args.onRefresh),
            ),
        );
    }

    const check = deploymentUpdate.activeRepoCheck();
    const ready = check.status === 'ok';
    const branches = deploymentUpdate.nixDockerBuild.branches.val.length > 0
        ? deploymentUpdate.nixDockerBuild.branches.val
        : (check.branches || []);
    const branch = deploymentUpdate.currentBranch(check, deploymentUpdate.nixDockerBuild.selectedBranch.val);
    const commits = deploymentUpdate.commitsForBranch(branch, check);
    const sourceLabel = "Commit";
    const selectedCommit = deploymentUpdate.nixDockerBuild.selectedCommitSourceKey.val === deploymentUpdate.sourceKey()
        ? deploymentUpdate.nixDockerBuild.selectedCommit.val
        : '';

    return div(
        {class: "flex flex-col gap-3"},
        sectionDivider("Version"),
        versionMessage(versionStatusMessage(deploymentUpdate, check)),
        div(
            {class: "grid grid-cols-1 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] items-end gap-3"},
            label(
                {class: "flex flex-col gap-1 text-xs text-gray-400"},
                span("Branch"),
                select(
                    {
                        class: selectClass(),
                        disabled: !ready || branches.length === 0 || args.loadingVersions.val,
                        onchange: (e) => {
                            deploymentUpdate.nixDockerBuild.selectedBranch.val = e.target.value;
                            args.onBranchChange(e.target.value);
                        },
                    },
                    option({value: '', selected: !branch}, branches.length ? "Select a branch..." : "No branches loaded"),
                    ...branches.map(s => option({value: s, selected: s === branch}, s)),
                ),
            ),
            label(
                {class: "flex flex-col gap-1 text-xs text-gray-400"},
                span(sourceLabel),
                select(
                    {
                        class: selectClass(),
                        disabled: !ready || args.loadingVersions.val || commits.length === 0,
                        onchange: (e) => {
                            deploymentUpdate.nixDockerBuild.selectedCommit.val = e.target.value;
                            deploymentUpdate.nixDockerBuild.selectedCommitSourceKey.val = deploymentUpdate.sourceKey();
                            deploymentUpdate.validateSelectedCommit(branch, e.target.value);
                        },
                    },
                    option({value: '', disabled: true, selected: !selectedCommit}, versionPlaceholder(ready, args.loadingVersions.val, commits)),
                    ...commits.map(v => option({value: v.id, selected: v.id === selectedCommit}, versionLabel(v))),
                ),
            ),
            refreshRow(!deploymentUpdate.currentSourceID() || args.loadingVersions.val, args.onRefresh),
        ),
    );
}

async function loadSourceVersions(deploymentUpdate, loadingVersions, branch, opts = {}) {
    const form = deploymentUpdate.form;
    const repo = deploymentUpdate.currentSourceID();
    if (!repo) return;
    loadingVersions.val = true;
    const sourceType = form.sourceType.val;
    const sourceKeyValue = deploymentUpdate.sourceKey();
    const trusted = deploymentUpdate.hasTrustedSourceValidation();
    const selectedBranch = branch || deploymentUpdate.nixDockerBuild.selectedBranch.val || '';
    const req = buildValidateSourceRequest(form, trusted ? {
        branch: selectedBranch,
        refreshAvailableBranches: opts.refreshAvailableBranches ?? !branch,
        refreshAvailableCommits: Boolean(selectedBranch),
        checkFlakePath: Boolean(form.nixFlake.val.trim()),
    } : {branch: branch || ''});
    let res;
    try {
        res = await capi.postV1RepoValidate(req);
    } catch (e) {
        console.error('[opendeploy] create version refresh request failed', {request: req, error: e, stack: e?.stack});
        deploymentUpdate.setRepoCheckError(e.message || 'Validation failed.');
        loadingVersions.val = false;
        return;
    }
    console.log('[opendeploy] create version refresh response', {request: req, response: res});
    try {
        const sourceResult = deploymentUpdate.validationSourceResult(res);
        deploymentUpdate.setRepoCheckFromValidation(res, repo, sourceType, sourceKeyValue);
        if (form.repoCheck.val.status === 'ok') {
            if (sourceType === SOURCE_NIX_DOCKER) {
                const nextBranch = sourceResult.availableCommits?.branch || deploymentUpdate.currentBranch(form.repoCheck.val, branch || deploymentUpdate.nixDockerBuild.selectedBranch.val);
                deploymentUpdate.nixDockerBuild.selectedBranch.val = nextBranch;
                deploymentUpdate.nixDockerBuild.commits.val = deploymentUpdate.commitsForBranch(nextBranch, form.repoCheck.val);
            }
            deploymentUpdate.syncVersionOptionsFromCheck(form.repoCheck.val, {preserveSelection: opts.preserveSelection !== false});
            if (!opts.preserveSelection && sourceType === SOURCE_NIX_DOCKER) {
                const commits = deploymentUpdate.nixDockerBuild.commits.val;
                deploymentUpdate.nixDockerBuild.selectedCommit.val = commits[0]?.id || '';
                deploymentUpdate.nixDockerBuild.selectedCommitSourceKey.val = deploymentUpdate.nixDockerBuild.selectedCommit.val ? sourceKeyValue : '';
            }
        }
    } catch (e) {
        console.error('[opendeploy] create version refresh client error', {request: req, response: res, error: e, stack: e?.stack});
        deploymentUpdate.setRepoCheckError(`Client error after validation: ${e.message || e}`);
    }
    loadingVersions.val = false;
}

function versionStatusMessage(deploymentUpdate, check) {
    const form = deploymentUpdate.form;
    if (!deploymentUpdate.currentSourceID()) return form.sourceType.val === SOURCE_DOCKER_IMAGE ? 'Image not set' : 'Repository not set';
    if (check.status === 'checking') return 'Checking repository access...';
    if (check.status !== 'ok') return form.sourceType.val === SOURCE_DOCKER_IMAGE ? 'Unable to list image tags.' : 'Unable to connect to source repository.';
    return '';
}

function versionMessage(message) {
    return message ? p({class: "text-xs text-gray-500"}, message) : span();
}

function refreshRow(disabled, onRefresh) {
    return div(
        {class: "flex items-end"},
        button({
            class: "inline-flex h-9 items-center justify-center gap-1.5 px-3 rounded-lg text-xs text-gray-300 bg-gray-800 border border-gray-600 hover:bg-gray-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors cursor-pointer",
            disabled,
            onclick: onRefresh,
            type: "button",
            title: "Refresh available versions",
        }, refreshIcon({size: 12}), "Refresh"),
    );
}

function versionPlaceholder(ready, loading, versions, unavailable = "Repository unavailable") {
    if (!ready) return unavailable;
    if (loading) return "Loading versions...";
    return versions.length ? "Select a version..." : "No versions loaded";
}

function versionLabel(v) {
    const date = v.time instanceof Date && v.time.getTime() > 0
        ? v.time.toISOString().substring(0, 10)
        : '';
    const shortId = v.id.length > 7 && /^[0-9a-f]+$/i.test(v.id) ? v.id.substring(0, 7) : v.id;
    const labelText = (v.label || '').substring(0, 30);
    const ellipsis = (v.label || '').length > 30 ? '...' : '';
    return `${date}\t\t${shortId}\t\t${labelText}${ellipsis}`;
}

function selectClass() {
    return "w-full h-9 px-3 rounded-sm bg-gray-800 text-gray-100 border border-gray-700 focus:outline-none focus:ring-1 focus:ring-brand";
}
