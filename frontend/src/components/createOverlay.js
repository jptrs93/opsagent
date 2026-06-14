import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {deploymentsS} from "../state/deployments.js";
import {spinnerButton} from "./spinnerbutton.js";
import {RefreshCw} from "vanjs-feather";
import {
    assetEditorPane,
    assetMountsPane,
    buildValidateSourceRequest,
    deploymentForm,
    emptyDeploymentForm,
    envVarsPane,
    formToYaml,
    imageVersionFromReference,
    isFormValid,
    sectionDivider,
    sourceCheckFromValidation,
    sourceValidationKey,
    validateSelectedCommit,
    validationSourceResult,
    volumeMountsPane,
} from "./deploymentForm.js";

const { div, span, button, p, label, select, option, input } = van.tags;

const SOURCE_GITHUB = 'githubRelease';
const SOURCE_DOCKER_IMAGE = 'containerImage';

export function createOverlay(onClose, onCreated) {
    const errorMsg = van.state('');
    const form = emptyDeploymentForm();
    const machines = van.state([]);
    const machinesLoaded = van.state(false);
    const assets = van.state([]);
    const selectedScope = van.state('');
    const selectedVersion = van.state('');
    const selectedVersionSourceKey = van.state('');
    const loadingVersions = van.state(false);

    van.derive(() => {
        const check = form.repoCheck.val;
        const key = sourceKey(form);
        if (check.status !== 'ok' || check.sourceKey !== key) return;
        const versions = Object.values(check.versionsByScope || {}).flatMap(scope => scope?.versions || []);
        if (versions.length === 0) return;
        if (selectedVersionSourceKey.val === key && versions.some(v => v.id === selectedVersion.val)) return;
        selectedVersion.val = versions[0].id;
        selectedVersionSourceKey.val = key;
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

    const environmentOptions = () => {
        const envs = new Set();
        for (const d of deploymentsS.val || []) {
            const env = d.config?.configId?.environment;
            if (env) envs.add(env);
        }
        return [...envs].sort();
    };

    const doCreate = async () => {
        errorMsg.val = '';
        if (!isFormValid(form, {machineOptions: machines.val})) {
            errorMsg.val = 'Name, machine, binary source, and required execution fields must be set.';
            throw new Error(errorMsg.val);
        }

        try {
            const cfg = await capi.postV1DeploymentCreate({yamlContent: formToYaml(form)});
            const targetVersion = createTargetVersion(form, selectedVersion, selectedVersionSourceKey);
            if (targetVersion && cfg?.id) {
                await capi.postV1DeploymentUpdate({
                    deploymentId: cfg.id,
                    targetVersion,
                    version: (cfg.version || 0) + 1,
                });
            }
        } catch (e) {
            errorMsg.val = e.message || 'Failed to create deployment';
            throw e;
        }
        if (onCreated) onCreated();
        onClose();
    };

    const createButton = spinnerButton("Create", doCreate, "btn-primary text-sm py-1.5 px-4", "button", () => !isFormValid(form, {machineOptions: machines.val}));
    createButton.dataset.testid = "create-deployment-submit";

    const backdrop = div({
        class: "fixed inset-0 bg-black/60 z-40",
        onclick: onClose,
    });

    const dialog = div(
        {class: "fixed inset-0 z-50 flex items-center justify-center p-4 md:p-8 pointer-events-none", "data-testid": "create-deployment-dialog"},
        div(
            {class: "bg-gray-900 border border-gray-700 rounded-xl shadow-2xl flex flex-row overflow-hidden pointer-events-auto",
             style: () => `width: ${form.envPaneOpen.val || form.assetMountsPaneOpen.val || form.volumeMountsPaneOpen.val || form.assetEditorOpen.val ? 1360 : 960}px; max-width: calc(100vw - 2rem); max-height: 88vh;`,
             onclick: (e) => e.stopPropagation()},
            div(
                {class: "flex-1 min-w-0 flex flex-col"},
                div(
                    {class: "flex-1 min-h-0 overflow-auto p-4 flex flex-col gap-5"},
                    () => deploymentForm(form, {
                        environmentOptions: environmentOptions(),
                        machineOptions: machines.val,
                        machineOptionsLoaded: machinesLoaded.val,
                        assets: assets.val,
                        enableAssetEditor: true,
                        showRunnerSummary: false,
                    }),
                    () => createVersionSection({
                        form,
                        selectedScope,
                        selectedVersion,
                        selectedVersionSourceKey,
                        loadingVersions,
                        onScopeChange: (scope) => loadSourceVersions(form, selectedScope, selectedVersion, selectedVersionSourceKey, loadingVersions, scope),
                        onRefresh: () => loadSourceVersions(form, selectedScope, selectedVersion, selectedVersionSourceKey, loadingVersions, selectedScope.val),
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
            envVarsPane(form),
            volumeMountsPane(form),
            () => assetMountsPane(form, {assets: assets.val, enableAssetEditor: true}),
            assetEditorPane(form, {onSaved: loadAssets}),
        ),
    );

    return div(backdrop, dialog);
}

function createTargetVersion(form, selectedVersion, selectedVersionSourceKey) {
    if (form.sourceType.val === SOURCE_DOCKER_IMAGE) {
        const explicitVersion = imageVersionFromReference(form.containerImage.val);
        if (explicitVersion) return explicitVersion;
    }
    const version = selectedVersion.val.trim();
    if (!version) return '';
    if (selectedVersionSourceKey.val !== sourceKey(form)) return '';
    const check = activeRepoCheck(form);
    if (check.status !== 'ok') return '';
    const versionsByScope = check.versionsByScope || {};
    const versions = Object.values(versionsByScope).flatMap(scope => scope?.versions || []);
    return versions.some(v => v.id === version) ? version : '';
}

function createVersionSection(args) {
    const sourceType = args.form.sourceType.val;
    if (sourceType === SOURCE_DOCKER_IMAGE) {
        const imageSet = Boolean(args.form.containerImage.val.trim());
        const explicitVersion = imageVersionFromReference(args.form.containerImage.val);
        const check = activeRepoCheck(args.form);
        const ready = check.status === 'ok';
        const versions = check.versions || [];
        const selectedVersion = args.selectedVersionSourceKey.val === sourceKey(args.form) ? args.selectedVersion.val : '';
        if (explicitVersion) {
            return div(
                {class: "flex flex-col gap-3"},
                sectionDivider("Version"),
                versionMessage(imageSet && check.status !== 'idle' ? versionStatusMessage(args.form, check) : (imageSet ? '' : 'Image not set')),
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
            versionMessage(imageSet ? versionStatusMessage(args.form, check) : 'Image not set'),
            div(
                {class: "grid grid-cols-1 md:grid-cols-[minmax(0,1fr)_auto] items-end gap-3"},
                label(
                    {class: "flex flex-col gap-1 text-xs text-gray-400"},
                    span("Tag"),
                    select({
                        class: selectClass(),
                        disabled: !ready || args.loadingVersions.val || versions.length === 0,
                        onchange: (e) => {
                            args.selectedVersion.val = e.target.value;
                            args.selectedVersionSourceKey.val = sourceKey(args.form);
                        },
                    },
                        option({value: '', disabled: true, selected: !selectedVersion}, versionPlaceholder(ready, args.loadingVersions.val, versions, "Image unavailable")),
                        ...versions.map(v => option({value: v.id, selected: v.id === selectedVersion}, versionLabel(v))),
                    ),
                ),
                refreshRow(!imageSet || args.loadingVersions.val, args.onRefresh),
            ),
        );
    }

    const check = activeRepoCheck(args.form);
    const ready = check.status === 'ok';
    const scopes = check.scopes || [];
    const scope = currentScope(check, args.selectedScope.val);
    const versions = ((check.versionsByScope || {})[scope]?.versions) || [];
    const sourceLabel = sourceType === SOURCE_GITHUB ? "Release" : "Commit";
    const selectedVersion = args.selectedVersionSourceKey.val === sourceKey(args.form) ? args.selectedVersion.val : '';

    return div(
        {class: "flex flex-col gap-3"},
        sectionDivider("Version"),
        versionMessage(versionStatusMessage(args.form, check)),
        div(
            {class: sourceType === SOURCE_GITHUB
                ? "grid grid-cols-1 md:grid-cols-[minmax(0,1fr)_auto] items-end gap-3"
                : "grid grid-cols-1 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] items-end gap-3"},
            sourceType === SOURCE_GITHUB
                ? ''
                : label(
                    {class: "flex flex-col gap-1 text-xs text-gray-400"},
                    span("Branch"),
                    select(
                        {
                            class: selectClass(),
                            disabled: !ready || scopes.length === 0 || args.loadingVersions.val,
                            onchange: (e) => args.onScopeChange(e.target.value),
                        },
                        option({value: '', selected: !scope}, scopes.length ? "Select a branch..." : "No branches loaded"),
                        ...scopes.map(s => option({value: s, selected: s === scope}, s)),
                    ),
                ),
            label(
                {class: "flex flex-col gap-1 text-xs text-gray-400"},
                span(sourceLabel),
                select(
                    {
                        class: selectClass(),
                        disabled: !ready || args.loadingVersions.val || versions.length === 0,
                        onchange: (e) => {
                            args.selectedVersion.val = e.target.value;
                            args.selectedVersionSourceKey.val = sourceKey(args.form);
                            validateSelectedCommit(args.form, scope, e.target.value);
                        },
                    },
                    option({value: '', disabled: true, selected: !selectedVersion}, versionPlaceholder(ready, args.loadingVersions.val, versions)),
                    ...versions.map(v => option({value: v.id, selected: v.id === selectedVersion}, versionLabel(v))),
                ),
            ),
            refreshRow(!currentRepo(args.form) || args.loadingVersions.val, args.onRefresh),
        ),
    );
}

async function loadSourceVersions(form, selectedScope, selectedVersion, selectedVersionSourceKey, loadingVersions, scope) {
    const repo = currentRepo(form);
    if (!repo) return;
    loadingVersions.val = true;
    const sourceType = form.sourceType.val;
    const sourceKey = sourceValidationKey(form);
    try {
        const res = await capi.postV1RepoValidate(buildValidateSourceRequest(form, {scope: scope || ''}));
        const sourceResult = validationSourceResult(form, res);
        form.repoCheck.val = sourceCheckFromValidation(form, res, repo, sourceType, sourceKey);
        if (form.repoCheck.val.status === 'ok') {
            const nextScope = sourceResult.scope || currentScope(form.repoCheck.val, scope || selectedScope.val);
            selectedScope.val = nextScope;
            const versions = sourceResult.versions || [];
            if (selectedVersionSourceKey.val !== sourceKey(form) || !versions.some(v => v.id === selectedVersion.val)) {
                selectedVersion.val = versions[0]?.id || '';
                selectedVersionSourceKey.val = selectedVersion.val ? sourceKey(form) : '';
            }
        }
    } catch (e) {
        form.repoCheck.val = {status: 'error', message: e.message || 'Validation failed.', repo, sourceType, sourceKey};
    }
    loadingVersions.val = false;
}

function activeRepoCheck(form) {
    const c = form.repoCheck.val;
    const repo = currentRepo(form);
    if (!repo || c.sourceType !== form.sourceType.val || c.repo !== repo || c.sourceKey !== sourceValidationKey(form)) {
        return {status: repo ? 'idle' : 'empty', message: ''};
    }
    return c;
}

function currentRepo(form) {
    if (form.sourceType.val === SOURCE_DOCKER_IMAGE) return form.containerImage.val.trim();
    return (form.sourceType.val === SOURCE_GITHUB ? form.githubRepo.val : form.nixRepo.val).trim();
}

function sourceKey(form) {
    return sourceValidationKey(form);
}

function currentScope(check, selectedScope) {
    if (check.status !== 'ok') return '';
    if (selectedScope && (check.versionsByScope || {})[selectedScope]) return selectedScope;
    const keys = Object.keys(check.versionsByScope || {});
    if (keys.length > 0) return keys[0];
    if ((check.scopes || []).includes('main')) return 'main';
    return (check.scopes || [])[0] || '';
}

function versionStatusMessage(form, check) {
    if (!currentRepo(form)) return form.sourceType.val === SOURCE_DOCKER_IMAGE ? 'Image not set' : 'Repository not set';
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
        }, RefreshCw({size: 12}), "Refresh"),
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
    return "w-full h-9 px-3 rounded-lg bg-gray-800 text-gray-100 border border-gray-600 focus:outline-none focus:ring-1 focus:ring-brand";
}
