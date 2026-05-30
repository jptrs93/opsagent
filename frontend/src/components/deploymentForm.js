import van from "vanjs-core";
import {capi} from "../capi/index.js";

const { div, h3, label, input, select, option, button, p, span, datalist } = van.tags;

const SOURCE_GITHUB = 'githubRelease';
const SOURCE_NIX = 'nixBuild';
const RUNNER_OS = 'osProcess';
const RUNNER_SYSTEMD = 'systemd';

let nextDatalistID = 1;
let nextEnvID = 1;

export function emptyDeploymentForm() {
    return makeFormState({
        name: '',
        environment: '',
        machine: '',
        sourceType: SOURCE_NIX,
        nixRepo: '',
        nixFlake: '',
        nixOutputExecutable: '',
        githubRepo: '',
        githubAsset: '',
        githubTag: '',
        runnerType: RUNNER_OS,
        osWorkingDir: '',
        osRunAs: '',
        osStrategy: '',
        systemdName: '',
        systemdBinPath: '',
    });
}

export function deploymentConfigToForm(cfg) {
    const cid = cfg?.configId || {};
    const spec = cfg?.spec || {};
    const prepare = spec.prepare || {};
    const runner = spec.runner || {};
    const nix = prepare.nixBuild || {};
    const gh = prepare.githubRelease || {};
    const os = runner.osProcess || {};
    const systemd = runner.systemd || {};

    // Reveal a section's additional options up-front when the existing config
    // already sets one of them, so they aren't hidden on edit.
    const showSourceOpts = Boolean(gh.asset || nix.outputExecutable);
    const showExecOpts = Boolean(os.workingDir || os.runAs || os.strategy || (os.env && os.env.length));

    return makeFormState({
        name: cid.name || '',
        environment: cid.environment || '',
        machine: cid.machine || '',
        sourceType: prepare.githubRelease ? SOURCE_GITHUB : SOURCE_NIX,
        nixRepo: nix.repo || '',
        nixFlake: nix.flake || '',
        nixOutputExecutable: nix.outputExecutable || '',
        githubRepo: gh.repo || '',
        githubAsset: gh.asset || '',
        githubTag: gh.tag || '',
        runnerType: runner.systemd ? RUNNER_SYSTEMD : RUNNER_OS,
        osWorkingDir: os.workingDir || '',
        osRunAs: os.runAs || '',
        osStrategy: os.strategy || '',
        systemdName: systemd.name || '',
        systemdBinPath: systemd.binPath || '',
        envVars: (os.env || []).map(e => ({id: nextEnvID++, key: e.key || '', value: e.value || ''})),
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

    return div(
        {class: "flex flex-col gap-5"},
        // Identity — the first section carries no divider/title; it's the top of the form.
        div(
            {class: "flex flex-col gap-3"},
            identityLocked
                ? span({class: "text-xs text-orange-300 self-end"}, "Deployment identity is fixed after creation.")
                : null,
            div(
                {class: "grid grid-cols-1 md:grid-cols-3 gap-3"},
                field("Name", input({
                    type: "text",
                    value: form.name.rawVal,
                    disabled: identityLocked,
                    class: () => textInputClass(nameValid(form), identityLocked, nameValid(form)),
                    placeholder: "my-service",
                    oninput: e => { form.name.val = e.target.value; },
                })),
                field("Environment (optional)", div(
                    input({
                        type: "text",
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
                )),
                field("Machine", machineSelect(form, {
                    identityLocked,
                    machineOptionsLoaded,
                    machineOptionValues,
                })),
            ),
        ),
        sectionDivider("Binary source"),
        div(
            {class: "flex flex-col gap-3"},
            inlineSelect("Source type", form.sourceType, [
                {value: SOURCE_GITHUB, label: "Github release"},
                {value: SOURCE_NIX, label: "Build NIX store"},
            ]),
            () => form.sourceType.val === SOURCE_GITHUB
                ? div(
                    {class: "grid grid-cols-1 gap-3"},
                    repoField(form, SOURCE_GITHUB),
                )
                : div(
                    {class: "grid grid-cols-1 md:grid-cols-2 gap-3"},
                    repoField(form, SOURCE_NIX),
                    field("Flake", textInput(form.nixFlake, "nix/app/flake.nix")),
                ),
            optionsDisclosure(form.showSourceOpts, () => sourceOptions(form)),
        ),
        sectionDivider("Execution"),
        div(
            {class: "flex flex-col gap-3"},
            inlineSelect("Runner type", form.runnerType, [
                {value: RUNNER_OS, label: "OpsAgent process"},
                {value: RUNNER_SYSTEMD, label: "systemd service"},
            ]),
            () => form.runnerType.val === RUNNER_SYSTEMD
                ? div(
                    {class: "grid grid-cols-1 md:grid-cols-2 gap-3"},
                    field("Unit name", textInput(form.systemdName, "my-service.service")),
                    field("Binary path", textInput(form.systemdBinPath, "/opt/my-service/bin/app")),
                )
                : div(
                    {class: "flex flex-col gap-3"},
                    p({class: "text-xs text-gray-500"}, "Runs with sensible defaults."),
                    optionsDisclosure(form.showExecOpts, () => execOptions(form)),
                ),
        ),
    );
}

export function formToYaml(form) {
    const obj = {
        name: form.name.val.trim(),
        machine: form.machine.val.trim(),
        prepare: {},
        runner: {},
    };
    const environment = form.environment.val.trim();
    if (environment) obj.environment = environment;

    if (form.sourceType.val === SOURCE_GITHUB) {
        obj.prepare.githubRelease = {
            repo: form.githubRepo.val.trim(),
            asset: form.githubAsset.val.trim(),
            tag: form.githubTag.val.trim(),
        };
    } else {
        obj.prepare.nixBuild = {
            repo: form.nixRepo.val.trim(),
            flake: form.nixFlake.val.trim(),
            outputExecutable: form.nixOutputExecutable.val.trim(),
        };
    }

    if (form.runnerType.val === RUNNER_SYSTEMD) {
        obj.runner.systemd = {
            name: form.systemdName.val.trim(),
            binPath: form.systemdBinPath.val.trim(),
        };
    } else {
        obj.runner.osProcess = {
            workingDir: form.osWorkingDir.val.trim(),
            runAs: form.osRunAs.val.trim(),
            strategy: form.osStrategy.val,
        };
        const env = form.envVars.val
            .map(v => ({key: v.key.trim(), value: v.value}))
            .filter(v => v.key);
        if (env.length) obj.runner.osProcess.env = env;
    }

    return toYaml(obj);
}

export function isFormValid(form, opts = {}) {
    if (!nameValid(form) || !form.machine.val.trim()) return false;
    const machineOptions = opts.machineOptions || [];
    const machineOptionValues = machineOptions.map(m => typeof m === 'string' ? m : m.name).filter(Boolean);
    if (machineOptionValues.length > 0 && !machineOptionValues.includes(form.machine.val.trim())) return false;
    if (form.sourceType.val === SOURCE_GITHUB) {
        if (!form.githubRepo.val.trim()) return false;
    } else if (!form.nixRepo.val.trim() || !form.nixFlake.val.trim()) {
        return false;
    }
    if (form.runnerType.val === RUNNER_SYSTEMD) {
        return Boolean(form.systemdName.val.trim() && form.systemdBinPath.val.trim());
    }
    return true;
}

export function configToYaml(cfg) {
    return formToYaml(deploymentConfigToForm(cfg));
}

function makeFormState(values) {
    return {
        name: van.state(values.name),
        environment: van.state(values.environment),
        machine: van.state(values.machine),
        sourceType: van.state(values.sourceType),
        nixRepo: van.state(values.nixRepo),
        nixFlake: van.state(values.nixFlake),
        nixOutputExecutable: van.state(values.nixOutputExecutable),
        githubRepo: van.state(values.githubRepo),
        githubAsset: van.state(values.githubAsset),
        githubTag: van.state(values.githubTag),
        runnerType: van.state(values.runnerType),
        osWorkingDir: van.state(values.osWorkingDir),
        osRunAs: van.state(values.osRunAs),
        osStrategy: van.state(values.osStrategy),
        systemdName: van.state(values.systemdName),
        systemdBinPath: van.state(values.systemdBinPath),
        envVars: van.state(values.envVars || []),
        showSourceOpts: van.state(Boolean(values.showSourceOpts)),
        showExecOpts: van.state(Boolean(values.showExecOpts)),
        // Transient repo-accessibility check; tracks the repo/source it applies
        // to so a stale result is hidden once the inputs change.
        repoCheck: van.state({status: 'idle', message: '', repo: '', sourceType: ''}),
    };
}

// sectionDivider renders a thin horizontal rule with the section title centered,
// splitting the line: ──────── Title ────────
function sectionDivider(title) {
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
        hint ? span({class: "text-[11px] text-gray-500"}, hint) : null,
    );
}

// --- Additional (optional) options -----------------------------------------

// optionsDisclosure renders a thin horizontal rule with an expand/collapse
// toggle (no surrounding card) that reveals a section's optional fields.
//
// The content node is always mounted and toggled via a CSS class rather than
// conditionally returned: a child binding that returns null gets a null _dom,
// which VanJS's keepConnected() GC drops on the next update cycle — leaving the
// disclosure unable to re-render.
function optionsDisclosure(open, content) {
    return div(
        {class: "border-t border-gray-700 pt-2"},
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

function sourceOptions(form) {
    return () => form.sourceType.val === SOURCE_GITHUB
        ? field("Asset", textInput(form.githubAsset, "app-linux-amd64"), "Release asset to download. Defaults to the release's only asset.")
        : field("Output executable", textInput(form.nixOutputExecutable, "app"), "Executable name in the build output's bin/. Defaults to the only executable.");
}

function execOptions(form) {
    return div(
        {class: "flex flex-col gap-3"},
        div(
            {class: "grid grid-cols-1 md:grid-cols-3 gap-3"},
            field("Run as", textInput(form.osRunAs, "ubuntu"), "OS user to run as. Defaults to the ubuntu user."),
            field("Working directory", input({
                type: "text",
                value: form.osWorkingDir.rawVal,
                class: textInputClass(),
                placeholder: () => `/home/${form.osRunAs.val.trim() || 'ubuntu'}`,
                oninput: e => { form.osWorkingDir.val = e.target.value; },
            }), "Defaults to the run-as user's home directory."),
            field("Strategy", select({
                class: selectClass(),
                onchange: e => { form.osStrategy.val = e.target.value; },
            },
                option({value: '', selected: form.osStrategy.val === ''}, "Terminate previous"),
                option({value: 'leavePrevious', selected: form.osStrategy.val === 'leavePrevious'}, "Leave previous running"),
            )),
        ),
        environmentVarsEditor(form),
    );
}

// --- Repository field with on-blur accessibility validation ----------------

function repoField(form, sourceType) {
    const repoState = sourceType === SOURCE_GITHUB ? form.githubRepo : form.nixRepo;
    return label(
        {class: "flex flex-col gap-1 text-xs text-gray-400"},
        span("Repository"),
        input({
            type: "text",
            value: repoState.rawVal,
            placeholder: "github.com/org/repo",
            class: () => repoInputClass(activeRepoCheck(form, sourceType, repoState).status),
            oninput: e => { repoState.val = e.target.value; },
            onblur: () => validateRepo(form),
        }),
        () => {
            const c = activeRepoCheck(form, sourceType, repoState);
            if (c.status === 'idle') return span();
            return p({class: repoMsgClass(c.status)}, c.message);
        },
    );
}

// activeRepoCheck returns the validation result only if it still matches the
// repo and source currently in the field; otherwise it reads as idle so a stale
// green/red state disappears the moment the user edits or switches source.
function activeRepoCheck(form, sourceType, repoState) {
    const c = form.repoCheck.val;
    const repo = repoState.val.trim();
    if (!repo || c.sourceType !== sourceType || c.repo !== repo) {
        return {status: 'idle', message: ''};
    }
    return c;
}

async function validateRepo(form) {
    const sourceType = form.sourceType.val;
    const repoState = sourceType === SOURCE_GITHUB ? form.githubRepo : form.nixRepo;
    const repo = repoState.val.trim();
    if (!repo) {
        form.repoCheck.val = {status: 'idle', message: '', repo: '', sourceType};
        return;
    }
    const c = form.repoCheck.val;
    // Don't re-check a repo we already have a verdict for.
    if (c.repo === repo && c.sourceType === sourceType && (c.status === 'ok' || c.status === 'error')) {
        return;
    }
    form.repoCheck.val = {status: 'checking', message: 'Checking repository access…', repo, sourceType};
    try {
        const res = await capi.postV1RepoValidate({repo, sourceType});
        form.repoCheck.val = {
            status: res.ok ? 'ok' : 'error',
            message: res.message || (res.ok ? 'Repository is accessible.' : 'Repository not accessible.'),
            repo,
            sourceType,
        };
    } catch (e) {
        form.repoCheck.val = {status: 'error', message: e.message || 'Validation failed.', repo, sourceType};
    }
}

function repoInputClass(status) {
    let border = 'border-gray-600 focus:ring-brand';
    if (status === 'ok') border = 'border-green-500 focus:ring-green-500';
    else if (status === 'error') border = 'border-red-500 focus:ring-red-500';
    return `w-full px-3 py-2 rounded-lg bg-gray-800 text-gray-100 border ${border} focus:outline-none focus:ring-1`;
}

function repoMsgClass(status) {
    if (status === 'ok') return 'text-xs text-green-400';
    if (status === 'error') return 'text-xs text-red-400';
    return 'text-xs text-gray-500';
}

function inlineSelect(text, state, options) {
    return label(
        {class: "flex items-center justify-end gap-2 text-xs text-gray-400"},
        span({class: "whitespace-nowrap"}, text),
        select({
            class: "h-8 w-48 rounded-lg bg-gray-800 text-gray-100 border border-gray-600 px-2 focus:outline-none focus:ring-1 focus:ring-brand",
            onchange: e => { state.val = e.target.value; },
        },
            ...options.map(opt => option({value: opt.value, selected: state.rawVal === opt.value}, opt.label)),
        ),
    );
}

function textInput(state, placeholder = '') {
    return input({
        type: "text",
        value: state.rawVal,
        class: textInputClass(),
        placeholder,
        oninput: e => { state.val = e.target.value; },
    });
}

function textInputClass(valid = false, disabled = false, success = false) {
    const border = success ? 'border-green-500 focus:ring-green-500' : 'border-gray-600 focus:ring-brand';
    const muted = disabled ? 'opacity-70 cursor-not-allowed' : '';
    return `w-full px-3 py-2 rounded-lg bg-gray-800 text-gray-100 border ${border} focus:outline-none focus:ring-1 ${muted}`;
}

function selectClass() {
    return "w-full px-3 py-2 rounded-lg bg-gray-800 text-gray-100 border border-gray-600 focus:outline-none focus:ring-1 focus:ring-brand";
}

function machineSelect(form, opts) {
    const current = form.machine.rawVal;
    const extraCurrent = current && !opts.machineOptionValues.includes(current)
        ? [option({value: current, selected: true}, current)]
        : [];
    return select({
        class: selectClass(),
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

function environmentVarsEditor(form) {
    const addEnvVar = () => {
        form.envVars.val = [...form.envVars.val, {id: nextEnvID++, key: '', value: ''}];
    };
    const updateEnvVar = (id, patch) => {
        form.envVars.val = form.envVars.val.map(v => v.id === id ? {...v, ...patch} : v);
    };
    const removeEnvVar = (id) => {
        form.envVars.val = form.envVars.val.filter(v => v.id !== id);
    };

    return div(
        {class: "border-t border-gray-700 pt-3"},
        div(
            {class: "flex items-center justify-between mb-2"},
            div(
                h3({class: "text-xs font-semibold text-gray-300"}, "Environment variables"),
            ),
            button({
                class: "text-xs px-2 py-1 rounded bg-gray-700 hover:bg-gray-600 text-gray-200 cursor-pointer",
                onclick: addEnvVar,
                type: "button",
            }, "Add variable"),
        ),
        () => form.envVars.val.length === 0
            ? p({class: "text-xs text-gray-500"}, "No environment variables added.")
            : div(
                {class: "flex flex-col gap-2"},
                ...form.envVars.val.map(v => div(
                    {class: "grid grid-cols-[1fr_1fr_auto] gap-2 items-center"},
                    input({
                        type: "text",
                        value: v.key,
                        class: textInputClass(),
                        placeholder: "KEY",
                        oninput: e => updateEnvVar(v.id, {key: e.target.value}),
                    }),
                    input({
                        type: "text",
                        value: v.value,
                        class: textInputClass(),
                        placeholder: "value",
                        oninput: e => updateEnvVar(v.id, {value: e.target.value}),
                    }),
                    button({
                        class: "text-xs text-gray-500 hover:text-red-400 cursor-pointer px-2",
                        onclick: () => removeEnvVar(v.id),
                        type: "button",
                    }, "Remove"),
                )),
            ),
    );
}

function toYaml(obj, indent = 0) {
    const lines = [];
    const pad = '  '.repeat(indent);
    for (const [key, val] of Object.entries(obj)) {
        if (val === undefined || val === null || val === '') continue;
        if (Array.isArray(val)) {
            if (val.length === 0) continue;
            lines.push(`${pad}${key}:`);
            const itemPad = '  '.repeat(indent + 1);
            for (const item of val) {
                const entries = (item !== null && typeof item === 'object')
                    ? Object.entries(item)
                    : [[null, item]];
                let first = true;
                for (const [k, v] of entries) {
                    if (v === undefined || v === null) continue;
                    const scalar = typeof v === 'string' ? JSON.stringify(v) : String(v);
                    const body = k === null ? scalar : `${k}: ${scalar}`;
                    lines.push(`${itemPad}${first ? '- ' : '  '}${body}`);
                    first = false;
                }
            }
        } else if (typeof val === 'object') {
            lines.push(`${pad}${key}:`);
            const nested = toYaml(val, indent + 1);
            if (nested) lines.push(nested);
        } else {
            lines.push(`${pad}${key}: ${String(val)}`);
        }
    }
    return lines.join('\n');
}
