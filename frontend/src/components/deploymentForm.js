import van from "vanjs-core";
import {X} from "vanjs-feather";
import {capi} from "../capi/index.js";

const { div, h3, label, input, select, option, button, p, span, datalist, textarea } = van.tags;

const SOURCE_NIX_DOCKER = 'nixDockerBuild';
const SOURCE_DOCKER_IMAGE = 'containerImage';
const RUNNER_CONTAINER = 'container';

let nextDatalistID = 1;
let nextEnvID = 1;
let nextAssetMountID = 1;
let nextVolumeMountID = 1;

export function emptyDeploymentForm() {
    return makeFormState({
        name: '',
        environment: '',
        machine: '',
        sourceType: SOURCE_DOCKER_IMAGE,
        nixRepo: '',
        nixFlake: '',
        containerImage: '',
        runnerType: RUNNER_CONTAINER,
        containerUser: '',
        containerDataMountPath: '',
        containerDisableDataVolume: false,
        assetMounts: [],
        volumeMounts: [],
    });
}

export function deploymentConfigToForm(cfg) {
    const cid = cfg?.configId || {};
    const spec = cfg?.spec || {};
    const prepare = spec.prepare || {};
    const runner = spec.runner || {};
    const nixDocker = prepare.nixDockerBuild || {};
    const containerImage = prepare.containerImage || {};
    const container = runner.container || {};

    // Reveal a section's additional options up-front when the existing config
    // already sets one of them, so they aren't hidden on edit.
    const showSourceOpts = false;
    const showExecOpts = false;

    return makeFormState({
        name: cid.name || '',
        environment: cid.environment || '',
        machine: cid.machine || '',
        sourceType: prepare.containerImage ? SOURCE_DOCKER_IMAGE : SOURCE_NIX_DOCKER,
        nixRepo: nixDocker.repo || '',
        nixFlake: nixDocker.flake || '',
        containerImage: containerImage.image || '',
        runnerType: RUNNER_CONTAINER,
        containerUser: container.user || '',
        containerDataMountPath: container.dataMountPath || '',
        containerDisableDataVolume: Boolean(container.disableDataVolume),
        envVars: (container.env || []).map(e => ({id: nextEnvID++, key: e.key || '', value: e.value || ''})),
        assetMounts: (container.assetMounts || []).map(m => {
            const row = {id: nextAssetMountID++, assetId: m.assetId || 0, key: m.asset || '', path: m.path || '', version: m.version || 0};
            return {...row, originalAssetId: row.assetId, originalKey: row.key, originalPath: row.path, originalVersion: row.version};
        }),
        volumeMounts: (container.mounts || []).map(m => ({id: nextVolumeMountID++, host: m.host || '', container: m.container || '', readonly: Boolean(m.readonly)})),
        showSourceOpts,
        showExecOpts,
    });
}

export function deploymentForm(form, opts = {}) {
    const environmentDatalistID = `deployment-environments-${nextDatalistID++}`;
    const identityLocked = Boolean(opts.identityLocked);
    const environmentOptions = opts.environmentOptions || [];
    const machineOptions = opts.machineOptions || [];
    const machineOptionValues = machineOptions.map(m => typeof m === 'string' ? m : m.name).filter(Boolean);
    const machineOptionsLoaded = opts.machineOptionsLoaded !== false;
    const executionTitle = opts.executionTitle || "Environment";
    const showIdentityLockedNotice = () => {
        if (!identityLocked) return;
        form.identityLockNotice.val = true;
        if (form.identityLockNoticeTimer) clearTimeout(form.identityLockNoticeTimer);
        form.identityLockNoticeTimer = setTimeout(() => {
            form.identityLockNotice.val = false;
            form.identityLockNoticeTimer = null;
        }, 5000);
    };

    return div(
        {class: "flex flex-col gap-5"},
        sectionDivider("Deployment identity"),
        div(
            {class: "flex flex-col gap-3"},
            div(
                {class: "grid grid-cols-1 md:grid-cols-3 gap-3"},
                identityField("Name", input({
                    type: "text",
                    "data-testid": "deployment-name-input",
                    value: form.name.rawVal,
                    disabled: identityLocked,
                    class: () => textInputClass(false, identityLocked, !identityLocked && nameValid(form)),
                    placeholder: "my-service",
                    oninput: e => { form.name.val = e.target.value; },
                }), identityLocked, showIdentityLockedNotice),
                identityField("Environment (optional)", div(
                    input({
                        type: "text",
                        "data-testid": "deployment-environment-input",
                        list: environmentDatalistID,
                        value: form.environment.rawVal,
                        disabled: identityLocked,
                        class: textInputClass(false, identityLocked),
                        placeholder: "PROD",
                        oninput: e => { form.environment.val = e.target.value; },
                    }),
                    datalist(
                        {id: environmentDatalistID},
                        ...environmentOptions.map(env => option({value: env}, env)),
                    ),
                ), identityLocked, showIdentityLockedNotice),
                identityField("Machine", machineSelect(form, {
                    identityLocked,
                    machineOptionsLoaded,
                    machineOptionValues,
                }), identityLocked, showIdentityLockedNotice),
            ),
            () => identityLocked && form.identityLockNotice.val
                ? span({class: "text-xs text-red-400 -mb-2"}, "Deployment identity is fixed after creation.")
                : '',
        ),
        opts.hideArtifactSource ? '' : div(
            {class: "flex flex-col gap-5"},
            sectionDivider("Artifact source"),
            div(
                {class: "flex flex-col gap-3"},
                div(
                    {class: "flex items-start gap-4"},
                    selectField("Source type", form.sourceType, [
                        {value: SOURCE_NIX_DOCKER, label: "Build NIX Docker image"},
                        {value: SOURCE_DOCKER_IMAGE, label: "Docker image"},
                    ], "w-56", value => {
                        form.runnerType.val = runnerForSource(value);
                    }),
                    () => form.sourceType.val === SOURCE_DOCKER_IMAGE
                        ? dockerImageField(form)
                        : repoField(form, form.sourceType.val),
                ),
                () => nixSourceFields(form),
            ),
        ),
        opts.hideExecution ? '' : div(
            {class: "flex flex-col gap-5"},
            sectionDivider(executionTitle),
            div(
                {class: "flex flex-col gap-3"},
                div(
                    {class: "flex flex-col gap-3"},
                    envSummary(form),
                    volumeMountsSummary(form),
                    assetMountsSection(form, opts),
                ),
            ),
        ),
    );
}

export function formToDeploymentIdentifier(form) {
    return {
        name: form.name.val.trim(),
        environment: form.environment.val.trim(),
        machine: form.machine.val.trim(),
    };
}

export function formToSpec(form) {
    const spec = {
        prepare: {},
        runner: {},
    };

    if (form.sourceType.val === SOURCE_NIX_DOCKER) {
        spec.prepare.nixDockerBuild = {
            repo: form.nixRepo.val.trim(),
            flake: form.nixFlake.val.trim(),
        };
    } else if (form.sourceType.val === SOURCE_DOCKER_IMAGE) {
        spec.prepare.containerImage = {
            image: form.containerImage.val.trim(),
        };
    }

    spec.runner.container = {
        disableDataVolume: Boolean(form.containerDisableDataVolume.val),
    };
    const user = form.containerUser.val.trim();
    if (user) spec.runner.container.user = user;
    const dataMountPath = form.containerDataMountPath.val.trim();
    if (dataMountPath) spec.runner.container.dataMountPath = dataMountPath;
    const env = formEnvVars(form);
    if (env.length) spec.runner.container.env = env;
    const mounts = formVolumeMounts(form);
    if (mounts.length) spec.runner.container.mounts = mounts;
    const assetMounts = formAssetMounts(form);
    if (assetMounts.length) spec.runner.container.assetMounts = assetMounts;

    return spec;
}

export function isFormValid(form, opts = {}) {
    if (!nameValid(form) || !form.machine.val.trim()) return false;
    const machineOptions = opts.machineOptions || [];
    const machineOptionValues = machineOptions.map(m => typeof m === 'string' ? m : m.name).filter(Boolean);
    if (machineOptionValues.length > 0 && !machineOptionValues.includes(form.machine.val.trim())) return false;
    if (form.sourceType.val === SOURCE_DOCKER_IMAGE) {
        if (!form.containerImage.val.trim()) return false;
    } else if (!form.nixRepo.val.trim() || !form.nixFlake.val.trim()) {
        return false;
    }
    if (hasInvalidVolumeConfig(form) || hasInvalidAssetMounts(form)) return false;
    return true;
}

export function buildValidateSourceRequest(form, opts = {}) {
    const sourceType = form.sourceType.val;
    const branch = (opts.scope || '').trim();
    const commit = (opts.commit || '').trim();
    if (sourceType === SOURCE_NIX_DOCKER) {
        return {nixDockerBuild: {
            repoUrl: form.nixRepo.val.trim(),
            branch,
            commit,
            flakePath: form.nixFlake.val.trim(),
        }};
    }
    if (sourceType === SOURCE_DOCKER_IMAGE) {
        return {containerImage: {image: form.containerImage.val.trim()}};
    }
    return {containerImage: {image: form.containerImage.val.trim()}};
}

export function sourceValidationKey(form) {
    const sourceType = form.sourceType.val;
    if (sourceType === SOURCE_DOCKER_IMAGE) {
        return `${sourceType}:${form.containerImage.val.trim()}`;
    }
    const repo = form.nixRepo.val.trim();
    const flake = sourceType === SOURCE_NIX_DOCKER ? form.nixFlake.val.trim() : '';
    return `${sourceType}:${repo}:${flake}`;
}

export function imageVersionFromReference(raw) {
    let image = (raw || '').trim();
    image = image.replace(/^docker:\/\//, '').replace(/^https?:\/\//, '').replace(/\/$/, '');
    const digestIdx = image.indexOf('@');
    if (digestIdx >= 0) return image.slice(digestIdx + 1);
    const lastSlash = image.lastIndexOf('/');
    const lastColon = image.lastIndexOf(':');
    if (lastColon > lastSlash) return image.slice(lastColon + 1);
    return '';
}

export function validationSourceResult(form, res) {
    switch (form.sourceType.val) {
        case SOURCE_NIX_DOCKER:
            return res?.nixDockerBuild || {};
        case SOURCE_DOCKER_IMAGE:
            return res?.containerImage || {};
        default:
            return {};
    }
}

export function validationVersionsByScope(sourceResult) {
    return {[sourceResult?.scope || '']: {versions: sourceResult?.versions || []}};
}

export function sourceCheckFromValidation(form, res, repo, sourceType, sourceKey) {
    const sourceResult = validationSourceResult(form, res);
    const gitRepository = sourceResult.gitRepository || sourceResult.image || {ok: false, message: 'Source not accessible.'};
    const nixFlakeFile = sourceResult.nixFlakeFile || {ok: false, message: ''};
    const flakeRequired = sourceType === SOURCE_NIX_DOCKER;
    const ok = Boolean(gitRepository.ok && (!flakeRequired || !form.nixFlake.val.trim() || nixFlakeFile.ok));
    return {
        status: ok ? 'ok' : 'error',
        message: gitRepository.message || (gitRepository.ok ? 'Repo accessible.' : 'Source not accessible.'),
        repo,
        sourceType,
        sourceKey,
        gitRepository,
        nixFlakeFile,
        scopes: sourceResult.scopes || [],
        scope: sourceResult.scope || '',
        versions: sourceResult.versions || [],
        versionsByScope: validationVersionsByScope(sourceResult),
    };
}

export async function validateSelectedCommit(form, scope, commit) {
    const sourceType = form.sourceType.val;
    if (sourceType !== SOURCE_NIX_DOCKER) return;
    const repo = form.nixRepo.val.trim();
    const selectedCommit = (commit || '').trim();
    if (!repo || !selectedCommit) return;

    const sourceKey = sourceValidationKey(form);
    const previous = form.repoCheck.val;
    form.repoCheck.val = {
        ...previous,
        status: 'checking',
        message: previous.message || 'Checking repository access…',
        repo,
        sourceType,
        sourceKey,
    };
    try {
        const res = await capi.postV1RepoValidate(buildValidateSourceRequest(form, {scope, commit: selectedCommit}));
        form.repoCheck.val = sourceCheckFromValidation(form, res, repo, sourceType, sourceKey);
    } catch (e) {
        form.repoCheck.val = {status: 'error', message: e.message || 'Validation failed.', repo, sourceType, sourceKey};
    }
}

function makeFormState(values) {
    return {
        name: van.state(values.name),
        environment: van.state(values.environment),
        machine: van.state(values.machine),
        sourceType: van.state(values.sourceType),
        nixRepo: van.state(values.nixRepo),
        nixFlake: van.state(values.nixFlake),
        containerImage: van.state(values.containerImage || ''),
        runnerType: van.state(values.runnerType),
        containerUser: van.state(values.containerUser || ''),
        containerDataMountPath: van.state(values.containerDataMountPath || ''),
        containerDisableDataVolume: van.state(Boolean(values.containerDisableDataVolume)),
        envVars: van.state(values.envVars || []),
        assetMounts: van.state(values.assetMounts || []),
        volumeMounts: van.state(values.volumeMounts || []),
        showSourceOpts: van.state(Boolean(values.showSourceOpts)),
        showExecOpts: van.state(Boolean(values.showExecOpts)),
        identityLockNotice: van.state(false),
        identityLockNoticeTimer: null,
        // Whether the environment-variables editor pane is open in the overlay.
        envPaneOpen: van.state(false),
        assetMountsPaneOpen: van.state(false),
        volumeMountsPaneOpen: van.state(false),
        assetEditorOpen: van.state(false),
        assetEditorMountID: van.state(0),
        assetEditorError: van.state(''),
        assetEditorKey: van.state(''),
        assetEditorFormat: van.state('text'),
        assetEditorContent: van.state(''),
        // Transient repo-accessibility check; tracks the repo/source it applies
        // to so a stale result is hidden once the inputs change.
        repoCheck: van.state({status: 'idle', message: '', repo: '', sourceType: '', sourceKey: ''}),
    };
}

// sectionDivider renders a thin horizontal rule with the section title centered,
// splitting the line: ──────── Title ────────
export function sectionDivider(title) {
    return div(
        {class: "flex items-center gap-3"},
        div({class: "flex-1 border-t border-gray-700"}),
        span({class: "text-xs font-semibold uppercase tracking-wide text-gray-400"}, title),
        div({class: "flex-1 border-t border-gray-700"}),
    );
}

function field(text, control, hint) {
    return label(
        {class: "flex flex-col gap-1 text-xs text-gray-400"},
        span(text),
        control,
        hint ? span({class: "text-[11px] text-gray-500"}, hint) : '',
    );
}

function identityField(text, control, locked, onLockedClick) {
    return label(
        {class: "flex flex-col gap-1 text-xs text-gray-400", onpointerdown: locked ? onLockedClick : undefined},
        span(text),
        control,
    );
}

// --- Additional (optional) options -----------------------------------------

// optionsDisclosure renders an expand/collapse toggle (no surrounding card or
// rule) that reveals a section's optional fields.
//
// The content node is always mounted and toggled via a CSS class rather than
// conditionally returned: a child binding that returns null gets a null _dom,
// which VanJS's keepConnected() GC drops on the next update cycle — leaving the
// disclosure unable to re-render.
function optionsDisclosure(open, content) {
    return div(
        {class: "pt-1"},
        button({
            type: "button",
            class: "flex items-center gap-1.5 text-xs text-gray-400 hover:text-gray-200 cursor-pointer",
            onclick: () => { open.val = !open.val; },
        },
            span({class: "text-[10px] leading-none"}, () => open.val ? "▼" : "▶"),
            span("Additional options"),
        ),
        div({class: () => open.val ? "flex flex-col gap-3 mt-3" : "hidden"}, content()),
    );
}

function nixSourceFields(form) {
    if (form.sourceType.val === SOURCE_NIX_DOCKER) {
        return flakeField(form);
    }
    return '';
}

function runnerForSource(sourceType) {
    return RUNNER_CONTAINER;
}

// envSummary shows the configured env-var count and a link that opens the
// editor pane (rendered at the overlay level via envVarsPane).
function envSummary(form) {
    return div(
        {class: "flex items-center justify-between gap-3"},
        span({class: "text-xs text-gray-400"}, () => {
            const n = envVarCount(form.envVars.val);
            return n === 0 ? "No environment variables" : `${n} environment variable${n === 1 ? '' : 's'}`;
        }),
        button({
            "data-testid": "deployment-env-vars-toggle",
            type: "button",
            class: "text-xs text-blue-400 hover:text-blue-300 cursor-pointer",
            onclick: () => {
                form.envPaneOpen.val = !form.envPaneOpen.val;
                if (form.envPaneOpen.val) closeRuntimePanes(form, 'env');
            },
        }, () => form.envPaneOpen.val ? "Close" : "View / edit"),
    );
}

function assetMountsSection(form, opts = {}) {
    const rows = () => form.assetMounts.val || [];
    const validRows = () => rows().filter(m => m && m.key && m.path);
    return div(
        {class: "flex items-center justify-between gap-3"},
        span({class: "text-xs text-gray-400"}, () => {
            const n = validRows().length;
            return n === 0 ? "No mounted assets" : `${n} mounted asset${n === 1 ? '' : 's'}`;
        }),
        button({
            type: "button",
            class: "text-xs text-blue-400 hover:text-blue-300 cursor-pointer",
            onclick: () => {
                if (rows().length === 0) {
                    form.assetMounts.val = [...rows(), {id: nextAssetMountID++, assetId: 0, key: '', path: '', version: 0}];
                }
                form.assetMountsPaneOpen.val = !form.assetMountsPaneOpen.val;
                if (form.assetMountsPaneOpen.val) closeRuntimePanes(form, 'assets');
            },
        }, () => form.assetMountsPaneOpen.val ? "Close" : (validRows().length ? "Click to manage" : "Click to mount assets")),
    );
}

function volumeMountsSummary(form) {
    const summaryText = () => {
        const n = form.containerDisableDataVolume.val ? 0 : 1;
        return `${n} mounted volume${n === 1 ? '' : 's'}`;
    };
    return div(
        {class: "flex items-center justify-between gap-3"},
        span({class: "text-xs text-gray-400"}, summaryText),
        button({
            type: "button",
            class: "text-xs text-blue-400 hover:text-blue-300 cursor-pointer",
            onclick: () => {
                form.volumeMountsPaneOpen.val = !form.volumeMountsPaneOpen.val;
                if (form.volumeMountsPaneOpen.val) closeRuntimePanes(form, 'volumes');
            },
        }, () => form.volumeMountsPaneOpen.val ? "Close" : "Click to manage"),
    );
}

function defaultVolumeCard(form) {
    if (form.containerDisableDataVolume.val) {
        return div(
            {class: "rounded-lg border border-gray-700 bg-gray-900/60 p-3 flex flex-col gap-1"},
            div({class: "flex items-center justify-between gap-3"},
                span({class: "text-xs font-medium text-gray-200"}, "deployment-default"),
                span({class: "text-[11px] text-gray-500"}, "Disabled"),
            ),
            p({class: "text-[11px] text-gray-500"}, "This deployment has disabled the built-in writable data volume."),
        );
    }
    return div(
        {class: "rounded-lg border border-gray-700 bg-gray-900/60 p-3 flex flex-col gap-2"},
        div(
            {class: "grid grid-cols-1 md:grid-cols-2 gap-3"},
            field("Volume", input({
                class: textInputClass(false, true),
                value: "deployment-default",
                disabled: true,
            })),
            field("Container path", input({
                class: textInputClass(),
                placeholder: defaultVolumeFallbackContainerPath(form),
                value: form.containerDataMountPath.rawVal,
                oninput: e => { form.containerDataMountPath.val = e.target.value; },
            })),
        ),
    );
}

export function assetMountsPane(form, opts = {}) {
    const assets = opts.assets || [];
    const enableAssetEditor = Boolean(opts.enableAssetEditor);
    const addMount = () => {
        const row = {id: nextAssetMountID++, assetId: 0, key: '', path: '', version: 0};
        form.assetMounts.val = [...(form.assetMounts.val || []), row];
        return row;
    };
    const updateMount = (row, patch) => {
        form.assetMounts.val = form.assetMounts.val.map(m => m.id === row.id ? {...m, ...patch} : m);
    };
    const removeMount = (row) => {
        form.assetMounts.val = form.assetMounts.val.filter(m => m.id !== row.id);
    };
    const discardMountChanges = (row) => {
        updateMount(row, {
            assetId: row.originalAssetId || 0,
            key: row.originalKey || '',
            path: row.originalPath || '',
            version: row.originalVersion || 0,
        });
    };
    const openAssetEditor = (row) => {
        form.assetEditorMountID.val = row.id;
        form.assetEditorKey.val = row.key || '';
        form.assetEditorFormat.val = 'text';
        form.assetEditorContent.val = '';
        form.assetEditorError.val = '';
        form.assetEditorOpen.val = true;
        closeRuntimePanes(form, 'assetEditor');
    };
    const onAssetSelect = (row, value) => {
        const match = assets.find(a => a.key === value);
        updateMount(row, {key: value, assetId: match?.id || 0, version: match?.version || 0});
    };

    const rows = () => form.assetMounts.val || [];
    const rowEl = (row) => div(
        {class: "rounded-lg border border-gray-700 bg-gray-900/60 p-3 flex flex-col gap-2"},
        div(
            {class: "grid grid-cols-1 md:grid-cols-2 gap-3"},
            field("Asset", select({
                class: `${selectClass()} ${assets.length === 0 ? 'opacity-70 cursor-not-allowed' : ''}`,
                disabled: assets.length === 0,
                value: row.key,
                onchange: e => onAssetSelect(row, e.target.value),
            },
                option({value: '', disabled: true, selected: !row.key || assets.length === 0}, assets.length ? "Select an asset..." : "No assets defined"),
                ...assets.map(a => option({value: a.key, selected: a.key === row.key}, `${a.key} v${a.version}`)),
            )),
            field("Container path", input({
                class: textInputClass(true),
                placeholder: "/etc/nginx/nginx.conf",
                value: row.path,
                oninput: e => updateMount(row, {path: e.target.value}),
            })),
        ),
        div({class: "flex items-center justify-between gap-2"},
            span({class: () => newInvalidAssetMount(row, assets) ? "text-[11px] text-amber-400" : "text-[11px] text-gray-500"}, () => {
                if (newInvalidAssetMount(row, assets)) return "No valid asset selected, this mount won't be saved.";
                return row.version ? `Version ${row.version}` : '';
            }),
            div({class: "flex items-center gap-3"},
                () => savedAssetMountEdited(row) ? button({
                    type: "button",
                    class: "text-xs text-gray-400 hover:text-gray-200 cursor-pointer",
                    onclick: () => discardMountChanges(row),
                }, "Discard changes") : '',
                button({
                    type: "button",
                    class: "text-xs text-gray-500 hover:text-red-400 cursor-pointer",
                    onclick: () => removeMount(row),
                }, "Remove"),
            ),
        ),
    );
    return div(
        {class: () => form.assetMountsPaneOpen.val
            ? "w-1/2 shrink-0 border-l border-gray-700 flex flex-col"
            : "hidden"},
        div(
            {class: "flex items-center justify-between gap-3 px-4 py-3 border-b border-gray-700"},
            h3({class: "text-sm font-semibold text-gray-200"}, "Mounted assets"),
            button({
                type: "button",
                class: "text-gray-500 hover:text-gray-200 cursor-pointer",
                title: "Close",
                onclick: () => { form.assetMountsPaneOpen.val = false; },
            }, X({size: 16})),
        ),
        div(
            {class: "flex-1 min-h-0 overflow-auto flex flex-col gap-3 p-4"},
            () => div({class: "flex flex-col gap-3"}, ...rows().map(rowEl)),
            div({class: "flex items-center justify-between gap-2"},
                button({
                    type: "button",
                    class: "text-xs text-blue-400 hover:text-blue-300 cursor-pointer",
                    onclick: addMount,
                }, "Add mount"),
                enableAssetEditor ? button({
                    type: "button",
                    class: "text-xs text-gray-400 hover:text-gray-200 cursor-pointer",
                    onclick: () => openAssetEditor(addMount()),
                }, "Create new asset") : '',
            ),
        ),
    );
}

function savedAssetMountEdited(row) {
    if (row.originalKey === undefined) return false;
    return (row.assetId || 0) !== (row.originalAssetId || 0)
        || (row.key || '') !== (row.originalKey || '')
        || (row.path || '') !== (row.originalPath || '')
        || (row.version || 0) !== (row.originalVersion || 0);
}

function newInvalidAssetMount(row, assets) {
    if (row.originalKey !== undefined) return false;
    return !assets.some(a => a.key === row.key);
}

export function volumeMountsPane(form) {
    return div(
        {class: () => form.volumeMountsPaneOpen.val
            ? "w-1/2 shrink-0 border-l border-gray-700 flex flex-col"
            : "hidden"},
        div(
            {class: "flex items-center justify-between gap-3 px-4 py-3 border-b border-gray-700"},
            h3({class: "text-sm font-semibold text-gray-200"}, "Mounted volumes"),
            button({
                type: "button",
                class: "text-gray-500 hover:text-gray-200 cursor-pointer",
                title: "Close",
                onclick: () => { form.volumeMountsPaneOpen.val = false; },
            }, X({size: 16})),
        ),
        div(
            {class: "flex-1 min-h-0 overflow-auto flex flex-col gap-3 p-4"},
            p({class: "text-[11px] text-gray-500"}, "Volumes are named storage directories mounted into the container at a chosen location. They are persistent across container restarts."),
            defaultVolumeCard(form),
            p({class: "text-[11px] text-gray-500"}, "Additional named volumes will be managed here later."),
        ),
    );
}

export function assetEditorPane(form, opts = {}) {
    const save = async () => {
        const key = form.assetEditorKey.val.trim();
        if (!key) {
            form.assetEditorError.val = 'Asset key is required';
            return;
        }
        try {
            form.assetEditorError.val = '';
            const asset = await capi.postV1AssetsSet({
                key,
                format: form.assetEditorFormat.val.trim() || 'text',
                blob: new TextEncoder().encode(form.assetEditorContent.val),
            });
            if (opts.onSaved) await opts.onSaved(asset);
            const mountID = form.assetEditorMountID.val;
            if (mountID) {
                form.assetMounts.val = form.assetMounts.val.map(m => m.id === mountID
                    ? {...m, assetId: asset.id, key: asset.key, version: asset.version}
                    : m);
            } else if (!form.assetMounts.val.some(m => m.key === asset.key)) {
                form.assetMounts.val = [...form.assetMounts.val, {id: nextAssetMountID++, assetId: asset.id, key: asset.key, version: asset.version, path: ''}];
            }
            form.assetEditorOpen.val = false;
            form.assetMountsPaneOpen.val = true;
        } catch (e) {
            form.assetEditorError.val = e.message || 'Failed to save asset';
        }
    };
    return div(
        {class: () => form.assetEditorOpen.val
            ? "w-1/2 shrink-0 border-l border-gray-700 flex flex-col"
            : "hidden"},
        div(
            {class: "flex items-center justify-between gap-3 px-4 py-3 border-b border-gray-700"},
            h3({class: "text-sm font-semibold text-gray-200"}, "Create asset"),
            button({
                type: "button",
                class: "text-gray-500 hover:text-gray-200 cursor-pointer",
                title: "Close",
                onclick: () => { form.assetEditorOpen.val = false; },
            }, X({size: 16})),
        ),
        div(
            {class: "flex-1 min-h-0 flex flex-col gap-3 p-4"},
            () => form.assetEditorError.val ? p({class: "text-xs text-red-400"}, form.assetEditorError.val) : '',
            field("Key", input({
                class: textInputClass(true),
                placeholder: "nginx.conf",
                value: form.assetEditorKey,
                oninput: e => { form.assetEditorKey.val = e.target.value; },
            })),
            field("Format", input({
                class: textInputClass(true),
                placeholder: "text",
                value: form.assetEditorFormat,
                oninput: e => { form.assetEditorFormat.val = e.target.value; },
            })),
            textarea({
                class: "flex-1 min-h-0 w-full resize-none rounded-sm bg-gray-800 text-gray-100 border border-gray-700 px-3 py-2 font-mono text-xs leading-relaxed focus:outline-none focus:ring-1 focus:ring-brand",
                spellcheck: "false",
                placeholder: "Paste config file contents here",
                value: form.assetEditorContent,
                oninput: e => { form.assetEditorContent.val = e.target.value; },
            }),
            div({class: "flex justify-end"},
                button({
                    type: "button",
                    class: "btn-primary text-sm py-1.5 px-4 cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed",
                    disabled: () => !form.assetEditorKey.val.trim(),
                    onclick: save,
                }, "Save asset")),
        ),
    );
}

// envVarsPane is the right-hand editor pane: a textarea of KEY=value lines that
// stays in sync with form.envVars. It is always mounted and toggled via a CSS
// class (a binding that returns null would be GC'd by VanJS and never re-open).
export function envVarsPane(form) {
    const text = van.state(envVarsToText(form.envVars.rawVal));
    return div(
        {class: () => form.envPaneOpen.val
            ? "w-1/2 shrink-0 border-l border-gray-700 flex flex-col"
            : "hidden"},
        div(
            {class: "flex items-center justify-between gap-3 px-4 py-3 border-b border-gray-700"},
            h3({class: "text-sm font-semibold text-gray-200"}, "Environment variables"),
            button({
                type: "button",
                class: "text-gray-500 hover:text-gray-200 cursor-pointer",
                title: "Close",
                onclick: () => { form.envPaneOpen.val = false; },
            }, X({size: 16})),
        ),
        div(
            {class: "flex-1 min-h-0 flex flex-col gap-2 p-4"},
            p({class: "text-[11px] text-gray-500"}, "One variable per line, as KEY=value."),
            textarea({
                "data-testid": "deployment-env-vars-textarea",
                class: "flex-1 min-h-0 w-full resize-none rounded-sm bg-gray-800 text-gray-100 border border-gray-700 px-3 py-2 font-mono text-xs leading-relaxed focus:outline-none focus:ring-1 focus:ring-brand",
                spellcheck: "false",
                placeholder: "DATABASE_URL=postgres://...\nLOG_LEVEL=info",
                value: text.rawVal,
                oninput: e => {
                    text.val = e.target.value;
                    form.envVars.val = textToEnvVars(e.target.value);
                },
            }),
        ),
    );
}

function envVarCount(arr) {
    return (arr || []).filter(v => v && v.key && v.key.trim()).length;
}

function formEnvVars(form) {
    return form.envVars.val
        .map(v => ({key: v.key.trim(), value: v.value}))
        .filter(v => v.key);
}

function formAssetMounts(form) {
    return (form.assetMounts.val || [])
        .map(m => ({asset: (m.key || '').trim(), version: m.version || 0, path: (m.path || '').trim()}))
        .filter(m => m.asset && m.path);
}

function formVolumeMounts(form) {
    return (form.volumeMounts.val || [])
        .map(m => ({host: (m.host || '').trim(), container: (m.container || '').trim(), readonly: Boolean(m.readonly)}))
        .filter(m => m.host && m.container);
}

function defaultVolumeFallbackContainerPath(form) {
    const user = form.containerUser.val.trim();
    if (!user || user === 'root' || user === '0') return '/var';
    const name = user.includes(':') ? user.slice(0, user.indexOf(':')) : user;
    return `/home/${name}/var`;
}

function hasInvalidVolumeConfig(form) {
    const path = form.containerDataMountPath.val.trim();
    if (!path) return false;
    return !path.startsWith('/') || path.endsWith('/') || path.includes('/../') || path.includes('/./') || path === '/';
}

function hasInvalidAssetMounts(form) {
    return (form.assetMounts.val || []).some(m => {
        const key = (m.key || '').trim();
        const path = (m.path || '').trim();
        if (!key) return false;
        return !path || !path.startsWith('/') || path.endsWith('/') || path.includes('/../') || path.includes('/./') || path === '/';
    });
}

function closeRuntimePanes(form, keep) {
    if (keep !== 'env') form.envPaneOpen.val = false;
    if (keep !== 'assets') form.assetMountsPaneOpen.val = false;
    if (keep !== 'volumes') form.volumeMountsPaneOpen.val = false;
    if (keep !== 'assetEditor') form.assetEditorOpen.val = false;
}

function envVarsToText(arr) {
    return (arr || [])
        .filter(v => v && (v.key || v.value))
        .map(v => `${v.key || ''}=${v.value || ''}`)
        .join('\n');
}

function textToEnvVars(text) {
    return text.split('\n').reduce((acc, line) => {
        if (!line.trim()) return acc;
        const idx = line.indexOf('=');
        const key = (idx === -1 ? line : line.slice(0, idx)).trim();
        const value = idx === -1 ? '' : line.slice(idx + 1);
        if (key || value) acc.push({key, value});
        return acc;
    }, []);
}

// --- Repository field with on-blur accessibility validation ----------------

function repoField(form, sourceType) {
    const repoState = form.nixRepo;
    return label(
        {class: "flex-1 flex flex-col gap-1 text-xs text-gray-400"},
        div(
            {class: "flex items-center justify-between gap-3"},
            span("Repository"),
            () => {
                const c = activeRepoCheck(form, sourceType, repoState);
                return c.status === 'idle' ? '' : span({class: repoMsgClass(c.status)}, c.message);
            },
        ),
        input({
            type: "text",
            "data-testid": "deployment-repo-input",
            value: repoState.rawVal,
            placeholder: "github.com/org/repo",
            class: () => repoInputClass(activeRepoCheck(form, sourceType, repoState).status),
            oninput: e => { repoState.val = e.target.value; },
            onblur: () => validateRepo(form),
        }),
    );
}

function flakeField(form) {
    return label(
        {class: "flex flex-col gap-1 text-xs text-gray-400"},
        div(
            {class: "flex items-center justify-between gap-3"},
            span("Path to flake.nix"),
            () => {
                const c = activeFlakeCheck(form);
                return c.status === 'idle' ? '' : span({class: repoMsgClass(c.status)}, c.message);
            },
        ),
        input({
            type: "text",
            "data-testid": "deployment-flake-input",
            value: form.nixFlake.rawVal,
            class: () => repoInputClass(activeFlakeCheck(form).status),
            placeholder: "nix/app/flake.nix",
            oninput: e => { form.nixFlake.val = e.target.value; },
            onblur: () => validateRepo(form),
        }),
    );
}

function dockerImageField(form) {
    return label(
        {class: "flex-1 flex flex-col gap-1 text-xs text-gray-400"},
        div(
            {class: "flex items-center justify-between gap-3"},
            span("Image"),
            () => {
                const c = activeImageCheck(form);
                return c.status === 'idle' ? '' : span({class: repoMsgClass(c.status)}, c.message);
            },
        ),
        input({
            type: "text",
            "data-testid": "deployment-container-image-input",
            name: "deployment-container-image",
            autocomplete: "off",
            autocapitalize: "none",
            autocorrect: "off",
            spellcheck: "false",
            value: form.containerImage.rawVal,
            placeholder: "postgres or ghcr.io/org/app",
            class: () => repoInputClass(activeImageCheck(form).status),
            oninput: e => { form.containerImage.val = e.target.value; },
            onblur: () => validateRepo(form),
        }),
        span({class: "text-[11px] text-gray-500"}, "Kubernetes-style image path. Docker Hub shorthand such as postgres is supported."),
    );
}

// activeRepoCheck returns the validation result only if it still matches the
// repo and source currently in the field; otherwise it reads as idle so a stale
// green/red state disappears the moment the user edits or switches source.
function activeRepoCheck(form, sourceType, repoState) {
    const c = form.repoCheck.val;
    const repo = repoState.val.trim();
    if (!repo || c.sourceType !== sourceType || c.repo !== repo || c.sourceKey !== sourceValidationKey(form)) {
        return {status: 'idle', message: ''};
    }
    return c;
}

function activeFlakeCheck(form) {
    const sourceType = form.sourceType.val;
    if (sourceType !== SOURCE_NIX_DOCKER) {
        return {status: 'idle', message: ''};
    }
    const repo = form.nixRepo.val.trim();
    const flakePath = form.nixFlake.val.trim();
    const c = form.repoCheck.val;
    if (!repo || !flakePath || c.sourceKey !== sourceValidationKey(form)) {
        return {status: 'idle', message: ''};
    }
    if (c.status === 'checking') {
        return {status: 'checking', message: 'Checking path…'};
    }
    if (c.nixFlakeFile?.ok) {
        return {status: 'ok', message: c.nixFlakeFile.message || 'Path verified'};
    }
    if (c.nixFlakeFile?.message) {
        return {status: 'error', message: c.nixFlakeFile.message};
    }
    return {status: 'idle', message: ''};
}

function activeImageCheck(form) {
    const c = form.repoCheck.val;
    const image = form.containerImage.val.trim();
    if (!image || form.sourceType.val !== SOURCE_DOCKER_IMAGE || c.sourceKey !== sourceValidationKey(form)) {
        return {status: 'idle', message: ''};
    }
    return c;
}

async function validateRepo(form) {
    const sourceType = form.sourceType.val;
    const repoState = sourceType === SOURCE_DOCKER_IMAGE ? form.containerImage : form.nixRepo;
    const repo = repoState.val.trim();
    if (!repo) {
        form.repoCheck.val = {status: 'idle', message: '', repo: '', sourceType, sourceKey: ''};
        return;
    }
    const sourceKey = sourceValidationKey(form);
    const c = form.repoCheck.val;
    // Don't re-check a repo we already have a verdict for.
    if (c.sourceKey === sourceKey && (c.status === 'ok' || c.status === 'error')) {
        return;
    }
    form.repoCheck.val = {status: 'checking', message: 'Checking repository access…', repo, sourceType, sourceKey};
    try {
        const res = await capi.postV1RepoValidate(buildValidateSourceRequest(form));
        form.repoCheck.val = sourceCheckFromValidation(form, res, repo, sourceType, sourceKey);
    } catch (e) {
        form.repoCheck.val = {status: 'error', message: e.message || 'Validation failed.', repo, sourceType, sourceKey};
    }
}

function repoInputClass(status) {
    let border = 'border-gray-700 focus:ring-brand';
    if (status === 'ok') border = 'border-green-500 focus:ring-green-500';
    else if (status === 'error') border = 'border-red-500 focus:ring-red-500';
    return `w-full px-3 py-2 rounded-sm bg-gray-800 text-gray-100 border ${border} focus:outline-none focus:ring-1`;
}

function repoMsgClass(status) {
    if (status === 'ok') return 'text-xs text-green-400';
    if (status === 'error') return 'text-xs text-red-400';
    return 'text-xs text-gray-500';
}

// selectField renders a label-above dropdown, consistent with the text fields.
// widthClass constrains the select so source/runner type sit left-aligned
// rather than stretching the full row.
function selectField(text, state, options, widthClass = "w-56", onChange) {
    const testID = {
        "Source type": "deployment-source-type-select",
        "Strategy": "deployment-runner-strategy-select",
    }[text];
    return field(text, select({
        "data-testid": testID,
        value: state,
        class: `${widthClass} px-3 py-2 rounded-sm bg-gray-800 text-gray-100 border border-gray-700 focus:outline-none focus:ring-1 focus:ring-brand`,
        onchange: e => {
            state.val = e.target.value;
            if (onChange) onChange(e.target.value);
        },
    },
        ...options.map(opt => option({value: opt.value, selected: state.rawVal === opt.value}, opt.label)),
    ));
}

function textInput(state, placeholder = '', onblur) {
    const testID = placeholder === 'nix/app/flake.nix' ? 'deployment-flake-input' : undefined;
    return input({
        type: "text",
        "data-testid": testID,
        value: state.rawVal,
        class: textInputClass(),
        placeholder,
        oninput: e => { state.val = e.target.value; },
        onblur,
    });
}

function textInputClass(valid = false, disabled = false, success = false) {
    const border = success ? 'border-green-500 focus:ring-green-500' : 'border-gray-700 focus:ring-brand';
    const muted = disabled ? 'opacity-70 cursor-not-allowed pointer-events-none' : '';
    return `w-full px-3 py-2 rounded-sm bg-gray-800 text-gray-100 border ${border} focus:outline-none focus:ring-1 ${muted}`;
}

function selectClass() {
    return "w-full px-3 py-2 rounded-sm bg-gray-800 text-gray-100 border border-gray-700 focus:outline-none focus:ring-1 focus:ring-brand";
}

function machineSelect(form, opts) {
    const current = form.machine.rawVal;
    const extraCurrent = current && !opts.machineOptionValues.includes(current)
        ? [option({value: current, selected: true}, current)]
        : [];
    return select({
        "data-testid": "deployment-machine-select",
        value: form.machine,
        class: `${selectClass()} ${opts.identityLocked ? 'opacity-70 cursor-not-allowed pointer-events-none' : ''}`,
        disabled: opts.identityLocked || !opts.machineOptionsLoaded || opts.machineOptionValues.length === 0,
        onchange: e => { form.machine.val = e.target.value; },
    },
        option({value: '', disabled: true, selected: !current}, machinePlaceholder(opts.machineOptionsLoaded, opts.machineOptionValues)),
        ...opts.machineOptionValues.map(name => option({value: name, selected: name === current}, name)),
        ...extraCurrent,
    );
}

function machinePlaceholder(loaded, options) {
    if (!loaded) return "Loading machines...";
    return options.length === 0 ? "No registered machines" : "Select a machine...";
}

function nameValid(form) {
    return Boolean(form.name.val.trim());
}
