import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {deploymentsS} from "../state/deployments.js";
import {spinnerButton} from "./spinnerbutton.js";
import {RefreshCw} from "vanjs-feather";
import {
    configToYaml,
    deploymentConfigToForm,
    deploymentForm,
    formToYaml,
    isFormValid,
} from "./deploymentForm.js";

const { div, h3, span, select, option, button, p, label } = van.tags;

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
    const selectedVersion = van.state('');
    const loadingVersions = van.state(false);
    const versionError = van.state('');
    const errorMsg = van.state('');
    const isRunning = deployment.existingStatus === STATUS_RUNNING;
    const canManageLifecycle = deployment.runnerType !== 'systemd';
    const canStart = Boolean(deployment.deployedVersion);

    const environmentOptions = () => {
        const envs = new Set();
        for (const d of deploymentsS.val || []) {
            const env = d.config?.configId?.environment;
            if (env) envs.add(env);
        }
        return [...envs].sort();
    };

    const loadVersions = async (scope) => {
        loadingVersions.val = true;
        versionError.val = '';
        try {
            const result = await capi.postV1DeploymentVersions({
                deploymentId: deployment.id,
                scope: scope || '',
            });
            scopes.val = result?.scopes || [];
            const byScope = result?.versionsByScope || {};
            const scopeKey = scope || Object.keys(byScope)[0] || '';
            const sv = byScope[scopeKey];
            const vsList = sv?.versions || [];
            versions.val = vsList;
            if (!selectedScope.val && scopeKey) {
                selectedScope.val = scopeKey;
            }
            const deployedId = deployment.deployedVersion || '';
            if (deployedId && vsList.some(v => v.id === deployedId)) {
                selectedVersion.val = deployedId;
            }
        } catch (e) {
            versionError.val = e.message || 'Failed to load versions';
            versions.val = [];
        }
        loadingVersions.val = false;
    };

    if (deployment.variant) {
        setTimeout(() => loadVersions(''), 0);
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
        if (selectedVersion.val) {
            payload.targetVersion = selectedVersion.val;
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
            {class: "bg-gray-950 border border-gray-700 rounded-xl shadow-2xl flex flex-col pointer-events-auto",
             style: "width: 760px; max-width: calc(100vw - 2rem); max-height: 88vh;",
             onclick: (e) => e.stopPropagation()},
            div(
                {class: "flex-1 min-h-0 overflow-auto p-4 flex flex-col gap-3"},
                deploymentForm(form, {identityLocked: true, environmentOptions: environmentOptions()}),
                hasVersions ? versionSection({
                    scopes,
                    selectedScope,
                    versions,
                    selectedVersion,
                    loadingVersions,
                    versionError,
                    sourceType: form.sourceType,
                    deployedVersion: deployment.deployedVersion || '',
                    onScopeChange,
                    onRefresh: () => loadVersions(selectedScope.val),
                }) : null,
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
    return div(
        {class: "rounded-lg border border-gray-700 bg-gray-900/70 p-4"},
        h3({class: "text-sm font-semibold text-gray-200 mb-3"}, "Version"),
        div(
            {class: "flex items-center gap-3"},
            div(
                {class: "flex-1 flex flex-col gap-3"},
                () => {
                    const s = args.scopes.val;
                    if (s.length === 0) return span();
                    return label(
                        {class: "grid grid-cols-[7rem_1fr] items-center gap-3 text-xs text-gray-400"},
                        span("Branch"),
                        select(
                            {
                                class: selectClass(),
                                onchange: args.onScopeChange,
                            },
                            ...s.map(b => option({value: b, selected: b === args.selectedScope.val}, b)),
                        ),
                    );
                },
                () => {
                    if (args.loadingVersions.val) {
                        return p({class: "text-xs text-gray-500"}, "Loading versions...");
                    }
                    if (args.versionError.val) {
                        return p({class: "text-xs text-red-400"}, args.versionError.val);
                    }
                    const vs = args.versions.val;
                    return label(
                        {class: "grid grid-cols-[7rem_1fr] items-center gap-3 text-xs text-gray-400"},
                        span(() => args.sourceType.val === 'githubRelease' ? "Release" : "Commit"),
                        select(
                            {
                                class: selectClass(),
                                onchange: (e) => { args.selectedVersion.val = e.target.value; },
                            },
                            option({value: '', disabled: true, selected: true}, vs.length ? "Select a version..." : "No versions loaded"),
                            ...vs.map(v => option({value: v.id, selected: v.id === args.deployedVersion}, versionLabel(v))),
                        ),
                    );
                },
            ),
            div(
                {class: "flex items-center self-stretch"},
                button({
                    class: "inline-flex h-9 items-center justify-center gap-1.5 px-3 rounded-lg text-xs text-gray-300 bg-gray-800 border border-gray-600 hover:bg-gray-700 transition-colors cursor-pointer",
                    onclick: args.onRefresh,
                    type: "button",
                    title: "Refresh available versions",
                }, RefreshCw({size: 12}), "Refresh"),
            ),
        ),
    );
}

function selectClass() {
    return "w-full h-9 px-3 rounded-lg bg-gray-800 text-gray-100 border border-gray-600 focus:outline-none focus:ring-1 focus:ring-brand";
}
