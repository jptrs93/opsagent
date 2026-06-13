import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {deploymentsS} from "../state/deployments.js";
import {spinnerButton} from "./spinnerbutton.js";
import {RefreshCw} from "vanjs-feather";
import {
    buildValidateSourceRequest,
    configToYaml,
    deploymentConfigToForm,
    deploymentForm,
    envVarsPane,
    formToYaml,
    imageVersionFromReference,
    isFormValid,
    sectionDivider,
    sourceCheckFromValidation,
    sourceValidationKey,
    validateSelectedCommit,
    validationSourceResult,
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
    const initialYaml = configToYaml(deploymentConfig);
    const scopes = van.state([]);
    const selectedScope = van.state('');
    const versions = van.state([]);
    const selectedVersion = van.state(deployment.variant === 'containerImage' ? (imageVersionFromReference(form.containerImage.val) || deployment.deployedVersion || '') : '');
    const loadingVersions = van.state(false);
    const versionError = van.state('');
    const errorMsg = van.state('');
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

    const loadVersions = async (scope) => {
        const sourceID = currentSourceID(form);
        if (!sourceID) {
            versionError.val = form.sourceType.val === 'containerImage' ? 'Image not set' : 'Repository not set';
            versions.val = [];
            return;
        }
        loadingVersions.val = true;
        versionError.val = '';
        const sourceType = form.sourceType.val;
        const sourceKey = sourceValidationKey(form);
        try {
            const result = await capi.postV1RepoValidate(buildValidateSourceRequest(form, {scope: scope || ''}));
            const sourceResult = validationSourceResult(form, result);
            form.repoCheck.val = sourceCheckFromValidation(form, result, sourceID, sourceType, sourceKey);
            if (form.repoCheck.val.status !== 'ok') {
                versionError.val = form.repoCheck.val.message || 'Unable to connect to source repository.';
                scopes.val = sourceResult.scopes || [];
                versions.val = [];
            } else {
                scopes.val = sourceResult.scopes || [];
                const scopeKey = sourceResult.scope || '';
                const vsList = sourceResult.versions || [];
                versions.val = vsList;
                selectedScope.val = scopeKey;
                const deployedId = deployment.deployedVersion || '';
                if (deployedId && vsList.some(v => v.id === deployedId)) {
                    selectedVersion.val = deployedId;
                }
            }
        } catch (e) {
            versionError.val = e.message || 'Failed to load versions';
            form.repoCheck.val = {status: 'error', message: versionError.val, repo: sourceID, sourceType, sourceKey};
            versions.val = [];
        }
        loadingVersions.val = false;
    };

    if (deployment.variant) {
        loadVersions('');
    }

    const onScopeChange = (e) => {
        selectedScope.val = e.target.value;
        loadVersions(e.target.value);
    };

    const doDeploy = async () => {
        errorMsg.val = '';
        if (!isFormValid(form)) {
            errorMsg.val = 'Binary source and required execution fields must be set.';
            throw new Error(errorMsg.val);
        }

        const payload = {
            deploymentId: deployment.id,
            version: deployment.currentVersion + 1,
        };

        const nextYaml = formToYaml(form);
        if (nextYaml !== initialYaml) {
            payload.yamlContent = nextYaml;
        }
        const targetVersion = imageVersionFromReference(form.containerImage.val) || selectedVersion.val.trim();
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
             style: () => `width: ${form.envPaneOpen.val ? 1360 : 960}px; max-width: calc(100vw - 2rem); max-height: 88vh;`,
             onclick: (e) => e.stopPropagation()},
            div(
                {class: "flex-1 min-w-0 flex flex-col"},
                div(
                    {class: "flex-1 min-h-0 overflow-auto p-4 flex flex-col gap-5"},
                    deploymentForm(form, {identityLocked: true, environmentOptions: environmentOptions()}),
                    hasVersions ? versionSection({
                        form,
                        scopes,
                        selectedScope,
                        versions,
                        selectedVersion,
                        loadingVersions,
                        versionError,
                        sourceType: form.sourceType,
                        deployedVersion: deployment.deployedVersion || '',
                        onScopeChange,
                        onVersionChange: (version) => validateSelectedCommit(form, selectedScope.val, version),
                        onRefresh: () => loadVersions(selectedScope.val),
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
                        spinnerButton("Update deployment", doDeploy, "btn-primary text-sm py-1.5 px-4", "button", () => !isFormValid(form)),
                    ),
                ),
            ),
            envVarsPane(form),
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
    if (form.sourceType.val === 'githubRelease') return form.githubRepo.val.trim();
    return form.nixRepo.val.trim();
}

function selectClass() {
    return "w-full h-9 px-3 rounded-lg bg-gray-800 text-gray-100 border border-gray-600 focus:outline-none focus:ring-1 focus:ring-brand";
}
