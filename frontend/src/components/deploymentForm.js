import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {xIcon} from "../lib/icons.js";
import {referencePicker} from "./referencePicker.js";
import {secretRefsS, spacesS, userConfigRefsS} from "../state/deployments.js";

const { div, h3, label, input, select, option, button, p, span, textarea, table, thead, tbody, tfoot, tr, th, td } = van.tags;

const SOURCE_NIX_DOCKER = 'nixDockerBuild';
const SOURCE_DOCKER_IMAGE = 'containerImage';
const RUNNER_CONTAINER = 'container';
const NETWORKING_MODE_VIRTUAL = 1;
const NETWORKING_MODE_HOST = 2;
const PORT_FORWARD_PROTOCOL_TCP = 1;
const PORT_FORWARD_PROTOCOL_UDP = 2;
const CONTAINER_UPGRADE_RECREATE = 1;
const CONTAINER_UPGRADE_ROLLOVER = 2;
const DEFAULT_READINESS_TIMEOUT_SECONDS = 600;
const DEPLOYMENT_VOLUME_HOST_RE = /^\/var\/lib\/opendeploy-volumes\/(\d+)\/default$/;
const FILE_DESCRIPTOR_LIMIT_DEFAULT = '2048';
const FILE_DESCRIPTOR_LIMIT_MAX = 2147483647;
const DEV_SHM_UNITS = [
    {value: 'KB', label: 'KB', factorKB: 1},
    {value: 'MB', label: 'MB', factorKB: 1024},
    {value: 'GB', label: 'GB', factorKB: 1024 * 1024},
    {value: 'TB', label: 'TB', factorKB: 1024 * 1024 * 1024},
];
const DEV_SHM_DEFAULT_VALUE = '64';
const DEV_SHM_DEFAULT_UNIT = 'MB';
const DEV_SHM_MAX_KB = 2147483647;
const DEFAULT_SPACE_ID = 1;
const INTERNAL_SPACE_ID = 0;

let nextEnvID = 1;
let nextAssetMountID = 1;
let nextVolumeMountID = 1;
let nextPortForwardID = 1;

export function emptyDeploymentForm() {
    return makeFormState({
        deploymentId: 0,
        name: '',
        spaceId: 1,
        machine: '',
        sourceType: SOURCE_DOCKER_IMAGE,
        nixRepo: '',
        nixFlake: '',
        containerImage: '',
        networkingMode: String(NETWORKING_MODE_VIRTUAL),
        portForwarding: [],
        runnerType: RUNNER_CONTAINER,
        containerUser: '',
        containerCommand: '',
        containerDataMountPath: '',
        containerDisableDataVolume: false,
        containerDevShmOverride: false,
        containerDevShmSizeValue: '',
        containerDevShmSizeUnit: DEV_SHM_DEFAULT_UNIT,
        containerFileDescriptorLimitOverride: false,
        containerFileDescriptorLimit: '',
        containerUpgradeStrategy: String(CONTAINER_UPGRADE_RECREATE),
        containerReadinessTimeoutSeconds: DEFAULT_READINESS_TIMEOUT_SECONDS,
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
    const networking = spec.networking || {};
    const devShm = devShmFormState(container.devShmSizeKb || 0);
    const fileDescriptorLimit = Number(container.fileDescriptorLimit || 0);
    // Reveal a section's additional options up-front when the existing config
    // already sets one of them, so they aren't hidden on edit.
    const showSourceOpts = false;
    const showExecOpts = (container.command || []).length > 0;
    const repoCheck = prepare.containerImage && containerImage.image
        ? knownContainerImageSourceCheck(containerImage.image)
        : (nixDocker.repo && nixDocker.flake ? knownNixSourceCheck(nixDocker.repo, nixDocker.flake) : undefined);

    return makeFormState({
        deploymentId: cfg.id || 0,
        name: cid.name || '',
        spaceId: cid.spaceId ?? DEFAULT_SPACE_ID,
        machine: cid.machine || '',
        sourceType: prepare.containerImage ? SOURCE_DOCKER_IMAGE : SOURCE_NIX_DOCKER,
        nixRepo: nixDocker.repo || '',
        nixFlake: nixDocker.flake || '',
        containerImage: containerImage.image || '',
        networkingMode: String(networking.mode || NETWORKING_MODE_HOST),
        portForwarding: portForwardingToFormRows(networking.portForwarding),
        runnerType: RUNNER_CONTAINER,
        containerUser: container.user || '',
        containerCommand: (container.command || []).join('\n'),
        containerDataMountPath: container.dataMountPath || '',
        containerDisableDataVolume: Boolean(container.disableDataVolume),
        containerDevShmOverride: Number(container.devShmSizeKb || 0) > 0,
        containerDevShmSizeValue: devShm.value,
        containerDevShmSizeUnit: devShm.unit,
        containerFileDescriptorLimitOverride: fileDescriptorLimit > 0,
        containerFileDescriptorLimit: fileDescriptorLimit > 0 ? String(fileDescriptorLimit) : '',
        containerUpgradeStrategy: String(container.upgradeStrategy || CONTAINER_UPGRADE_RECREATE),
        containerReadinessTimeoutSeconds: container.readinessSignal?.timeoutSeconds || DEFAULT_READINESS_TIMEOUT_SECONDS,
        envVars: envVarsToFormRows(container.envVars),
        assetMounts: (container.assetMounts || []).map(m => {
            const row = {id: nextAssetMountID++, assetId: m.assetId || 0, key: m.asset || '', path: m.path || '', version: m.version || 0, executable: Boolean(m.executable)};
            return {...row, originalAssetId: row.assetId, originalKey: row.key, originalPath: row.path, originalVersion: row.version, originalExecutable: row.executable};
        }),
        volumeMounts: (container.mounts || []).map(m => mountToFormRow(m)),
        showSourceOpts,
        showExecOpts,
        repoCheck,
    });
}

export function deploymentForm(form, opts = {}) {
    const identityLocked = Boolean(opts.identityLocked);
    const spaceOptions = publicSpaceOptions(opts.spaceOptions || spacesS.val, form.spaceId.val);
    const machineOptions = opts.machineOptions || [];
    const machineOptionValues = machineOptions.map(m => typeof m === 'string' ? m : m.name).filter(Boolean);
    const machineOptionsLoaded = opts.machineOptionsLoaded !== false;
    const executionTitle = opts.executionTitle || "Runtime";
    const showIdentityLockedNotice = (message) => {
        if (!identityLocked) return;
        form.identityLockNotice.val = message;
        if (form.identityLockNoticeTimer) clearTimeout(form.identityLockNoticeTimer);
        form.identityLockNoticeTimer = setTimeout(() => {
            form.identityLockNotice.val = '';
            form.identityLockNoticeTimer = null;
        }, 5000);
    };

    return div(
        {class: "flex flex-col gap-5"},
        opts.hideIdentity ? '' : sectionDivider("Deployment identity"),
        opts.hideIdentity ? '' : div(
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
                }), identityLocked, () => showIdentityLockedNotice("Name is not currently changeable after creation.")),
                identityField("Space", select({
                    "data-testid": "deployment-space-select",
                    value: String(form.spaceId.val ?? DEFAULT_SPACE_ID),
                    class: textInputClass(false, false),
                    onchange: e => { form.spaceId.val = Number(e.target.value || 0); },
                }, ...spaceOptions.map(space => option({value: String(space.id), selected: Number(space.id) === Number(form.spaceId.val)}, space.name || `space ${space.id}`))), false),
                identityField("Machine", machineSelect(form, {
                    identityLocked,
                    machineOptionsLoaded,
                    machineOptionValues,
                }), identityLocked, () => showIdentityLockedNotice("Machine is not currently changeable after creation.")),
            ),
            () => identityLocked && form.identityLockNotice.val
                ? span({class: "text-xs text-red-400 -mb-2"}, form.identityLockNotice.val)
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
                    commandSummary(form),
                    () => commandOptions(form),
                    volumeMountsSummary(form),
                    assetMountsSection(form, opts),
                    upgradeStrategySummary(form),
                    resourcesSummary(form),
                    networkingSummary(form),
                ),
            ),
        ),
    );
}

export function formToDeploymentIdentifier(form) {
    return {
        name: form.name.val.trim(),
        spaceId: Number(form.spaceId.val || DEFAULT_SPACE_ID),
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
        upgradeStrategy: Number(form.containerUpgradeStrategy.val || CONTAINER_UPGRADE_RECREATE),
    };
    spec.networking = {
        mode: Number(form.networkingMode.val || NETWORKING_MODE_VIRTUAL),
    };
    const portForwarding = formPortForwarding(form);
    if (portForwarding.length) spec.networking.portForwarding = portForwarding;
    if (Number(form.containerUpgradeStrategy.val) === CONTAINER_UPGRADE_ROLLOVER) {
        spec.runner.container.readinessSignal = {timeoutSeconds: Number(form.containerReadinessTimeoutSeconds.val || 0)};
    }
    const user = form.containerUser.val.trim();
    if (user) spec.runner.container.user = user;
    const command = formCommand(form);
    if (command.length) spec.runner.container.command = command;
    const dataMountPath = form.containerDataMountPath.val.trim();
    if (dataMountPath) spec.runner.container.dataMountPath = dataMountPath;
    const devShmSizeKb = devShmSizeKbForForm(form);
    if (devShmSizeKb > 0) spec.runner.container.devShmSizeKb = devShmSizeKb;
    const fileDescriptorLimit = fileDescriptorLimitForForm(form);
    if (fileDescriptorLimit > 0) spec.runner.container.fileDescriptorLimit = fileDescriptorLimit;
    const env = formEnvVars(form);
    if (Object.keys(env).length) spec.runner.container.envVars = env;
    const mounts = formVolumeMounts(form);
    if (mounts.length) spec.runner.container.mounts = mounts;
    const assetMounts = formAssetMounts(form);
    if (assetMounts.length) spec.runner.container.assetMounts = assetMounts;

    return spec;
}

export function isFormValid(form, opts = {}) {
    return !formInvalidReason(form, opts);
}

export function formInvalidReason(form, opts = {}) {
    if (!nameValid(form)) return 'Deployment name is required.';
    if (!form.machine.val.trim()) return 'Machine is required.';
    const machineOptions = opts.machineOptions || [];
    const machineOptionValues = machineOptions.map(m => typeof m === 'string' ? m : m.name).filter(Boolean);
    if (machineOptionValues.length > 0 && !machineOptionValues.includes(form.machine.val.trim())) return 'Select a registered machine.';
    if (form.sourceType.val === SOURCE_DOCKER_IMAGE) {
        if (!form.containerImage.val.trim()) return 'Container image is required.';
    } else if (!form.nixRepo.val.trim() || !form.nixFlake.val.trim()) {
        return 'Repository and flake path are required.';
    }
    return invalidEnvVarsReason(form)
        || invalidCommandReason(form)
        || invalidVolumeConfigReason(form, opts)
        || invalidAssetMountsReason(form)
        || invalidUpgradeStrategyReason(form)
        || invalidDevShmReason(form)
        || invalidFileDescriptorLimitReason(form)
        || '';
}

export function buildValidateSourceRequest(form, opts = {}) {
    const sourceType = form.sourceType.val;
    const branch = (opts.branch || '').trim();
    const commit = (opts.commit || '').trim();
    const hasExplicitFlags = [
        'refreshAvailableBranches',
        'refreshAvailableCommits',
        'refreshVersions',
        'checkRepo',
        'checkBranch',
        'checkCommit',
        'checkFlakePath',
    ].some(k => k in opts);
    if (sourceType === SOURCE_NIX_DOCKER) {
        const refreshAvailableBranches = hasExplicitFlags
            ? Boolean(opts.refreshAvailableBranches)
            : true;
        const refreshAvailableCommits = hasExplicitFlags
            ? Boolean(opts.refreshAvailableCommits ?? opts.refreshVersions === true)
            : Boolean(branch);
        return {nixDockerBuild: {
            repoUrl: form.nixRepo.val.trim(),
            selectedBranch: branch,
            selectedCommit: commit ? {id: commit} : undefined,
            selectedFlakePath: form.nixFlake.val.trim(),
            refreshAvailableBranches,
            refreshAvailableCommits,
            checkRepo: hasExplicitFlags ? Boolean(opts.checkRepo ?? (refreshAvailableBranches || opts.refreshVersions === true)) : true,
            checkBranch: hasExplicitFlags ? Boolean(opts.checkBranch) : Boolean(branch),
            checkCommit: hasExplicitFlags ? Boolean(opts.checkCommit) : Boolean(commit),
            checkFlakePath: hasExplicitFlags ? Boolean(opts.checkFlakePath) : Boolean(form.nixFlake.val.trim()),
        }};
    }
    if (sourceType === SOURCE_DOCKER_IMAGE) {
        return {containerImage: {image: form.containerImage.val.trim(), refreshVersions: opts.refreshVersions !== false}};
    }
    return {containerImage: {image: form.containerImage.val.trim(), refreshVersions: opts.refreshVersions !== false}};
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

export function sourceCheckFromValidation(form, res, repo, sourceType, sourceKey) {
    const sourceResult = validationSourceResult(form, res);
    const previous = form.repoCheck.val || {};
    const canReusePrevious = previous.sourceKey === sourceKey && previous.sourceType === sourceType && previous.repo === repo;

    if (sourceType === SOURCE_DOCKER_IMAGE) {
        const image = validationResultOrPrevious(
            sourceResult.image,
            canReusePrevious ? previous.image : undefined,
            {ok: false, message: 'Image not accessible.'},
        );
        return {
            status: image.ok ? 'ok' : 'error',
            message: image.message || (image.ok ? 'Image accessible.' : 'Image not accessible.'),
            repo,
            sourceType,
            sourceKey,
            image,
            tags: (sourceResult.tags || []).length > 0 ? sourceResult.tags : (canReusePrevious ? previous.tags || [] : []),
        };
    }

    const gitRepository = validationResultOrPrevious(
        sourceResult.gitRepository,
        canReusePrevious ? previous.gitRepository : undefined,
        {ok: false, message: 'Git repository not accessible.'},
    );
    const nixFlakeFile = validationResultOrPrevious(
        sourceResult.nixFlakeFile,
        canReusePrevious ? previous.nixFlakeFile : undefined,
        {ok: false, message: ''},
    );
    const ok = Boolean(gitRepository.ok && (!form.nixFlake.val.trim() || nixFlakeFile.ok));
    const branches = sourceResult.availableBranches?.loaded
        ? (sourceResult.availableBranches.branches || [])
        : (canReusePrevious ? previous.branches || [] : []);
    const commitsByBranch = canReusePrevious ? {...(previous.commitsByBranch || {})} : {};
    const activeBranch = sourceResult.availableCommits?.branch || sourceResult.checkedBranch || (canReusePrevious ? previous.branch || '' : '');
    if (sourceResult.availableCommits?.loaded) {
        commitsByBranch[activeBranch] = sourceResult.availableCommits?.commits || [];
    }
    const activeCommits = commitsByBranch[activeBranch] || [];
    return {
        status: ok ? 'ok' : 'error',
        message: gitRepository.message || (gitRepository.ok ? 'Repo accessible.' : 'Source not accessible.'),
        repo,
        sourceType,
        sourceKey,
        gitRepository,
        nixFlakeFile,
        branches,
        branch: activeBranch,
        commits: activeCommits,
        commitsByBranch,
    };
}

function hasValidationResult(result) {
    return Boolean(result && (result.checked || result.ok || result.message));
}

function validationResultOrPrevious(result, previous, fallback) {
    if (hasValidationResult(result)) return result;
    if (hasValidationResult(previous)) return previous;
    return fallback;
}

function knownNixSourceCheck(repo, flake) {
    const trimmedRepo = (repo || '').trim();
    const trimmedFlake = (flake || '').trim();
    const sourceKey = `${SOURCE_NIX_DOCKER}:${trimmedRepo}:${trimmedFlake}`;
    return {
        status: 'ok',
        message: 'Repo accessible.',
        repo: trimmedRepo,
        sourceType: SOURCE_NIX_DOCKER,
        sourceKey,
        gitRepository: {checked: true, ok: true, message: 'Repo accessible.'},
        nixFlakeFile: {checked: true, ok: true, message: 'Path verified'},
        branches: [],
        branch: '',
        commits: [],
        commitsByBranch: {},
    };
}

function knownContainerImageSourceCheck(image) {
    const trimmedImage = (image || '').trim();
    const sourceKey = `${SOURCE_DOCKER_IMAGE}:${trimmedImage}`;
    return {
        status: 'ok',
        message: 'Image accessible.',
        repo: trimmedImage,
        sourceType: SOURCE_DOCKER_IMAGE,
        sourceKey,
        image: {checked: true, ok: true, message: 'Image accessible.'},
        tags: [],
    };
}

export async function validateSelectedCommit(form, branch, commit) {
    if (form.deploymentCreationUpdate) {
        return form.deploymentCreationUpdate.validateSelectedCommit(branch, commit);
    }
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
        const req = buildValidateSourceRequest(form, {
            branch,
            commit: selectedCommit,
            checkCommit: true,
            checkFlakePath: Boolean(form.nixFlake.val.trim()),
        });
        const res = await capi.postV1RepoValidate(req);
        console.log('[opendeploy] repo validate selected commit response', {request: req, response: res});
        form.repoCheck.val = sourceCheckFromValidation(form, res, repo, sourceType, sourceKey);
    } catch (e) {
        console.error('[opendeploy] repo validate selected commit failed', {error: e, stack: e?.stack});
        form.repoCheck.val = {status: 'error', message: e.message || 'Validation failed.', repo, sourceType, sourceKey};
    }
}

export function hasTrustedSourceValidation(form) {
    const sourceType = form.sourceType.val;
    const repo = sourceType === SOURCE_DOCKER_IMAGE ? form.containerImage.val.trim() : form.nixRepo.val.trim();
    const c = form.repoCheck.val;
    return Boolean(repo && c.status === 'ok' && c.sourceType === sourceType && c.repo === repo && c.sourceKey === sourceValidationKey(form));
}

function makeFormState(values) {
    return {
        name: van.state(values.name),
        deploymentId: van.state(values.deploymentId || 0),
        spaceId: van.state(values.spaceId ?? DEFAULT_SPACE_ID),
        machine: van.state(values.machine),
        sourceType: van.state(values.sourceType),
        nixRepo: van.state(values.nixRepo),
        nixFlake: van.state(values.nixFlake),
        containerImage: van.state(values.containerImage || ''),
        networkingMode: van.state(String(values.networkingMode || NETWORKING_MODE_VIRTUAL)),
        portForwarding: van.state(values.portForwarding || []),
        runnerType: van.state(values.runnerType),
        containerUser: van.state(values.containerUser || ''),
        containerCommand: van.state(values.containerCommand || ''),
        containerDataMountPath: van.state(values.containerDataMountPath || ''),
        containerDisableDataVolume: van.state(Boolean(values.containerDisableDataVolume)),
        containerDevShmOverride: van.state(Boolean(values.containerDevShmOverride)),
        containerDevShmSizeValue: van.state(values.containerDevShmSizeValue || ''),
        containerDevShmSizeUnit: van.state(values.containerDevShmSizeUnit || DEV_SHM_DEFAULT_UNIT),
        containerFileDescriptorLimitOverride: van.state(Boolean(values.containerFileDescriptorLimitOverride)),
        containerFileDescriptorLimit: van.state(values.containerFileDescriptorLimit || ''),
        containerUpgradeStrategy: van.state(String(values.containerUpgradeStrategy || CONTAINER_UPGRADE_RECREATE)),
        containerReadinessTimeoutSeconds: van.state(values.containerReadinessTimeoutSeconds ?? DEFAULT_READINESS_TIMEOUT_SECONDS),
        envVars: van.state(values.envVars || []),
        assetMounts: van.state(values.assetMounts || []),
        volumeMounts: van.state(values.volumeMounts || []),
        showSourceOpts: van.state(Boolean(values.showSourceOpts)),
        showExecOpts: van.state(Boolean(values.showExecOpts)),
        identityLockNotice: van.state(''),
        identityLockNoticeTimer: null,
        // Whether the environment-variables editor pane is open in the overlay.
        envPaneOpen: van.state(false),
        assetMountsPaneOpen: van.state(false),
        volumeMountsPaneOpen: van.state(false),
        upgradeStrategyPaneOpen: van.state(false),
        resourcesPaneOpen: van.state(false),
        networkingPaneOpen: van.state(false),
        assetEditorOpen: van.state(false),
        assetEditorMountID: van.state(0),
        assetEditorError: van.state(''),
        assetEditorKey: van.state(''),
        assetEditorFormat: van.state('text'),
        assetEditorContent: van.state(''),
        // Transient repo-accessibility check; tracks the repo/source it applies
        // to so a stale result is hidden once the inputs change.
        repoCheck: van.state(values.repoCheck || {status: 'idle', message: '', repo: '', sourceType: '', sourceKey: ''}),
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

function commandSummary(form) {
    return div(
        {class: "flex items-center justify-between gap-3"},
        span({class: "text-xs text-gray-400"}, () => {
            const command = formCommand(form);
            return command.length === 0 ? "Image default command" : `${command.length} command argument${command.length === 1 ? '' : 's'}`;
        }),
        button({
            type: "button",
            class: "text-xs text-blue-400 hover:text-blue-300 cursor-pointer",
            onclick: () => { form.showExecOpts.val = !form.showExecOpts.val; },
        }, () => form.showExecOpts.val ? "Close" : "Override"),
    );
}

function commandOptions(form) {
    return div(
        {class: () => form.showExecOpts.val ? "flex flex-col gap-2 rounded-lg border border-gray-700 bg-gray-900/60 p-3" : "hidden"},
        field("Command argv", textarea({
            "data-testid": "deployment-container-command-textarea",
            rows: 4,
            class: `${textInputClass()} font-mono text-xs`,
            placeholder: "/app/server\n--listen\n:8080",
            value: form.containerCommand.rawVal,
            oninput: e => { form.containerCommand.val = e.target.value; },
        }), "One argument per line. Leave blank to use the image default entrypoint/cmd."),
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
        const extra = formVolumeMounts(form).length;
        const n = (form.containerDisableDataVolume.val ? 0 : 1) + extra;
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

function upgradeStrategySummary(form) {
    return div(
        {class: "flex items-center justify-between gap-3"},
        span({class: "text-xs text-gray-400"}, () => `Upgrade strategy: ${upgradeStrategyLabel(form)}`),
        button({
            type: "button",
            class: "text-xs text-blue-400 hover:text-blue-300 cursor-pointer",
            onclick: () => {
                form.upgradeStrategyPaneOpen.val = !form.upgradeStrategyPaneOpen.val;
                if (form.upgradeStrategyPaneOpen.val) closeRuntimePanes(form, 'strategy');
            },
        }, () => form.upgradeStrategyPaneOpen.val ? "Close" : "Configure"),
    );
}

function networkingSummary(form) {
    return div(
        {class: "flex items-center justify-between gap-3"},
        span({class: "text-xs text-gray-400"}, () => `Networking: ${networkingSummaryText(form)}`),
        button({
            type: "button",
            class: "text-xs text-blue-400 hover:text-blue-300 cursor-pointer",
            onclick: () => {
                const opening = !form.networkingPaneOpen.val;
                if (opening) closeRuntimePanes(form, 'networking');
                form.networkingPaneOpen.val = opening;
            },
        }, () => form.networkingPaneOpen.val ? "Close" : "Configure"),
    );
}

function networkingSummaryText(form) {
    if (Number(form.networkingMode.val) === NETWORKING_MODE_HOST) return "Host";
    const count = formPortForwarding(form).length;
    return count === 0 ? "Virtual" : `Virtual, ${count} forwarded port${count === 1 ? '' : 's'}`;
}

export function networkingPane(form) {
    return div(
        {class: () => form.networkingPaneOpen.val
            ? "w-1/2 shrink-0 border-l border-gray-700 flex flex-col"
            : "hidden"},
        div(
            {class: "flex items-center justify-between gap-3 px-4 py-3 border-b border-gray-700"},
            h3({class: "text-sm font-semibold text-gray-200"}, "Networking"),
            button({
                type: "button",
                class: "text-gray-500 hover:text-gray-200 cursor-pointer",
                title: "Close",
                onclick: () => { form.networkingPaneOpen.val = false; },
            }, xIcon({size: 16})),
        ),
        div(
            {class: "flex-1 min-h-0 overflow-auto flex flex-col gap-4 p-4"},
            div(
                {class: "flex flex-col gap-3 rounded-sm border border-gray-700 bg-gray-900/40 p-4"},
                selectField("Mode", form.networkingMode, [
                    {value: String(NETWORKING_MODE_VIRTUAL), label: "Virtual"},
                    {value: String(NETWORKING_MODE_HOST), label: "Host"},
                ], "w-full", value => {
                    form.networkingMode.val = value;
                }),
                p({class: "text-xs leading-relaxed text-gray-500"}, () => Number(form.networkingMode.val) === NETWORKING_MODE_HOST
                    ? "Host mode keeps the container in the machine network namespace. Port forwarding is unavailable because the process binds host ports directly."
                    : "Virtual mode gives the container an isolated network namespace on the OpenDeploy virtual network. Add port forwarding when the workload must be reachable from the machine's host interfaces."),
            ),
            () => Number(form.networkingMode.val) === NETWORKING_MODE_VIRTUAL ? portForwardingSection(form) : '',
        ),
    );
}

function portForwardingSection(form) {
    const rows = () => form.portForwarding.val || [];
    const update = (row, patch) => {
        form.portForwarding.val = rows().map(port => port.id === row.id ? {...port, ...patch} : port);
    };
    const remove = (row) => {
        form.portForwarding.val = rows().filter(port => port.id !== row.id);
    };
    return div(
        {class: "flex flex-col gap-2 rounded-sm border border-gray-800 bg-gray-900/40 p-3"},
        div(
            {class: "flex items-center justify-between gap-3"},
            div(
                span({class: "text-xs text-gray-300"}, "Port forwarding"),
                p({class: "text-[11px] text-gray-500 mt-1"}, "Publish a host TCP or UDP port to a port inside this virtual container."),
            ),
            button({
                type: "button",
                class: "px-2 py-1 rounded-sm bg-gray-800 text-gray-200 text-xs hover:bg-gray-700 cursor-pointer",
                onclick: () => { form.portForwarding.val = [...rows(), newPortForwardingRow()]; },
            }, "Add port"),
        ),
        () => rows().length === 0
            ? p({class: "text-xs text-gray-500"}, "No host ports published.")
            : table(
                {class: "w-full text-xs"},
                thead(tr(
                    th({class: "text-left font-normal text-gray-500 pb-1"}, "Protocol"),
                    th({class: "text-left font-normal text-gray-500 pb-1"}, "Host port"),
                    th({class: "text-left font-normal text-gray-500 pb-1"}, "Container port"),
                    th({class: "w-8"}),
                )),
                tbody(...rows().map(row => tr(
                    td({class: "pr-2 py-1"}, select({
                        value: String(row.protocol || PORT_FORWARD_PROTOCOL_TCP),
                        class: "w-full px-2 py-1 rounded-sm bg-gray-800 text-gray-100 border border-gray-700 focus:outline-none focus:ring-1 focus:ring-brand",
                        onchange: e => update(row, {protocol: Number(e.target.value || PORT_FORWARD_PROTOCOL_TCP)}),
                    },
                        option({value: String(PORT_FORWARD_PROTOCOL_TCP), selected: Number(row.protocol) === PORT_FORWARD_PROTOCOL_TCP}, "TCP"),
                        option({value: String(PORT_FORWARD_PROTOCOL_UDP), selected: Number(row.protocol) === PORT_FORWARD_PROTOCOL_UDP}, "UDP"),
                    )),
                    td({class: "pr-2 py-1"}, input({
                        type: "number",
                        min: "1",
                        max: "65535",
                        value: row.hostPort || '',
                        class: textInputClass(false, false),
                        placeholder: "443",
                        oninput: e => update(row, {hostPort: e.target.value}),
                    })),
                    td({class: "pr-2 py-1"}, input({
                        type: "number",
                        min: "1",
                        max: "65535",
                        value: row.containerPort || '',
                        class: textInputClass(false, false),
                        placeholder: "443",
                        oninput: e => update(row, {containerPort: e.target.value}),
                    })),
                    td({class: "py-1 text-right"}, button({
                        type: "button",
                        class: "text-gray-500 hover:text-red-300 cursor-pointer",
                        title: "Remove port forwarding",
                        onclick: () => remove(row),
                    }, xIcon({class: "w-4 h-4"}))),
                ))),
            ),
    );
}

function newPortForwardingRow(values = {}) {
    return {
        id: nextPortForwardID++,
        protocol: values.protocol || PORT_FORWARD_PROTOCOL_TCP,
        hostPort: values.hostPort ? String(values.hostPort) : '',
        containerPort: values.containerPort ? String(values.containerPort) : '',
    };
}

function portForwardingToFormRows(portForwarding) {
    return (portForwarding || []).map(port => newPortForwardingRow({
        protocol: port.protocol || PORT_FORWARD_PROTOCOL_TCP,
        hostPort: port.hostPort || '',
        containerPort: port.containerPort || '',
    }));
}

function resourcesSummary(form) {
    return div(
        {class: "flex items-center justify-between gap-3"},
        span({class: "text-xs text-gray-400"}, () => `Resources: /dev/shm ${devShmSummaryText(form)}, file descriptors ${fileDescriptorLimitSummaryText(form)}`),
        button({
            type: "button",
            class: "text-xs text-blue-400 hover:text-blue-300 cursor-pointer",
            onclick: () => {
                const opening = !form.resourcesPaneOpen.val;
                if (opening) closeRuntimePanes(form, 'resources');
                form.resourcesPaneOpen.val = opening;
            },
        }, () => form.resourcesPaneOpen.val ? "Close" : "Configure"),
    );
}

function upgradeStrategyLabel(form) {
    const strategy = Number(form.containerUpgradeStrategy.val) === CONTAINER_UPGRADE_ROLLOVER ? "Rollover" : "Re-create";
    return strategy;
}

export function upgradeStrategyPane(form) {
    return div(
        {class: () => form.upgradeStrategyPaneOpen.val
            ? "w-1/2 shrink-0 border-l border-gray-700 flex flex-col"
            : "hidden"},
        div(
            {class: "flex items-center justify-between gap-3 px-4 py-3 border-b border-gray-700"},
            h3({class: "text-sm font-semibold text-gray-200"}, "Upgrade strategy"),
            button({
                type: "button",
                class: "text-gray-500 hover:text-gray-200 cursor-pointer",
                title: "Close",
                onclick: () => { form.upgradeStrategyPaneOpen.val = false; },
            }, xIcon({size: 16})),
        ),
        div(
            {class: "flex-1 min-h-0 overflow-auto flex flex-col gap-4 p-4"},
            selectField("Strategy", form.containerUpgradeStrategy, [
                {value: String(CONTAINER_UPGRADE_RECREATE), label: "Re-create"},
                {value: String(CONTAINER_UPGRADE_ROLLOVER), label: "Rollover"},
            ], "w-full"),
            () => Number(form.containerUpgradeStrategy.val) === CONTAINER_UPGRADE_ROLLOVER
                ? field("Readiness timeout", input({
                    type: "number",
                    min: "0",
                    step: "1",
                    value: form.containerReadinessTimeoutSeconds.rawVal,
                    class: textInputClass(false, false, !hasInvalidUpgradeStrategy(form)),
                    oninput: e => { form.containerReadinessTimeoutSeconds.val = e.target.value; },
                }), "seconds; 0 uses server default")
                : '',
            () => Number(form.containerUpgradeStrategy.val) === CONTAINER_UPGRADE_ROLLOVER
                ? p({class: "text-xs leading-relaxed text-gray-500"}, "OpenDeploy starts the new container beside the old one and waits for it to write ready to OPENDEPLOY_READINESS_SOCK_PATH. After the signal, OpenDeploy stops the old container; the app should then wait for its port to be free before serving.")
                : p({class: "text-xs leading-relaxed text-gray-500"}, "OpenDeploy stops the current container before starting the new version."),
        ),
    );
}

export function resourcesPane(form) {
    return div(
        {class: () => form.resourcesPaneOpen.val
            ? "w-1/2 shrink-0 border-l border-gray-700 flex flex-col"
            : "hidden"},
        div(
            {class: "flex items-center justify-between gap-3 px-4 py-3 border-b border-gray-700"},
            h3({class: "text-sm font-semibold text-gray-200"}, "Resources"),
            button({
                type: "button",
                class: "text-gray-500 hover:text-gray-200 cursor-pointer",
                title: "Close",
                onclick: () => { form.resourcesPaneOpen.val = false; },
            }, xIcon({size: 16})),
        ),
        div(
            {class: "flex-1 min-h-0 overflow-auto flex flex-col gap-4 p-4"},
            div(
                {class: "flex flex-col gap-3 rounded-sm border border-gray-700 bg-gray-900/40 p-4"},
                label({class: "flex items-center gap-2 text-sm text-gray-200"},
                    input({
                        type: "checkbox",
                        checked: () => form.containerDevShmOverride.val,
                        onchange: e => {
                            form.containerDevShmOverride.val = e.target.checked;
                            if (e.target.checked) {
                                ensureDevShmOverrideValue(form);
                            } else {
                                resetDevShmOverrideValue(form);
                            }
                        },
                    }),
                    span("Override shared memory size"),
                ),
                div(
                    {class: "grid grid-cols-[minmax(0,1fr)_8rem] gap-3"},
                    input({
                        type: "number",
                        min: "1",
                        step: "1",
                        disabled: () => !form.containerDevShmOverride.val,
                        class: () => `${textInputClass(false, !form.containerDevShmOverride.val)} text-sm`,
                        value: () => form.containerDevShmOverride.val ? form.containerDevShmSizeValue.rawVal : DEV_SHM_DEFAULT_VALUE,
                        oninput: e => { form.containerDevShmSizeValue.val = e.target.value; },
                    }),
                    select({
                        disabled: () => !form.containerDevShmOverride.val,
                        class: () => `${selectClass()} text-sm ${!form.containerDevShmOverride.val ? 'opacity-70 cursor-not-allowed pointer-events-none' : ''}`,
                        value: () => selectedDevShmUnit(form),
                        onchange: e => { form.containerDevShmSizeUnit.val = e.target.value; },
                    }, ...DEV_SHM_UNITS.map(unit => option({value: unit.value, selected: () => unit.value === selectedDevShmUnit(form)}, unit.label))),
                ),
                () => invalidDevShmReason(form)
                    ? p({class: "text-xs text-amber-400"}, invalidDevShmReason(form))
                    : p({class: "text-xs leading-relaxed text-gray-500"}, form.containerDevShmOverride.val
                        ? `Computed API value: ${devShmSizeKbForForm(form)} KiB. Useful for PostgreSQL, browser automation, and other shared-memory-heavy workloads.`
                        : "Unchecked leaves this field out of the deployment config and uses the OpenDeploy default of 64 MiB."),
            ),
            div(
                {class: "flex flex-col gap-3 rounded-sm border border-gray-700 bg-gray-900/40 p-4"},
                label({class: "flex items-center gap-2 text-sm text-gray-200"},
                    input({
                        type: "checkbox",
                        checked: () => form.containerFileDescriptorLimitOverride.val,
                        onchange: e => {
                            form.containerFileDescriptorLimitOverride.val = e.target.checked;
                            if (e.target.checked) {
                                ensureFileDescriptorLimitOverrideValue(form);
                            } else {
                                resetFileDescriptorLimitOverrideValue(form);
                            }
                        },
                    }),
                    span("Override file descriptor limit"),
                ),
                input({
                    type: "number",
                    min: "1",
                    step: "1",
                    disabled: () => !form.containerFileDescriptorLimitOverride.val,
                    class: () => `${textInputClass(false, !form.containerFileDescriptorLimitOverride.val)} text-sm`,
                    value: () => form.containerFileDescriptorLimitOverride.val ? form.containerFileDescriptorLimit.rawVal : FILE_DESCRIPTOR_LIMIT_DEFAULT,
                    oninput: e => { form.containerFileDescriptorLimit.val = e.target.value; },
                }),
                () => invalidFileDescriptorLimitReason(form)
                    ? p({class: "text-xs text-amber-400"}, invalidFileDescriptorLimitReason(form))
                    : p({class: "text-xs leading-relaxed text-gray-500"}, form.containerFileDescriptorLimitOverride.val
                        ? "OpenDeploy sets both the soft and hard RLIMIT_NOFILE values to this override."
                        : `Unchecked leaves this field out of the deployment config and uses the OpenDeploy default of ${FILE_DESCRIPTOR_LIMIT_DEFAULT}.`),
            ),
        ),
    );
}

function defaultVolumeCard(form) {
    return div(
        {class: "flex flex-col gap-2"},
        div({class: "grid grid-cols-[auto_minmax(0,1fr)] items-end gap-3"},
            label({class: "flex items-center gap-2 pb-2 text-xs text-gray-300"},
                input({
                    type: "checkbox",
                    checked: () => !form.containerDisableDataVolume.val,
                    onchange: e => { form.containerDisableDataVolume.val = !e.target.checked; },
                }),
                span("Enable default volume"),
            ),
            field("Container mount path", input({
                class: textInputClass(),
                placeholder: defaultVolumeFallbackContainerPath(form),
                value: form.containerDataMountPath.rawVal,
                disabled: () => form.containerDisableDataVolume.val,
                oninput: e => { form.containerDataMountPath.val = e.target.value; },
            })),
        ),
    );
}

function assetOptionValue(asset) {
    const id = Number(asset?.id || 0);
    return id ? String(id) : '';
}

function rowAssetOptionValue(row) {
    const id = Number(row?.assetId || 0);
    return id ? String(id) : '';
}

function assetOptionLabel(asset) {
    const suffix = asset.selectedOnly ? ' (selected)' : '';
    return `${asset.key} v${asset.version || '?'}${suffix}`;
}

function assetOptionsForRow(assets, row) {
    const options = versionedAssetOptions(assets, row?.assetId);
    const selected = {
        id: Number(row?.assetId || 0),
        key: row?.key || row?.asset || '',
        version: Number(row?.version || 0),
        selectedOnly: true,
    };
    if (selected.id && selected.key && !options.some(assetOption => assetOptionValue(assetOption) === assetOptionValue(selected))) {
        options.unshift(selected);
    }
    return options;
}

function versionedAssetOptions(assets, selectedID) {
    const latestByKey = new Map();
    const byID = new Map();
    for (const asset of assets || []) {
        if (!asset || !asset.id) continue;
        byID.set(Number(asset.id), asset);
        const key = asset.key || '';
        const current = latestByKey.get(key);
        if (!current || Number(asset.version || 0) > Number(current.version || 0)) {
            latestByKey.set(key, asset);
        }
    }
    const options = Array.from(latestByKey.values());
    const selected = byID.get(Number(selectedID || 0));
    if (selected && !options.some(asset => Number(asset.id) === Number(selected.id))) {
        options.push({...selected, selectedOnly: true});
    }
    return options.sort((a, b) => (a.key || '').localeCompare(b.key || '') || Number(a.version || 0) - Number(b.version || 0));
}

export function assetMountsPane(form, opts = {}) {
    const assets = opts.assets || [];
    const enableAssetEditor = Boolean(opts.enableAssetEditor);
    const addMount = () => {
        const row = {id: nextAssetMountID++, assetId: 0, key: '', path: '', version: 0, executable: false};
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
            executable: Boolean(row.originalExecutable),
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
        const match = assetOptionsForRow(assets, row).find(a => assetOptionValue(a) === value);
        updateMount(row, {key: match?.key || '', assetId: match?.id || 0, version: match?.version || 0});
    };

    const rows = () => form.assetMounts.val || [];
    const rowEl = (row) => {
        const assetOptions = assetOptionsForRow(assets, row);
        const selectedValue = rowAssetOptionValue(row);
        return div(
            {class: "rounded-lg border border-gray-700 bg-gray-900/60 p-3 flex flex-col gap-2"},
            div(
                {class: "grid grid-cols-1 md:grid-cols-3 gap-3"},
                field("Asset", select({
                    class: `${selectClass()} ${assetOptions.length === 0 ? 'opacity-70 cursor-not-allowed' : ''}`,
                    disabled: assetOptions.length === 0,
                    value: selectedValue,
                    onchange: e => onAssetSelect(row, e.target.value),
                },
                    option({value: '', disabled: true, selected: !selectedValue || assetOptions.length === 0}, assetOptions.length ? "Select an asset..." : "No assets defined"),
                    ...assetOptions.map(a => option({value: assetOptionValue(a), selected: assetOptionValue(a) === selectedValue}, assetOptionLabel(a))),
                )),
            field("Container path", input({
                class: textInputClass(true),
                placeholder: "/etc/nginx/nginx.conf",
                value: row.path,
                oninput: e => updateMount(row, {path: e.target.value}),
            })),
            field("Mode", select({
                class: selectClass(),
                value: row.executable ? 'executable' : 'readonly',
                onchange: e => updateMount(row, {executable: e.target.value === 'executable'}),
            },
                option({value: 'readonly', selected: !row.executable}, "Read-only"),
                option({value: 'executable', selected: Boolean(row.executable)}, "Read + execute"),
            )),
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
    };
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
            }, xIcon({size: 16})),
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
        || (row.version || 0) !== (row.originalVersion || 0)
        || Boolean(row.executable) !== Boolean(row.originalExecutable);
}

function newInvalidAssetMount(row, assets) {
    if (row.originalKey !== undefined) return false;
    const selectedValue = rowAssetOptionValue(row);
    return !assetOptionsForRow(assets, row).some(asset => assetOptionValue(asset) === selectedValue);
}

export function volumeMountsPane(form, opts = {}) {
    const rows = () => form.volumeMounts.val || [];
    const deploymentRows = () => rows().filter(r => (r.kind || 'host') === 'deployment');
    const hostRows = () => rows().filter(r => (r.kind || 'host') === 'host');
    const addDeploymentMount = () => {
        form.volumeMounts.val = [...rows(), {id: nextVolumeMountID++, kind: 'deployment', deploymentId: 0, host: '', container: '', readonly: false}];
    };
    const addHostMount = () => {
        form.volumeMounts.val = [...rows(), {id: nextVolumeMountID++, kind: 'host', host: '', container: '', readonly: false}];
    };
    const updateMount = (row, patch) => {
        form.volumeMounts.val = rows().map(m => m.id === row.id ? {...m, ...patch} : m);
    };
    const mutateMount = (row, patch) => {
        Object.assign(row, patch);
    };
    const removeMount = (row) => {
        form.volumeMounts.val = rows().filter(m => m.id !== row.id);
    };
    const deploymentOptions = () => deploymentVolumeOptions(optionDeployments(opts), form);
    const deploymentRowEl = (row) => div(
        {class: "rounded-lg border border-gray-700 bg-gray-900/60 p-3 flex flex-col gap-2"},
        div(
            {class: "grid grid-cols-1 md:grid-cols-2 gap-3"},
            field("Deployment", select({
                class: selectClass(),
                value: String(row.deploymentId || ''),
                onchange: e => {
                    const deploymentId = Number(e.target.value || 0);
                    updateMount(row, {deploymentId, host: deploymentId ? defaultVolumeHostPath(deploymentId) : ''});
                },
            },
                option({value: '', disabled: true, selected: !row.deploymentId}, deploymentOptions().length ? "Select deployment..." : "No deployments on this machine"),
                ...deploymentOptions().map(d => option({value: String(d.config.id), selected: d.config.id === row.deploymentId}, deploymentVolumeLabel(d))),
            )),
            field("Container mount path", input({
                class: textInputClass(true),
                placeholder: "/mnt/other-data",
                value: row.container || '',
                oninput: e => mutateMount(row, {container: e.target.value}),
            })),
        ),
        div({class: "flex justify-end"},
            button({
                type: "button",
                class: "text-xs text-gray-500 hover:text-red-400 cursor-pointer",
                onclick: () => removeMount(row),
            }, "Remove"),
        ),
    );
    const hostRowEl = (row) => div(
        {class: "rounded-lg border border-gray-700 bg-gray-900/60 p-3 flex flex-col gap-2"},
        div(
            {class: "grid grid-cols-1 md:grid-cols-2 gap-3"},
            field("Host path", input({
                class: textInputClass(true),
                placeholder: "/home/ubuntu/coflip-server/data",
                value: row.host || '',
                oninput: e => mutateMount(row, {host: e.target.value}),
            }), "Must already exist on the target machine."),
            field("Container mount path", input({
                class: textInputClass(true),
                placeholder: "/data",
                value: row.container || '',
                oninput: e => mutateMount(row, {container: e.target.value}),
            })),
        ),
        div({class: "flex items-center justify-between gap-2"},
            label({class: "flex items-center gap-2 text-xs text-gray-400"},
                input({
                    type: "checkbox",
                    checked: Boolean(row.readonly),
                    onchange: e => updateMount(row, {readonly: e.target.checked}),
                }),
                span("Read-only"),
            ),
            button({
                type: "button",
                class: "text-xs text-gray-500 hover:text-red-400 cursor-pointer",
                onclick: () => removeMount(row),
            }, "Remove"),
        ),
    );
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
            }, xIcon({size: 16})),
        ),
        div(
            {class: "flex-1 min-h-0 overflow-auto flex flex-col gap-3 p-4"},
            defaultVolumeCard(form),
            paneSectionDivider("Mount another deployment's default volume"),
            p({class: "text-[11px] text-gray-500 -mt-2"}, "Only deployments on the selected machine are shown."),
            () => div({class: "flex flex-col gap-3"}, ...deploymentRows().map(deploymentRowEl)),
            button({
                type: "button",
                class: "text-xs text-blue-400 hover:text-blue-300 cursor-pointer self-start",
                onclick: addDeploymentMount,
            }, "Add deployment volume mount"),
            paneSectionDivider("Mount custom host directory"),
            () => div({class: "flex flex-col gap-3"}, ...hostRows().map(hostRowEl)),
            button({
                type: "button",
                class: "text-xs text-blue-400 hover:text-blue-300 cursor-pointer self-start",
                onclick: addHostMount,
            }, "Add host path mount"),
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
                form.assetMounts.val = [...form.assetMounts.val, {id: nextAssetMountID++, assetId: asset.id, key: asset.key, version: asset.version, path: '', executable: false}];
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
            }, xIcon({size: 16})),
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

// envVarsPane is the right-hand editor pane. It is always mounted and toggled
// via a CSS class (a binding that returns null would be GC'd by VanJS and never
// re-open).
export function envVarsPane(form, opts = {}) {
    const assets = opts.assets || [];
    const envRows = tbody();
    let envRowsSignature = '';
    van.derive(() => {
        const rows = form.envVars.val || [];
        const signature = [
            rows.map(row => `${row.id}:${row.type || 'value'}:${row.asset || ''}:${row.assetId || 0}:${row.version || 0}`).join('|'),
            (secretRefsS.val || []).map(ref => `${ref.id}:${ref.name}`).join('|'),
            (userConfigRefsS.val || []).map(ref => `${ref.id}:${ref.name}`).join('|'),
            assets.map(asset => `${asset.id}:${asset.key}:${asset.version}`).join('|'),
        ].join('::');
        if (signature === envRowsSignature) return;
        envRowsSignature = signature;
        envRows.replaceChildren(...rows.map(row => envVarRow(form, row, assets)));
    });
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
            }, xIcon({size: 16})),
        ),
        div(
            {class: "flex-1 min-h-0 flex flex-col p-3 overflow-auto"},
            table({class: "w-full text-xs border-collapse"},
                thead(
                    tr({class: "text-left text-gray-400 border-b border-gray-700"},
                        th({class: "pb-1.5 pr-1.5 font-medium"}, "Env name"),
                        th({class: "pb-1.5 px-1.5 font-medium w-24"}, "Type"),
                        th({class: "pb-1.5 pl-1.5 pr-0.5 font-medium"}, "Value"),
                        th({class: "pb-1.5 pl-0.5 font-medium w-12 text-right"}, ""),
                    ),
                ),
                envRows,
                tfoot(
                    tr(td({colSpan: 4, class: "pt-2"},
                        button({
                            type: "button",
                            class: "w-full rounded-md border border-dashed border-gray-600 text-gray-300 hover:border-brand hover:text-white py-1.5 cursor-pointer",
                            onclick: () => { form.envVars.val = [...(form.envVars.val || []), newEnvRow()]; },
                        }, "+ Add environment variable"),
                    )),
                ),
            ),
        ),
    );
}

function envVarRow(form, row, assets) {
    const type = row.type || 'value';
    return tr({class: "border-b border-gray-800 last:border-b-0"},
        td({class: "py-1 pr-1.5 align-top"},
            input({
                type: "text",
                class: "w-full rounded-sm bg-gray-800 border border-gray-700 px-1.5 py-1 text-gray-100 font-mono focus:outline-none focus:ring-1 focus:ring-brand",
                placeholder: "DATABASE_URL",
                value: row.key || '',
                oninput: e => updateEnvRow(form, row.id, {key: e.target.value}),
            }),
        ),
        td({class: "py-1 px-1.5 align-top"},
            select({
                class: "w-full rounded-sm bg-gray-800 border border-gray-700 px-1.5 py-1 text-gray-100 focus:outline-none focus:ring-1 focus:ring-brand",
                value: type,
                onchange: e => updateEnvRow(form, row.id, envTypePatch(row, e.target.value)),
            },
                option({value: "value", selected: type === 'value'}, "Value"),
                option({value: "config", selected: type === 'config'}, "Config"),
                option({value: "secret", selected: type === 'secret'}, "Secret"),
                option({value: "asset", selected: type === 'asset'}, "Asset"),
            ),
        ),
        td({class: "py-1 pl-1.5 pr-0.5 align-top"}, envValueInput(form, row, assets)),
        td({class: "py-1 pl-0.5 align-top text-right"},
            button({
                type: "button",
                class: "text-gray-500 hover:text-red-300 cursor-pointer px-1 py-1",
                onclick: () => { form.envVars.val = (form.envVars.val || []).filter(v => v.id !== row.id); },
            }, "Remove"),
        ),
    );
}

function envValueInput(form, row, assets) {
    if ((row.type || 'value') === 'value') {
        return input({
            type: "text",
            class: "w-full rounded-sm bg-gray-800 border border-gray-700 px-1.5 py-1 text-gray-100 font-mono focus:outline-none focus:ring-1 focus:ring-brand",
            placeholder: "inplace env val",
            value: row.value || '',
            oninput: e => updateEnvRow(form, row.id, {value: e.target.value}),
        });
    }
    if (row.type === 'asset') {
        const assetOptions = assetOptionsForRow(assets, row);
        const selectedKey = van.state(rowAssetOptionValue(row));
        return referencePicker({
            refs: assetOptions,
            selectedKey,
            placeholder: "Search assets",
            noMatchesLabel: "No matching assets",
            emptyLabel: "No assets defined",
            getKey: assetOptionValue,
            getLabel: assetOptionLabel,
            onSelect: asset => {
                selectedKey.val = assetOptionValue(asset);
                updateEnvAssetRow(form, row, asset);
            },
        });
    }
    return envReferenceAutocomplete(form, row);
}

function updateEnvAssetRow(form, row, asset) {
    updateEnvRow(form, row.id, {asset: asset?.key || '', assetId: asset?.id || 0, version: asset?.version || 0});
}

function envReferenceAutocomplete(form, row) {
    const isSecret = row.type === 'secret';
    const selectedID = isSecret ? Number(row.secretId || 0) : Number(row.configId || 0);
    const selectedKey = van.state(selectedID || '');
    const options = () => versionedRefOptions(isSecret ? (secretRefsS.val || []) : (userConfigRefsS.val || []), selectedKey.val);
    return referencePicker({
        refs: options,
        selectedKey,
        placeholder: isSecret ? "Search secrets" : "Search configs",
        noMatchesLabel: `No matching ${isSecret ? 'secrets' : 'configs'}`,
        emptyLabel: `No ${isSecret ? 'secrets' : 'configs'} available`,
        getLabel: ref => `${ref.name} v${ref.version || 0}`,
        onSelect: ref => {
            selectedKey.val = ref.id;
            updateEnvRow(form, row.id, isSecret
                ? {secretId: ref.id}
                : {configId: ref.id});
        },
    });
}

function versionedRefOptions(refs, selectedID) {
    const latestByName = new Map();
    const byID = new Map();
    for (const ref of refs || []) {
        if (!ref || !ref.id) continue;
        byID.set(Number(ref.id), ref);
        const current = latestByName.get(ref.name || '');
        if (!current || Number(ref.version || 0) > Number(current.version || 0)) {
            latestByName.set(ref.name || '', ref);
        }
    }
    const options = Array.from(latestByName.values());
    const selected = byID.get(Number(selectedID || 0));
    if (selected && !options.some(ref => Number(ref.id) === Number(selected.id))) {
        options.push(selected);
    }
    return options.sort((a, b) => (a.name || '').localeCompare(b.name || '') || Number(a.version || 0) - Number(b.version || 0));
}

function newEnvRow(values = {}) {
    return {id: nextEnvID++, key: '', type: 'value', value: '', secretId: 0, configId: 0, asset: '', assetId: 0, version: 0, ...values};
}

function updateEnvRow(form, id, patch) {
    form.envVars.val = (form.envVars.val || []).map(row => row.id === id ? {...row, ...patch} : row);
}

function envTypePatch(row, type) {
    if (type === 'secret') return {type, value: '', configId: 0, asset: '', assetId: 0, version: 0, refSearch: ''};
    if (type === 'config') return {type, value: '', secretId: 0, asset: '', assetId: 0, version: 0, refSearch: ''};
    if (type === 'asset') return {type, value: '', secretId: 0, configId: 0, refSearch: ''};
    return {type: 'value', secretId: 0, configId: 0, asset: '', assetId: 0, version: 0, refSearch: '', value: row.value || ''};
}

function envVarCount(arr) {
    return (arr || []).filter(v => v && v.key && v.key.trim()).length;
}

function formEnvVars(form) {
    return Object.fromEntries((form.envVars.val || [])
        .map(v => {
            const key = (v.key || '').trim();
            if (!key) return null;
            if (v.type === 'secret') return Number(v.secretId || 0) ? [key, {secretId: Number(v.secretId)}] : null;
            if (v.type === 'config') return Number(v.configId || 0) ? [key, {configId: Number(v.configId)}] : null;
            if (v.type === 'asset') return Number(v.assetId || 0) ? [key, {asset: (v.asset || '').trim(), assetId: Number(v.assetId || 0)}] : null;
            return [key, {value: v.value || ''}];
        })
        .filter(Boolean));
}

function formPortForwarding(form) {
    if (Number(form.networkingMode.val) !== NETWORKING_MODE_VIRTUAL) return [];
    return (form.portForwarding.val || [])
        .map(port => ({
            protocol: Number(port.protocol || PORT_FORWARD_PROTOCOL_TCP),
            hostPort: Number(port.hostPort || 0),
            containerPort: Number(port.containerPort || 0),
        }))
        .filter(port => port.hostPort > 0 || port.containerPort > 0);
}

function formCommand(form) {
    return (form.containerCommand.val || '')
        .replace(/\r/g, '')
        .split('\n')
        .map(arg => arg.trim())
        .filter(Boolean);
}

function invalidCommandReason(form) {
    const raw = form.containerCommand.val || '';
    if (raw.includes('\0')) return 'Command arguments cannot contain NUL bytes.';
    return '';
}

function formAssetMounts(form) {
    return (form.assetMounts.val || [])
        .map(m => ({asset: (m.key || '').trim(), assetId: Number(m.assetId || 0), path: (m.path || '').trim(), executable: Boolean(m.executable)}))
        .filter(m => m.assetId && m.path);
}

function formVolumeMounts(form) {
    return (form.volumeMounts.val || [])
        .map(m => {
            const deploymentId = Number(m.deploymentId || 0);
            const host = (m.kind === 'deployment' && deploymentId) ? defaultVolumeHostPath(deploymentId) : (m.host || '').trim();
            return {host, container: (m.container || '').trim(), readonly: Boolean(m.readonly)};
        })
        .filter(m => m.host && m.container);
}

function defaultVolumeFallbackContainerPath() {
    return '/data';
}

function hasInvalidVolumeConfig(form, opts = {}) {
    return Boolean(invalidVolumeConfigReason(form, opts));
}

function invalidVolumeConfigReason(form, opts = {}) {
    const path = form.containerDataMountPath.val.trim();
    if (path && !validAbsolutePath(path)) return 'Data mount path must be an absolute path without trailing slash or dot segments.';
    const deploymentOptions = deploymentVolumeOptions(optionDeployments(opts), form);
    for (const m of form.volumeMounts.val || []) {
        const deploymentId = Number(m.deploymentId || 0);
        const host = (m.kind === 'deployment' && deploymentId) ? defaultVolumeHostPath(deploymentId) : (m.host || '').trim();
        const container = (m.container || '').trim();
        if (!host && !container && !deploymentId) continue;
        if (m.kind === 'deployment' && !deploymentOptions.some(d => d.config?.id === deploymentId)) return 'Select a deployment volume source.';
        if (!validAbsolutePath(host)) return 'Volume host path must be an absolute path without trailing slash or dot segments.';
        if (!validAbsolutePath(container)) return 'Volume container path must be an absolute path without trailing slash or dot segments.';
    }
    return '';
}

function hasInvalidAssetMounts(form) {
    return Boolean(invalidAssetMountsReason(form));
}

function invalidAssetMountsReason(form) {
    for (const m of form.assetMounts.val || []) {
        const assetId = Number(m.assetId || 0);
        const path = (m.path || '').trim();
        if (!assetId) continue;
        if (!validAbsolutePath(path)) return 'Asset mount path must be an absolute file path without trailing slash or dot segments.';
    }
    return '';
}

function hasInvalidUpgradeStrategy(form) {
    return Boolean(invalidUpgradeStrategyReason(form));
}

function invalidDevShmReason(form) {
	if (!form.containerDevShmOverride.val) return '';
	const raw = form.containerDevShmSizeValue.val.trim();
	if (!raw) return 'Shared /dev/shm override is required when enabled.';
	if (!/^[0-9]+$/.test(raw)) return 'Shared /dev/shm size must be a whole number.';
	const value = Number(raw);
	if (!Number.isSafeInteger(value) || value <= 0) return 'Shared /dev/shm size must be greater than zero.';
	const factor = devShmUnitFactorKB(form.containerDevShmSizeUnit.val);
	if (!factor) return 'Select a valid shared /dev/shm unit.';
	const kb = value * factor;
	if (!Number.isSafeInteger(kb) || kb > DEV_SHM_MAX_KB) return `Shared /dev/shm size must be ${DEV_SHM_MAX_KB} KiB or less.`;
	return '';
}

function invalidFileDescriptorLimitReason(form) {
	if (!form.containerFileDescriptorLimitOverride.val) return '';
	const raw = form.containerFileDescriptorLimit.val.trim();
	if (!raw) return 'File descriptor limit override is required when enabled.';
	if (!/^[0-9]+$/.test(raw)) return 'File descriptor limit must be a whole number.';
	const value = Number(raw);
	if (!Number.isSafeInteger(value) || value <= 0) return 'File descriptor limit must be greater than zero.';
	if (value > FILE_DESCRIPTOR_LIMIT_MAX) return `File descriptor limit must be ${FILE_DESCRIPTOR_LIMIT_MAX} or less.`;
	return '';
}

function invalidUpgradeStrategyReason(form) {
    const strategy = Number(form.containerUpgradeStrategy.val || CONTAINER_UPGRADE_RECREATE);
    if (strategy !== CONTAINER_UPGRADE_RECREATE && strategy !== CONTAINER_UPGRADE_ROLLOVER) return 'Select a valid upgrade strategy.';
    if (strategy !== CONTAINER_UPGRADE_ROLLOVER) return '';
    const timeout = Number(form.containerReadinessTimeoutSeconds.val || 0);
    return !Number.isFinite(timeout) || timeout < 0 ? 'Readiness timeout must be zero or greater.' : '';
}

function hasInvalidEnvVars(form) {
    return Boolean(invalidEnvVarsReason(form));
}

function invalidEnvVarsReason(form) {
    const seen = new Set();
    for (const row of form.envVars.val || []) {
        const key = (row.key || '').trim();
        if (!key) continue;
        if (seen.has(key)) return `Environment variable "${key}" is duplicated.`;
        seen.add(key);
        if (row.type === 'secret') {
            if (!Number(row.secretId || 0)) return `Select a secret for environment variable "${key}".`;
            continue;
        }
        if (row.type === 'config') {
            if (!Number(row.configId || 0)) return `Select a config for environment variable "${key}".`;
            continue;
        }
        if (row.type === 'asset') {
            if (!(row.asset || '').trim()) return `Select an asset for environment variable "${key}".`;
            continue;
        }
        if (row.type && row.type !== 'value') return `Select a valid type for environment variable "${key}".`;
    }
    return '';
}

function envVarsToFormRows(envVars) {
    return Object.entries(envVars || {})
        .map(([key, value], index) => ({key, value, index}))
        .sort((a, b) => a.key.localeCompare(b.key) || a.index - b.index)
        .map(({key, value}) => {
        const secretId = Number(value?.secretId || 0);
        const configId = Number(value?.configId || 0);
        const assetId = Number(value?.assetId || 0);
        const version = Number(value?.version || 0);
        if (secretId) return newEnvRow({key, type: 'secret', secretId});
        if (configId) return newEnvRow({key, type: 'config', configId});
        if (assetId) return newEnvRow({key, type: 'asset', asset: value?.asset || '', assetId, version});
        return newEnvRow({key, type: 'value', value: value?.value || ''});
    });
}

function validAbsolutePath(path) {
    return path.startsWith('/')
        && !path.endsWith('/')
        && !path.endsWith('/.')
        && !path.endsWith('/..')
        && !path.includes('//')
        && !path.includes('/../')
        && !path.includes('/./')
        && path !== '/';
}

function mountToFormRow(m) {
    const host = m.host || '';
    const match = host.match(DEPLOYMENT_VOLUME_HOST_RE);
    if (match) {
        return {id: nextVolumeMountID++, kind: 'deployment', deploymentId: Number(match[1]), host, container: m.container || '', readonly: Boolean(m.readonly)};
    }
    return {id: nextVolumeMountID++, kind: 'host', host, container: m.container || '', readonly: Boolean(m.readonly)};
}

function defaultVolumeHostPath(deploymentID) {
    return `/var/lib/opendeploy-volumes/${deploymentID}/default`;
}

function deploymentVolumeOptions(deployments, form) {
    const machine = form.machine.val.trim();
    const currentID = Number(form.deploymentId.val || 0);
    return (deployments || [])
        .filter(d => d.config?.id && d.config.id !== currentID && !d.config?.deleted && d.config?.configId?.machine === machine)
        .sort((a, b) => deploymentVolumeLabel(a).localeCompare(deploymentVolumeLabel(b)));
}

function optionDeployments(opts) {
    const deployments = opts.deployments;
    if (!deployments) return [];
    return Array.isArray(deployments) ? deployments : (deployments.val || []);
}

function deploymentVolumeLabel(deployment) {
    const id = deployment.config?.configId || {};
    const space = spaceName(id.spaceId);
    return `${id.name || `deployment ${deployment.config?.id}`} (${space})`;
}

function spaceName(id) {
    const space = (spacesS.val || []).find(s => s.id === id);
    return space?.name || `space ${id || 0}`;
}

function paneSectionDivider(text) {
    return div(
        {class: "flex items-center gap-3 mt-2 first:mt-0"},
        div({class: "flex-1 border-t border-gray-700"}),
        span({class: "text-xs font-semibold uppercase tracking-wide text-gray-400 text-center"}, text),
        div({class: "flex-1 border-t border-gray-700"}),
    );
}

function closeRuntimePanes(form, keep) {
    if (keep !== 'env') form.envPaneOpen.val = false;
    if (keep !== 'assets') form.assetMountsPaneOpen.val = false;
    if (keep !== 'volumes') form.volumeMountsPaneOpen.val = false;
    if (keep !== 'strategy') form.upgradeStrategyPaneOpen.val = false;
    if (keep !== 'resources') form.resourcesPaneOpen.val = false;
    if (keep !== 'networking') form.networkingPaneOpen.val = false;
    if (keep !== 'assetEditor') form.assetEditorOpen.val = false;
}

function devShmFormState(kb) {
    const value = Number(kb || 0);
    if (!Number.isFinite(value) || value <= 0) return {value: '', unit: DEV_SHM_DEFAULT_UNIT};
    for (const unit of [...DEV_SHM_UNITS].reverse()) {
        if (value % unit.factorKB === 0) {
            return {value: String(value / unit.factorKB), unit: unit.value};
        }
    }
    return {value: String(value), unit: 'KB'};
}

function devShmUnitFactorKB(unit) {
    return DEV_SHM_UNITS.find(item => item.value === unit)?.factorKB || 0;
}

function devShmSizeKbForForm(form) {
    if (invalidDevShmReason(form)) return 0;
    const raw = form.containerDevShmSizeValue.val.trim();
    if (!raw) return 0;
    return Number(raw) * devShmUnitFactorKB(form.containerDevShmSizeUnit.val);
}

function fileDescriptorLimitForForm(form) {
    if (invalidFileDescriptorLimitReason(form)) return 0;
    const raw = form.containerFileDescriptorLimit.val.trim();
    if (!raw) return 0;
    return Number(raw);
}

function devShmSummaryText(form) {
	if (!form.containerDevShmOverride.val) return `${DEV_SHM_DEFAULT_VALUE} ${DEV_SHM_DEFAULT_UNIT}`;
	const raw = form.containerDevShmSizeValue.val.trim();
	if (!raw) return `${DEV_SHM_DEFAULT_VALUE} ${DEV_SHM_DEFAULT_UNIT}`;
	return `${raw} ${form.containerDevShmSizeUnit.val}`;
}

function fileDescriptorLimitSummaryText(form) {
	if (!form.containerFileDescriptorLimitOverride.val) return FILE_DESCRIPTOR_LIMIT_DEFAULT;
	const raw = form.containerFileDescriptorLimit.val.trim();
	if (!raw) return FILE_DESCRIPTOR_LIMIT_DEFAULT;
	return raw;
}

function selectedDevShmUnit(form) {
	return form.containerDevShmOverride.val ? form.containerDevShmSizeUnit.val : DEV_SHM_DEFAULT_UNIT;
}

function ensureDevShmOverrideValue(form) {
	if (!form.containerDevShmSizeValue.val.trim()) {
		form.containerDevShmSizeValue.val = DEV_SHM_DEFAULT_VALUE;
	}
	if (!devShmUnitFactorKB(form.containerDevShmSizeUnit.val)) {
		form.containerDevShmSizeUnit.val = DEV_SHM_DEFAULT_UNIT;
	}
}

function ensureFileDescriptorLimitOverrideValue(form) {
	if (!form.containerFileDescriptorLimit.val.trim()) {
		form.containerFileDescriptorLimit.val = FILE_DESCRIPTOR_LIMIT_DEFAULT;
	}
}

function resetDevShmOverrideValue(form) {
	form.containerDevShmSizeValue.val = DEV_SHM_DEFAULT_VALUE;
	form.containerDevShmSizeUnit.val = DEV_SHM_DEFAULT_UNIT;
}

function resetFileDescriptorLimitOverrideValue(form) {
	form.containerFileDescriptorLimit.val = FILE_DESCRIPTOR_LIMIT_DEFAULT;
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
    if (form.deploymentCreationUpdate) {
        const sourceID = repoState.val.trim();
        if (!sourceID || form.sourceType.val !== sourceType) return {status: 'idle', message: ''};
        const validity = sourceType === SOURCE_DOCKER_IMAGE
            ? form.deploymentCreationUpdate.imageValid.val
            : form.deploymentCreationUpdate.repoValid.val;
        return validity.fieldKey === form.deploymentCreationUpdate.repoValidityKey(sourceType, sourceID)
            ? validity
            : {status: 'idle', message: ''};
    }
    const c = form.repoCheck.val;
    const repo = repoState.val.trim();
    if (!repo || c.sourceType !== sourceType || c.repo !== repo || c.sourceKey !== sourceValidationKey(form)) {
        return {status: 'idle', message: ''};
    }
    return c;
}

function activeFlakeCheck(form) {
    if (form.deploymentCreationUpdate) {
        if (form.sourceType.val !== SOURCE_NIX_DOCKER || !form.nixRepo.val.trim() || !form.nixFlake.val.trim()) {
            return {status: 'idle', message: ''};
        }
        const validity = form.deploymentCreationUpdate.flakePathValid.val;
        return validity.fieldKey === form.deploymentCreationUpdate.flakeValidityKey()
            ? validity
            : {status: 'idle', message: ''};
    }
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
    if (form.deploymentCreationUpdate) {
        if (!form.containerImage.val.trim() || form.sourceType.val !== SOURCE_DOCKER_IMAGE) return {status: 'idle', message: ''};
        const validity = form.deploymentCreationUpdate.imageValid.val;
        return validity.fieldKey === form.deploymentCreationUpdate.repoValidityKey(SOURCE_DOCKER_IMAGE, form.containerImage.val)
            ? validity
            : {status: 'idle', message: ''};
    }
    const c = form.repoCheck.val;
    const image = form.containerImage.val.trim();
    if (!image || form.sourceType.val !== SOURCE_DOCKER_IMAGE || c.sourceKey !== sourceValidationKey(form)) {
        return {status: 'idle', message: ''};
    }
    return c;
}

async function validateRepo(form) {
    if (form.deploymentCreationUpdate) {
        return form.deploymentCreationUpdate.validateRepo();
    }
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
        const req = buildValidateSourceRequest(form);
        const res = await capi.postV1RepoValidate(req);
        console.log('[opendeploy] repo validate response', {request: req, response: res});
        form.repoCheck.val = sourceCheckFromValidation(form, res, repo, sourceType, sourceKey);
    } catch (e) {
        console.error('[opendeploy] repo validate failed', {error: e, stack: e?.stack});
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
        "Mode": "deployment-networking-mode-select",
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

function publicSpaceOptions(spaces, currentSpaceID) {
    const current = Number(currentSpaceID ?? DEFAULT_SPACE_ID);
    const publicSpaces = (spaces || [{id: DEFAULT_SPACE_ID, name: 'default'}])
        .filter(space => Number(space.id) !== INTERNAL_SPACE_ID);
    if (current !== INTERNAL_SPACE_ID && !publicSpaces.some(space => Number(space.id) === current)) {
        publicSpaces.push({id: current, name: `space ${current}`});
    }
    if (publicSpaces.length === 0) return [{id: DEFAULT_SPACE_ID, name: 'default'}];
    return publicSpaces;
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
