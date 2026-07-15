import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {assetMetasS, deploymentsS, spacesS} from "../state/deployments.js";
import {spinnerButton} from "./spinnerbutton.js";
import {refreshIcon} from "../lib/icons.js";
import {
    assetEditorPane,
    assetMountsPane,
    buildValidateSourceRequest,
    deploymentForm,
    envVarsPane,
    formInvalidReason,
    imageVersionFromReference,
    isFormValid,
    networkingPane,
    resourcesPane,
    sectionDivider,
    upgradeStrategyPane,
    volumeMountsPane,
} from "./deploymentForm.js";
import {DeploymentCreationUpdate, SOURCE_DOCKER_IMAGE, SOURCE_NIX_DOCKER} from "./deploymentCreationUpdate.js";

const { div, span, select, option, button, p, label, input } = van.tags;

const versionLabel = (v) => {
    const date = v.time instanceof Date && v.time.getTime() > 0
        ? v.time.toISOString().substring(0, 10)
        : '';
    const shortId = v.id.length > 7 && /^[0-9a-f]+$/i.test(v.id) ? v.id.substring(0, 7) : v.id;
    const releaseLabel = (v.label || '').substring(0, 30);
    const ellipsis = (v.label || '').length > 30 ? '...' : '';
    return `${date}\t\t${shortId}\t\t${releaseLabel}${ellipsis}`;
};

const currentCommitLabel = (id, commits) => {
    const matched = (commits || []).find(v => v?.id === id);
    if (matched) return `${versionLabel(matched)} current version`;
    return `${id} current version (not found on branch)`;
};

const isInternalOpenDeployDeployment = (deployment) => {
    const name = deployment?.name || deployment?.configId?.name || '';
    const spaceId = Number(deployment?.spaceId ?? deployment?.configId?.spaceId ?? -1);
    return spaceId === 0 && (name === 'opendeploy' || name === 'opendeploy-net');
};

export function deployOverlay(deployment, deploymentConfig, onClose, onDeployed) {
    const deploymentUpdate = new DeploymentCreationUpdate({deployment, deploymentConfig});
    const form = deploymentUpdate.form;
    const internalDeployment = isInternalOpenDeployDeployment(deployment);
    const internalGithubRelease = deployment.variant === 'githubRelease';
    const internalOpenDeployRelease = internalDeployment || internalGithubRelease;
    const loadingVersions = van.state(false);
    const requestDescription = van.state('');
    const versionError = van.state('');
    const errorMsg = van.state('');
    const assets = van.state(assetMetasS.val);
    const canStop = Boolean(deployment.desiredRunning);
    const canManageLifecycle = !internalDeployment && deployment.runnerType !== 'systemd';
    const canStart = Boolean(deployment.deployedVersion);
    let requestSeq = 0;
    let versionRequestSeq = 0;

    const startRequest = (description) => {
        const seq = ++requestSeq;
        requestDescription.val = description;
        return () => {
            if (requestSeq === seq) requestDescription.val = '';
        };
    };

    const withRequest = async (description, action) => {
        const endRequest = startRequest(description);
        try {
            return await action();
        } finally {
            endRequest();
        }
    };

    const loadVersions = async (branch, opts = {}) => {
        const versionRequest = ++versionRequestSeq;
        const sourceID = deploymentUpdate.currentSourceID();
        if (!internalOpenDeployRelease && !sourceID) {
            versionError.val = form.sourceType.val === 'containerImage' ? 'Image not set' : 'Repository not set';
            deploymentUpdate.nixDockerBuild.commits.val = [];
            deploymentUpdate.containerImage.tags.val = [];
            loadingVersions.val = false;
            return;
        }
        const sourceType = form.sourceType.val;
        const selectedBranch = sourceType === SOURCE_NIX_DOCKER
            ? (branch || deploymentUpdate.nixDockerBuild.selectedBranch.val || '')
            : '';
        const endRequest = startRequest(versionRequestDescription(sourceType, internalOpenDeployRelease, selectedBranch));
        loadingVersions.val = true;
        versionError.val = '';
        const sourceKey = deploymentUpdate.sourceKey();
        const isCurrentVersionRequest = () => versionRequest === versionRequestSeq
            && sourceType === form.sourceType.val
            && sourceKey === deploymentUpdate.sourceKey();
        try {
            if (internalOpenDeployRelease) {
                const req = {deploymentId: deployment.id};
                let result;
                try {
                    result = await capi.postV1DeploymentVersions(req);
                } catch (e) {
                    if (!isCurrentVersionRequest()) return;
                    console.error('[opendeploy] deployment versions refresh request failed', {request: req, error: e, stack: e?.stack});
                    versionError.val = e.message || 'Failed to load versions';
                    deploymentUpdate.githubRelease.releases.val = [];
                    return;
                }
                if (!isCurrentVersionRequest()) return;
                console.log('[opendeploy] deployment versions refresh response', {request: req, response: result});
                try {
                    const releases = result?.githubRelease?.releases || [];
                    deploymentUpdate.githubRelease.releases.val = releases;
                    const previous = deploymentUpdate.githubRelease.selectedRelease.val;
                    const deployedId = deployment.deployedVersion || '';
                    if (opts.preserveSelection && previous && releases.some(v => v.id === previous)) {
                        deploymentUpdate.githubRelease.selectedRelease.val = previous;
                    } else if (opts.preserveSelection && deployedId && releases.some(v => v.id === deployedId)) {
                        deploymentUpdate.githubRelease.selectedRelease.val = deployedId;
                    } else if (!releases.some(v => v.id === previous)) {
                        deploymentUpdate.githubRelease.selectedRelease.val = releases[0]?.id || '';
                    }
                } catch (e) {
                    console.error('[opendeploy] deployment versions refresh client error', {request: req, response: result, error: e, stack: e?.stack});
                    versionError.val = `Client error after loading versions: ${e.message || e}`;
                    deploymentUpdate.githubRelease.releases.val = [];
                }
                return;
            }
            const trusted = deploymentUpdate.hasTrustedSourceValidation();
            const req = sourceType === SOURCE_NIX_DOCKER
                ? buildValidateSourceRequest(form, trusted ? {
                    branch: selectedBranch,
                    refreshAvailableBranches: opts.refreshAvailableBranches ?? (!selectedBranch && deploymentUpdate.nixDockerBuild.branches.val.length === 0),
                    refreshAvailableCommits: Boolean(selectedBranch),
                    checkFlakePath: Boolean(form.nixFlake.val.trim()),
                } : {branch: selectedBranch})
                : buildValidateSourceRequest(form, {refreshVersions: true});
            let result;
            try {
                result = await capi.postV1RepoValidate(req);
            } catch (e) {
                if (!isCurrentVersionRequest()) return;
                console.error('[opendeploy] deployment repo refresh request failed', {request: req, error: e, stack: e?.stack});
                versionError.val = e.message || 'Failed to load versions';
                deploymentUpdate.setRepoCheckError(versionError.val);
                deploymentUpdate.nixDockerBuild.commits.val = [];
                deploymentUpdate.containerImage.tags.val = [];
                return;
            }
            if (!isCurrentVersionRequest()) return;
            console.log('[opendeploy] deployment repo refresh response', {request: req, response: result});
            try {
                let sourceResult = deploymentUpdate.validationSourceResult(result);
                deploymentUpdate.setRepoCheckFromValidation(result, sourceID, sourceType, sourceKey, {syncVersionOptions: false});
                if (form.repoCheck.val.status !== 'ok') {
                    versionError.val = form.repoCheck.val.message || 'Unable to connect to source repository.';
                    deploymentUpdate.nixDockerBuild.branches.val = form.repoCheck.val.branches || [];
                    deploymentUpdate.nixDockerBuild.commits.val = [];
                    deploymentUpdate.containerImage.tags.val = [];
                } else {
                    if (sourceType === SOURCE_DOCKER_IMAGE) {
                        const tags = form.repoCheck.val.tags || [];
                        deploymentUpdate.containerImage.tags.val = tags;
                        const previous = deploymentUpdate.containerImage.selectedTag.val;
                        const deployedId = deployment.deployedVersion || '';
                        if (!opts.preserveSelection) {
                            deploymentUpdate.containerImage.selectedTag.val = tags[0]?.id || '';
                        } else if (previous && tags.some(v => v.id === previous)) {
                            deploymentUpdate.containerImage.selectedTag.val = previous;
                        } else if (deployedId && tags.some(v => v.id === deployedId)) {
                            deploymentUpdate.containerImage.selectedTag.val = deployedId;
                        } else if (!tags.some(v => v.id === deploymentUpdate.containerImage.selectedTag.val)) {
                            deploymentUpdate.containerImage.selectedTag.val = tags[0]?.id || '';
                        }
                        deploymentUpdate.containerImage.selectedTagSourceKey.val = deploymentUpdate.containerImage.selectedTag.val ? sourceKey : '';
                    } else {
                        deploymentUpdate.nixDockerBuild.branches.val = form.repoCheck.val.branches || [];
                        const nextBranch = sourceResult.availableCommits?.branch || form.repoCheck.val.branch || deploymentUpdate.currentBranch(form.repoCheck.val, selectedBranch);
                        const commits = deploymentUpdate.commitsForBranch(nextBranch, form.repoCheck.val);
                        deploymentUpdate.nixDockerBuild.selectedBranch.val = nextBranch;
                        deploymentUpdate.nixDockerBuild.commits.val = commits;
                        const previous = deploymentUpdate.nixDockerBuild.selectedCommit.val;
                        const deployedId = deployment.deployedVersion || '';
                        if (!opts.preserveSelection) {
                            deploymentUpdate.nixDockerBuild.selectedCommit.val = deployedId || commits[0]?.id || '';
                        } else if (previous && commits.some(v => v.id === previous)) {
                            deploymentUpdate.nixDockerBuild.selectedCommit.val = previous;
                        } else if (previous && previous === deployedId) {
                            deploymentUpdate.nixDockerBuild.selectedCommit.val = previous;
                        } else if (opts.preserveSelection && deployedId) {
                            deploymentUpdate.nixDockerBuild.selectedCommit.val = deployedId;
                        } else if (deployedId && commits.some(v => v.id === deployedId)) {
                            deploymentUpdate.nixDockerBuild.selectedCommit.val = deployedId;
                        } else if (!commits.some(v => v.id === deploymentUpdate.nixDockerBuild.selectedCommit.val)) {
                            deploymentUpdate.nixDockerBuild.selectedCommit.val = commits[0]?.id || '';
                        }
                        deploymentUpdate.nixDockerBuild.selectedCommitSourceKey.val = deploymentUpdate.nixDockerBuild.selectedCommit.val ? sourceKey : '';
                    }
                }
            } catch (e) {
                console.error('[opendeploy] deployment repo refresh client error', {request: req, response: result, error: e, stack: e?.stack});
                versionError.val = `Client error after validation: ${e.message || e}`;
                deploymentUpdate.setRepoCheckError(versionError.val);
                deploymentUpdate.nixDockerBuild.commits.val = [];
                deploymentUpdate.containerImage.tags.val = [];
            }
        } finally {
            if (isCurrentVersionRequest()) loadingVersions.val = false;
            endRequest();
        }
    };

    van.derive(() => {
        assets.val = assetMetasS.val;
    });

    van.derive(() => {
        if (form.sourceType.val !== 'containerImage') return;
        const check = form.repoCheck.val;
        if (check.status !== 'ok' || check.sourceKey !== deploymentUpdate.sourceKey()) return;
        const nextTags = check.tags || [];
        if (nextTags.length === 0) return;
        deploymentUpdate.containerImage.tags.val = nextTags;
        if (!nextTags.some(v => v.id === deploymentUpdate.containerImage.selectedTag.val)) {
            deploymentUpdate.containerImage.selectedTag.val = nextTags[0]?.id || '';
            deploymentUpdate.containerImage.selectedTagSourceKey.val = deploymentUpdate.sourceKey();
        }
    });

    if (deployment.variant && (internalOpenDeployRelease || deployment.variant !== 'containerImage' || !deploymentUpdate.hasTrustedSourceValidation())) {
        loadVersions('', {preserveSelection: true});
    }

    const onBranchChange = (e) => {
        deploymentUpdate.nixDockerBuild.selectedBranch.val = e.target.value;
        loadVersions(e.target.value, {preserveSelection: false});
    };

    const doDeploy = async () => {
        return withRequest('Updating deployment.', async () => {
            errorMsg.val = '';
            if (!internalDeployment && !internalGithubRelease && !isFormValid(form, {deployments: deploymentsS.val})) {
                errorMsg.val = 'Artifact source and required execution fields must be set.';
                throw new Error(errorMsg.val);
            }

            const payload = deploymentUpdate.toUpdatePayload({internalGithubRelease: internalOpenDeployRelease, versionOnly: internalDeployment});

            try {
                await capi.postV1DeploymentUpdate(payload);
            } catch (e) {
                errorMsg.val = e.message || 'Deploy failed';
                throw e;
            }
            if (onDeployed) onDeployed();
            onClose();
        });
    };

    const doStop = async () => {
        return withRequest('Stopping deployment.', async () => {
            errorMsg.val = '';
            try {
                await capi.postV1DeploymentUpdate({
                    deploymentId: deployment.id,
                    stop: true,
                    version: deployment.currentVersion + 1,
                });
            } catch (e) {
                errorMsg.val = e.message || 'Stop failed';
                throw e;
            }
            if (onDeployed) onDeployed();
            onClose();
        });
    };

    const doStart = async () => {
        return withRequest('Starting deployment.', async () => {
            errorMsg.val = '';
            if (!canStart) {
                errorMsg.val = 'No previously selected version is available to start.';
                throw new Error(errorMsg.val);
            }
            try {
                await capi.postV1DeploymentUpdate({
                    deploymentId: deployment.id,
                    targetVersion: deployment.deployedVersion,
                    version: deployment.currentVersion + 1,
                });
            } catch (e) {
                errorMsg.val = e.message || 'Start failed';
                throw e;
            }
            if (onDeployed) onDeployed();
            onClose();
        });
    };

    const updateInvalidReason = () => (internalDeployment || internalGithubRelease) ? '' : formInvalidReason(form, {deployments: deploymentsS.val});

    const backdrop = div({
        class: "fixed inset-0 bg-black/60 z-40",
        onclick: onClose,
    });

    const hasVersions = deployment.variant;
    const dialog = div(
        {class: "fixed inset-0 z-50 flex items-center justify-center p-4 md:p-8 pointer-events-none"},
        div(
            {class: "bg-gray-900 border border-gray-700 rounded-xl shadow-2xl flex flex-row overflow-hidden pointer-events-auto",
             style: () => `width: ${!internalDeployment && (form.envPaneOpen.val || form.assetMountsPaneOpen.val || form.volumeMountsPaneOpen.val || form.upgradeStrategyPaneOpen.val || form.resourcesPaneOpen.val || form.networkingPaneOpen.val || form.assetEditorOpen.val) ? 1560 : 1120}px; max-width: calc(100vw - 1rem); max-height: 88vh;`,
             onclick: (e) => e.stopPropagation()},
            div(
                {class: "flex-1 min-w-0 flex flex-col"},
                div(
                    {class: "flex-1 min-h-0 overflow-auto p-4 flex flex-col gap-5"},
                    () => deploymentForm(form, {
                        identityLocked: true,
                        hideIdentity: internalDeployment,
                        hideArtifactSource: internalDeployment || internalGithubRelease,
                        hideExecution: internalDeployment || internalGithubRelease,
                        spaceOptions: spacesS.val,
                        assets: assets.val,
                        enableAssetEditor: true,
                    }),
                    () => hasVersions ? versionSection({
                        form,
                        deploymentUpdate,
                        internalGithubRelease: internalOpenDeployRelease,
                        loadingVersions,
                        versionError,
                        deployedVersion: deployment.deployedVersion || '',
                        onBranchChange,
                        onRefresh: () => loadVersions(deploymentUpdate.nixDockerBuild.selectedBranch.val, {refreshAvailableBranches: true, preserveSelection: true}),
                    }) : '',
                ),
                () => {
                    if (!errorMsg.val) return span();
                    return div(
                        {class: "px-4 pb-2"},
                        p({class: "text-xs text-red-400"}, errorMsg.val),
                    );
                },
                div(
                    {class: "flex flex-col gap-1 px-4 py-3 border-t border-gray-700"},
                    div(
                        {class: "flex items-center justify-between gap-3"},
                        requestStatus(requestDescription),
                        div(
                            {class: "flex items-center gap-2"},
                            button({
                                class: "text-sm text-gray-400 hover:text-gray-200 cursor-pointer px-3 py-1.5",
                                onclick: onClose,
                            }, "Cancel"),
                            lifecycleButton({
                                canManageLifecycle,
                                canStop,
                                canStart,
                                doStop,
                                doStart,
                            }),
                            spinnerButton("Update deployment", doDeploy, "btn-primary text-sm py-1.5 px-4", "button", () => Boolean(updateInvalidReason())),
                        ),
                    ),
                    () => updateInvalidReason()
                        ? p({class: "text-right text-xs text-amber-400"}, updateInvalidReason())
                        : '',
                ),
            ),
            () => internalDeployment ? '' : envVarsPane(form, {assets: assets.val}),
            () => internalDeployment ? '' : volumeMountsPane(form, {deployments: deploymentsS}),
            () => internalDeployment ? '' : assetMountsPane(form, {assets: assets.val, enableAssetEditor: true}),
            () => internalDeployment ? '' : upgradeStrategyPane(form),
            () => internalDeployment ? '' : resourcesPane(form),
            () => internalDeployment ? '' : networkingPane(form),
            () => internalDeployment ? '' : assetEditorPane(form),
        ),
    );

    return div(backdrop, dialog);
}

function lifecycleButton(args) {
    if (!args.canManageLifecycle) return span();
    if (args.canStop) {
        return spinnerButton(
            "Stop deployment",
            args.doStop,
            "bg-red-900/70 text-red-200 border border-red-700 hover:bg-red-900 text-sm py-1.5 px-4",
        );
    }
    return spinnerButton(
        "Start deployment",
        args.doStart,
        "btn-secondary text-sm py-1.5 px-4",
        "button",
        () => !args.canStart,
    );
}

function versionSection(args) {
    const deploymentUpdate = args.deploymentUpdate;
    if (args.internalGithubRelease) {
        return githubReleaseVersionSection(args);
    }
    if (args.form.sourceType.val === SOURCE_DOCKER_IMAGE) {
        const explicitVersion = imageVersionFromReference(args.form.containerImage.val);
        if (explicitVersion) {
            return div(
                {class: "flex flex-col gap-3"},
                sectionDivider("Version"),
                label(
                    {class: "flex flex-col gap-1 text-xs text-gray-400"},
                    span(explicitVersion.startsWith('sha256:') ? "Digest from image" : "Tag from image"),
                    input({class: selectClass(), disabled: true, value: explicitVersion}),
                ),
            );
        }
        const tags = deploymentUpdate.containerImage.tags.val;
        const selectedTag = deploymentUpdate.containerImage.selectedTag.val;
        const message = args.loadingVersions.val
            ? "Loading tags..."
            : args.versionError.val;
        return div(
            {class: "flex flex-col gap-3"},
            sectionDivider("Version"),
            div(
                {class: "grid grid-cols-1 md:grid-cols-[minmax(0,1fr)_auto] items-end gap-3"},
                label(
                    {class: "flex flex-col gap-1 text-xs text-gray-400"},
                    span("Tag"),
                    select(
                        {
                            class: selectClass(),
                            value: selectedTag,
                            disabled: args.loadingVersions.val || args.versionError.val || tags.length === 0,
                            onchange: (e) => {
                                deploymentUpdate.containerImage.selectedTag.val = e.target.value;
                                deploymentUpdate.containerImage.selectedTagSourceKey.val = deploymentUpdate.sourceKey();
                            },
                        },
                        option({value: '', disabled: true, selected: !selectedTag}, message || (tags.length ? "Select a tag..." : "No tags loaded")),
                        ...tags.map(v => option({value: v.id, selected: v.id === selectedTag}, versionLabel(v))),
                    ),
                ),
                refreshButton(args),
            ),
        );
    }

    return nixVersionSection(args);
}

function githubReleaseVersionSection(args) {
    const releases = args.deploymentUpdate.githubRelease.releases.val;
    const selectedRelease = args.deploymentUpdate.githubRelease.selectedRelease.val;
    const message = args.loadingVersions.val
        ? "Loading releases..."
        : args.versionError.val;
    return div(
        {class: "flex flex-col gap-3"},
        sectionDivider("Version"),
        div(
            {class: "grid grid-cols-1 md:grid-cols-[minmax(0,1fr)_auto] items-end gap-3"},
            label(
                {class: "flex flex-col gap-1 text-xs text-gray-400"},
                span("Release"),
                select(
                    {
                        class: selectClass(),
                        value: selectedRelease,
                        disabled: args.loadingVersions.val || args.versionError.val || releases.length === 0,
                        onchange: (e) => { args.deploymentUpdate.githubRelease.selectedRelease.val = e.target.value; },
                    },
                    option({value: '', disabled: true, selected: !selectedRelease}, message || (releases.length ? "Select a release..." : "No releases loaded")),
                    ...releases.map(v => option({value: v.id, selected: v.id === selectedRelease}, versionLabel(v))),
                ),
            ),
            refreshButton(args),
        ),
    );
}

function nixVersionSection(args) {
    const deploymentUpdate = args.deploymentUpdate;
    const branches = deploymentUpdate.nixDockerBuild.branches.val;
    const branch = deploymentUpdate.nixDockerBuild.selectedBranch.val;
    const commits = deploymentUpdate.nixDockerBuild.commits.val;
    const selectedCommit = deploymentUpdate.nixDockerBuild.selectedCommit.val;
    const deployedVersion = args.deployedVersion || '';
    const hasRealDeployedCommit = Boolean(deployedVersion && commits.some(v => v?.id === deployedVersion));
    const commitOptions = deployedVersion && !hasRealDeployedCommit
        ? [
            {id: deployedVersion, label: currentCommitLabel(deployedVersion, commits)},
            ...commits.filter(v => v?.id).map(v => ({id: v.id, label: versionLabel(v)})),
        ]
        : commits.map(v => ({id: v.id, label: versionLabel(v)}));
    const message = args.loadingVersions.val
        ? "Loading commits..."
        : args.versionError.val;
    return div(
        {class: "flex flex-col gap-3"},
        sectionDivider("Version"),
        div(
            {class: "grid grid-cols-1 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] items-end gap-3"},
            label(
                {class: "flex flex-col gap-1 text-xs text-gray-400"},
                span("Branch"),
                select(
                    {
                        class: selectClass(),
                        value: branch,
                        disabled: branches.length === 0 || args.loadingVersions.val,
                        onchange: args.onBranchChange,
                    },
                    option({value: '', disabled: true, selected: branches.length === 0}, branches.length ? "Select a branch..." : "No branches loaded"),
                    ...branches.map(b => option({value: b, selected: b === branch}, b)),
                ),
            ),
            label(
                {class: "flex flex-col gap-1 text-xs text-gray-400"},
                span("Commit"),
                select(
                        {
                            class: selectClass(),
                            value: selectedCommit,
                            disabled: args.loadingVersions.val || args.versionError.val || commitOptions.length === 0,
                            onchange: (e) => {
                                deploymentUpdate.nixDockerBuild.selectedCommit.val = e.target.value;
                            deploymentUpdate.nixDockerBuild.selectedCommitSourceKey.val = deploymentUpdate.sourceKey();
                            if (args.onVersionChange) args.onVersionChange(e.target.value);
                        },
                        },
                    option({value: '', disabled: true, selected: !selectedCommit}, message || (commitOptions.length ? "Select a commit..." : "No commits loaded")),
                    ...commitOptions.map(v => option({value: v.id, selected: v.id === selectedCommit}, v.label)),
                ),
            ),
            refreshButton(args),
        ),
    );
}

function requestStatus(requestDescription) {
    return span(
        {class: () => requestDescription.val ? "inline-flex items-center gap-2 text-xs text-gray-400" : "invisible text-xs"},
        span({class: "w-[1.1em] h-[1.1em] border-[0.15em] border-gray-500/30 border-t-gray-300 rounded-full animate-spin"}),
        span(() => requestDescription.val || 'Idle'),
    );
}

function versionRequestDescription(sourceType, internalGithubRelease, selectedBranch) {
    if (internalGithubRelease) return 'Refreshing available releases.';
    if (sourceType === SOURCE_DOCKER_IMAGE) return 'Refreshing available tags.';
    if (sourceType === SOURCE_NIX_DOCKER) {
        return selectedBranch ? 'Refreshing available commits.' : 'Refreshing available branches.';
    }
    return 'Refreshing available versions.';
}

function refreshButton(args) {
    return div(
        {class: "flex items-end"},
        button({
            class: "inline-flex h-9 items-center justify-center gap-1.5 px-3 rounded-lg text-xs text-gray-300 bg-gray-800 border border-gray-600 hover:bg-gray-700 transition-colors cursor-pointer",
            disabled: args.loadingVersions.val,
            onclick: args.onRefresh,
            type: "button",
            title: "Refresh available versions",
        }, refreshIcon({size: 12}), "Refresh"),
    );
}

function selectClass() {
    return "w-full h-9 px-3 rounded-sm bg-gray-800 text-gray-100 border border-gray-700 focus:outline-none focus:ring-1 focus:ring-brand";
}
