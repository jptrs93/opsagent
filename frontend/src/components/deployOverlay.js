import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {deploymentsS} from "../state/deployments.js";
import {spinnerButton} from "./spinnerbutton.js";
import {RefreshCw} from "vanjs-feather";
import {
    assetEditorPane,
    assetMountsPane,
    buildValidateSourceRequest,
    deploymentConfigToForm,
    deploymentForm,
    envVarsPane,
    formToSpec,
    hasTrustedSourceValidation,
    imageVersionFromReference,
    isFormValid,
    sectionDivider,
    sourceCheckFromValidation,
    sourceValidationKey,
    validateSelectedCommit,
    validationSourceResult,
    volumeMountsPane,
} from "./deploymentForm.js";

const { div, span, select, option, button, p, label, input } = van.tags;

const STATUS_RUNNING = 2;

const versionLabel = (v) => {
    const date = v.time instanceof Date && v.time.getTime() > 0
        ? v.time.toISOString().substring(0, 10)
        : '';
    const shortId = v.id.length > 7 && /^[0-9a-f]+$/i.test(v.id) ? v.id.substring(0, 7) : v.id;
    const label = (v.label || '').substring(0, 30);
    const ellipsis = (v.label || '').length > 30 ? '...' : '';
    return `${date}\t\t${shortId}\t\t${label}${ellipsis}`;
};

export function deployOverlay(deployment, deploymentConfig, onClose, onDeployed) {
    const form = deploymentConfigToForm(deploymentConfig);
    const internalGithubRelease = deployment.variant === 'githubRelease';
    const initialSpecKey = JSON.stringify(formToSpec(form));
    const scopes = van.state([]);
    const selectedScope = van.state('');
    const versions = van.state(deployment.variant === 'containerImage' && deployment.deployedVersion
        ? [{id: deployment.deployedVersion, label: 'Current'}]
        : []);
    const selectedVersion = van.state(deployment.variant === 'containerImage' ? (imageVersionFromReference(form.containerImage.val) || deployment.deployedVersion || '') : '');
    const loadingVersions = van.state(false);
    const versionError = van.state('');
    const errorMsg = van.state('');
    const assets = van.state([]);
    const isRunning = deployment.existingStatus === STATUS_RUNNING;
    const canManageLifecycle = deployment.runnerType !== 'systemd';
    const canStart = Boolean(deployment.deployedVersion);

    const environmentOptions = () => {
        const envs = new Set();
        for (const d of deploymentsS.rawVal || []) {
            const env = d.config?.configId?.environment;
            if (env) envs.add(env);
        }
        return [...envs].sort();
    };

    const loadVersions = async (scope, opts = {}) => {
        const sourceID = currentSourceID(form);
        if (!internalGithubRelease && !sourceID) {
            versionError.val = form.sourceType.val === 'containerImage' ? 'Image not set' : 'Repository not set';
            versions.val = [];
            return;
        }
        loadingVersions.val = true;
        versionError.val = '';
        const sourceType = form.sourceType.val;
        const sourceKey = sourceValidationKey(form);
        if (internalGithubRelease) {
            const req = {deploymentId: deployment.id, scope: scope || ''};
            let result;
            try {
                result = await capi.postV1DeploymentVersions(req);
            } catch (e) {
                console.error('[opendeploy] deployment versions refresh request failed', {request: req, error: e, stack: e?.stack});
                versionError.val = e.message || 'Failed to load versions';
                versions.val = [];
                loadingVersions.val = false;
                return;
            }
            console.log('[opendeploy] deployment versions refresh response', {request: req, response: result});
            try {
                const selected = selectedDeploymentVersionScope(result, scope);
                scopes.val = result.scopes || [];
                versions.val = selected.versions;
                selectedScope.val = selected.scope;
                if (!versions.val.some(v => v.id === selectedVersion.val)) {
                    selectedVersion.val = versions.val[0]?.id || '';
                }
            } catch (e) {
                console.error('[opendeploy] deployment versions refresh client error', {request: req, response: result, error: e, stack: e?.stack});
                versionError.val = `Client error after loading versions: ${e.message || e}`;
                versions.val = [];
            }
            loadingVersions.val = false;
            return;
        }
        const trusted = hasTrustedSourceValidation(form);
        const shouldRefreshScopes = opts.refreshScopes ?? (!scope && scopes.val.length === 0);
        const previousSelectedVersion = selectedVersion.val;
        const req = buildValidateSourceRequest(form, trusted ? {
            scope: scope || selectedScope.val || '',
            refreshScopes: shouldRefreshScopes,
            refreshVersions: true,
            checkFlakePath: Boolean(form.nixFlake.val.trim()),
        } : {scope: scope || ''});
        let result;
        try {
            result = await capi.postV1RepoValidate(req);
        } catch (e) {
            console.error('[opendeploy] deployment repo refresh request failed', {request: req, error: e, stack: e?.stack});
            versionError.val = e.message || 'Failed to load versions';
            form.repoCheck.val = {status: 'error', message: versionError.val, repo: sourceID, sourceType, sourceKey};
            versions.val = [];
            loadingVersions.val = false;
            return;
        }
        console.log('[opendeploy] deployment repo refresh response', {request: req, response: result});
        try {
            let sourceResult = validationSourceResult(form, result);
            form.repoCheck.val = sourceCheckFromValidation(form, result, sourceID, sourceType, sourceKey);
            if (form.repoCheck.val.status !== 'ok') {
                versionError.val = form.repoCheck.val.message || 'Unable to connect to source repository.';
                scopes.val = form.repoCheck.val.scopes || [];
                versions.val = [];
            } else {
                const preferredVersion = opts.preferVersion || '';
                if (!scope && preferredVersion && !(sourceResult.versions || []).some(v => v.id === preferredVersion)) {
                    const found = await validationForVersionScope(form, sourceID, sourceType, sourceKey, form.repoCheck.val.scopes || [], sourceResult.scope || '', preferredVersion);
                    if (found && sourceValidationKey(form) === sourceKey) {
                        result = found;
                        sourceResult = validationSourceResult(form, result);
                        form.repoCheck.val = sourceCheckFromValidation(form, result, sourceID, sourceType, sourceKey);
                    }
                }
                scopes.val = form.repoCheck.val.scopes || [];
                const scopeKey = sourceResult.scope || '';
                const vsList = (sourceResult.versions || []).length > 0
                    ? sourceResult.versions
                    : ((form.repoCheck.val.versionsByScope || {})[scopeKey]?.versions || []);
                versions.val = vsList;
                selectedScope.val = scopeKey;
                const deployedId = deployment.deployedVersion || '';
                if (!opts.preserveSelection) {
                    selectedVersion.val = vsList[0]?.id || '';
                } else if (previousSelectedVersion && vsList.some(v => v.id === previousSelectedVersion)) {
                    selectedVersion.val = previousSelectedVersion;
                } else if (deployedId && vsList.some(v => v.id === deployedId)) {
                    selectedVersion.val = deployedId;
                } else if (!vsList.some(v => v.id === selectedVersion.val)) {
                    selectedVersion.val = vsList[0]?.id || '';
                }
            }
        } catch (e) {
            console.error('[opendeploy] deployment repo refresh client error', {request: req, response: result, error: e, stack: e?.stack});
            versionError.val = `Client error after validation: ${e.message || e}`;
            form.repoCheck.val = {status: 'error', message: versionError.val, repo: sourceID, sourceType, sourceKey};
            versions.val = [];
        }
        loadingVersions.val = false;
    };

    const loadAssets = async () => {
        try {
            const res = await capi.postV1AssetsList({});
            assets.val = res.items || [];
        } catch (e) {
            assets.val = [];
        }
    };

    van.derive(() => {
        if (form.sourceType.val !== 'containerImage') return;
        const check = form.repoCheck.val;
        if (check.status !== 'ok' || check.sourceKey !== sourceValidationKey(form)) return;
        const nextVersions = check.versions || [];
        if (nextVersions.length === 0) return;
        versions.val = nextVersions;
        if (!nextVersions.some(v => v.id === selectedVersion.val)) {
            selectedVersion.val = nextVersions[0]?.id || '';
        }
    });

    if (deployment.variant && (deployment.variant !== 'containerImage' || !hasTrustedSourceValidation(form))) {
        loadVersions('', {preserveSelection: true, preferVersion: deployment.deployedVersion || ''});
    }
    loadAssets();

    const onScopeChange = (e) => {
        selectedScope.val = e.target.value;
        loadVersions(e.target.value, {preserveSelection: false});
    };

    const doDeploy = async () => {
        errorMsg.val = '';
        if (!internalGithubRelease && !isFormValid(form, {deployments: deploymentsS.val})) {
            errorMsg.val = 'Artifact source and required execution fields must be set.';
            throw new Error(errorMsg.val);
        }

        const payload = {
            deploymentId: deployment.id,
            version: deployment.currentVersion + 1,
        };

        if (!internalGithubRelease) {
            const nextSpec = formToSpec(form);
            if (JSON.stringify(nextSpec) !== initialSpecKey) {
                payload.spec = nextSpec;
            }
        }
        const targetVersion = internalGithubRelease ? selectedVersion.val.trim() : (imageVersionFromReference(form.containerImage.val) || selectedVersion.val.trim());
        if (targetVersion) {
            payload.targetVersion = targetVersion;
        }

        try {
            await capi.postV1DeploymentUpdate(payload);
        } catch (e) {
            errorMsg.val = e.message || 'Deploy failed';
            throw e;
        }
        if (onDeployed) onDeployed();
        onClose();
    };

    const doStop = async () => {
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
    };

    const doStart = async () => {
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
    };

    const backdrop = div({
        class: "fixed inset-0 bg-black/60 z-40",
        onclick: onClose,
    });

    const hasVersions = deployment.variant;
    const dialog = div(
        {class: "fixed inset-0 z-50 flex items-center justify-center p-4 md:p-8 pointer-events-none"},
        div(
            {class: "bg-gray-900 border border-gray-700 rounded-xl shadow-2xl flex flex-row overflow-hidden pointer-events-auto",
             style: () => `width: ${form.envPaneOpen.val || form.assetMountsPaneOpen.val || form.volumeMountsPaneOpen.val || form.assetEditorOpen.val ? 1560 : 1120}px; max-width: calc(100vw - 1rem); max-height: 88vh;`,
             onclick: (e) => e.stopPropagation()},
            div(
                {class: "flex-1 min-w-0 flex flex-col"},
                div(
                    {class: "flex-1 min-h-0 overflow-auto p-4 flex flex-col gap-5"},
                    () => deploymentForm(form, {
                        identityLocked: true,
                        hideArtifactSource: internalGithubRelease,
                        hideExecution: internalGithubRelease,
                        environmentOptions: environmentOptions(),
                        assets: assets.val,
                        enableAssetEditor: true,
                    }),
                    hasVersions ? versionSection({
                        form,
                        scopes,
                        selectedScope,
                        versions,
                        selectedVersion,
                        loadingVersions,
                        versionError,
                        sourceType: internalGithubRelease ? {val: 'githubRelease'} : form.sourceType,
                        deployedVersion: deployment.deployedVersion || '',
                        onScopeChange,
                        onVersionChange: (version) => validateSelectedCommit(form, selectedScope.val, version),
                        onRefresh: () => loadVersions(selectedScope.val, {refreshScopes: true, preserveSelection: true}),
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
                    {class: "flex items-center justify-between gap-3 px-4 py-3 border-t border-gray-700"},
                    button({
                        class: "text-sm text-gray-400 hover:text-gray-200 cursor-pointer px-3 py-1.5",
                        onclick: onClose,
                    }, "Cancel"),
                    div(
                        {class: "flex items-center gap-2"},
                        lifecycleButton({
                            canManageLifecycle,
                            isRunning,
                            canStart,
                            doStop,
                            doStart,
                        }),
                        spinnerButton("Update deployment", doDeploy, "btn-primary text-sm py-1.5 px-4", "button", () => !internalGithubRelease && !isFormValid(form, {deployments: deploymentsS.val})),
                    ),
                ),
            ),
            envVarsPane(form),
            volumeMountsPane(form, {deployments: deploymentsS}),
            () => assetMountsPane(form, {assets: assets.val, enableAssetEditor: true}),
            assetEditorPane(form, {onSaved: loadAssets}),
        ),
    );

    return div(backdrop, dialog);
}

function lifecycleButton(args) {
    if (!args.canManageLifecycle) return span();
    if (args.isRunning) {
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
    if (args.sourceType.val === 'containerImage') {
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
        const vs = args.versions.val;
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
                            disabled: args.loadingVersions.val || args.versionError.val || vs.length === 0,
                            onchange: (e) => { args.selectedVersion.val = e.target.value; },
                        },
                        option({value: '', disabled: true, selected: !args.selectedVersion.val}, message || (vs.length ? "Select a tag..." : "No tags loaded")),
                        ...vs.map(v => option({value: v.id, selected: v.id === args.selectedVersion.val}, versionLabel(v))),
                    ),
                ),
                refreshButton(args),
            ),
        );
    }

    return div(
        {class: "flex flex-col gap-3"},
        sectionDivider("Version"),
        div(
            {class: () => args.sourceType.val === 'githubRelease'
                ? "grid grid-cols-1 md:grid-cols-[minmax(0,1fr)_auto] items-end gap-3"
                : "grid grid-cols-1 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] items-end gap-3"},
            () => {
                if (args.sourceType.val === 'githubRelease') return '';
                const s = args.scopes.val;
                return label(
                    {class: "flex flex-col gap-1 text-xs text-gray-400"},
                    span("Branch"),
                    select(
                        {
                            class: selectClass(),
                            disabled: s.length === 0 || args.loadingVersions.val,
                            onchange: args.onScopeChange,
                        },
                        option({value: '', disabled: true, selected: s.length === 0}, s.length ? "Select a branch..." : "No branches loaded"),
                        ...s.map(b => option({value: b, selected: b === args.selectedScope.val}, b)),
                    ),
                );
            },
            () => {
                const vs = args.versions.val;
                const message = args.loadingVersions.val
                    ? "Loading versions..."
                    : args.versionError.val;
                return label(
                    {class: "flex flex-col gap-1 text-xs text-gray-400"},
                    span(() => args.sourceType.val === 'githubRelease' ? "Release" : "Commit"),
                    select(
                        {
                            class: selectClass(),
                            disabled: args.loadingVersions.val || args.versionError.val || vs.length === 0,
                            onchange: (e) => {
                                args.selectedVersion.val = e.target.value;
                                if (args.onVersionChange) args.onVersionChange(e.target.value);
                            },
                        },
                        option({value: '', disabled: true, selected: !args.selectedVersion.val}, message || (vs.length ? "Select a version..." : "No versions loaded")),
                        ...vs.map(v => option({value: v.id, selected: v.id === args.selectedVersion.val}, versionLabel(v))),
                    ),
                );
            },
            refreshButton(args),
        ),
    );
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
        }, RefreshCw({size: 12}), "Refresh"),
    );
}

function currentSourceID(form) {
    if (form.sourceType.val === 'containerImage') return form.containerImage.val.trim();
    return form.nixRepo.val.trim();
}

function selectedDeploymentVersionScope(result, requestedScope) {
    const versionsByScope = result?.versionsByScope || {};
    if (requestedScope && versionsByScope[requestedScope]) {
        return {scope: requestedScope, versions: versionsByScope[requestedScope]?.versions || []};
    }
    if (versionsByScope['']) {
        return {scope: '', versions: versionsByScope['']?.versions || []};
    }
    const scopes = result?.scopes || [];
    const fallbackScope = scopes.includes('main') ? 'main' : (scopes[0] || Object.keys(versionsByScope)[0] || '');
    return {scope: fallbackScope, versions: versionsByScope[fallbackScope]?.versions || []};
}

async function validationForVersionScope(form, sourceID, sourceType, sourceKey, scopes, currentScope, version) {
    if (!version || form.sourceType.val !== 'nixDockerBuild') return null;
    for (const candidate of scopes) {
        if (!candidate || candidate === currentScope) continue;
        const req = buildValidateSourceRequest(form, {
            scope: candidate,
            refreshScopes: false,
            refreshVersions: true,
            checkFlakePath: Boolean(form.nixFlake.val.trim()),
        });
        try {
            const result = await capi.postV1RepoValidate(req);
            if (sourceValidationKey(form) !== sourceKey) return null;
            const sourceResult = validationSourceResult(form, result);
            if ((sourceResult.versions || []).some(v => v.id === version)) return result;
        } catch (e) {
            console.error('[opendeploy] deployment version branch search failed', {sourceID, sourceType, scope: candidate, request: req, error: e, stack: e?.stack});
        }
    }
    return null;
}

function selectClass() {
    return "w-full h-9 px-3 rounded-sm bg-gray-800 text-gray-100 border border-gray-700 focus:outline-none focus:ring-1 focus:ring-brand";
}
