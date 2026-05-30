import van from "vanjs-core";
import {X} from "vanjs-feather";
import {capi} from "../capi/index.js";

const { div, h3, label, input, select, option, button, p, span, datalist, textarea } = van.tags;

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
        githubDownloadScript: '',
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
    const showSourceOpts = Boolean(gh.asset || gh.downloadScript || nix.outputExecutable);
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
        githubDownloadScript: gh.downloadScript || '',
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
        sectionDivider("Deployment identity"),
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
            div(
                {class: "flex items-start gap-4"},
                selectField("Source type", form.sourceType, [
                    {value: SOURCE_GITHUB, label: "Github release"},
                    {value: SOURCE_NIX, label: "Build NIX store"},
                ]),
                () => repoField(form, form.sourceType.val),
            ),
            () => form.sourceType.val === SOURCE_NIX
                ? field("Flake", textInput(form.nixFlake, "nix/app/flake.nix"))
                : span(),
            optionsDisclosure(form.showSourceOpts, () => sourceOptions(form)),
        ),
        sectionDivider("Execution"),
        div(
            {class: "flex flex-col gap-3"},
            selectField("Runner type", form.runnerType, [
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
            downloadScript: form.githubDownloadScript.val.replace(/\s+$/, ''),
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
        githubDownloadScript: van.state(values.githubDownloadScript || ''),
        runnerType: van.state(values.runnerType),
        osWorkingDir: van.state(values.osWorkingDir),
        osRunAs: van.state(values.osRunAs),
        osStrategy: van.state(values.osStrategy),
        systemdName: van.state(values.systemdName),
        systemdBinPath: van.state(values.systemdBinPath),
        envVars: van.state(values.envVars || []),
        showSourceOpts: van.state(Boolean(values.showSourceOpts)),
        showExecOpts: van.state(Boolean(values.showExecOpts)),
        // Whether the environment-variables editor pane is open in the overlay.
        envPaneOpen: van.state(false),
        // Transient repo-accessibility check; tracks the repo/source it applies
        // to so a stale result is hidden once the inputs change.
        repoCheck: van.state({status: 'idle', message: '', repo: '', sourceType: ''}),
        // Transient github-asset check; tracks the repo/asset it applies to so a
        // stale result is hidden once either input changes.
        assetCheck: van.state({status: 'idle', message: '', repo: '', asset: ''}),
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
        hint ? span({class: "text-[11px] text-gray-500"}, hint) : null,
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

function sourceOptions(form) {
    return () => form.sourceType.val === SOURCE_GITHUB
        ? div({class: "flex flex-col gap-3"}, assetField(form), downloadScriptField(form))
        : field("Output executable", textInput(form.nixOutputExecutable, "app"), "Executable name in the build output's bin/. Defaults to the only executable.");
}

// downloadScriptField lets the operator supply a bash script that downloads the
// artifact instead of pulling a release asset directly. The script runs in the
// release download dir with the version tag as $1 and must leave the runnable
// artifact there (named by Asset, or the only file it produces).
function downloadScriptField(form) {
    return label(
        {class: "flex flex-col gap-1 text-xs text-gray-400"},
        span("Custom download script"),
        textarea({
            class: "w-full h-32 resize-y rounded-lg bg-gray-800 text-gray-100 border border-gray-600 px-3 py-2 font-mono text-xs leading-relaxed focus:outline-none focus:ring-1 focus:ring-brand",
            spellcheck: "false",
            placeholder: "#!/usr/bin/env bash\nset -euo pipefail\nversion=\"$1\"\ncurl -fsSL -o app \"https://example.com/$version/app\"",
            value: form.githubDownloadScript.rawVal,
            oninput: e => { form.githubDownloadScript.val = e.target.value; },
        }),
        span({class: "text-[11px] text-gray-500"}, "Optional. Runs instead of downloading an asset. Receives the version tag as $1 and runs in the download directory; leave the built executable there. GITHUB_TOKEN is available in the environment."),
    );
}

// assetField mirrors repoField: on blur it checks (when filled) that the named
// release asset exists in at least one published release of the configured repo.
function assetField(form) {
    return label(
        {class: "flex flex-col gap-1 text-xs text-gray-400"},
        span("Asset"),
        input({
            type: "text",
            value: form.githubAsset.rawVal,
            placeholder: "app-linux-amd64",
            class: () => repoInputClass(activeAssetCheck(form).status),
            oninput: e => { form.githubAsset.val = e.target.value; },
            onblur: () => validateAsset(form),
        }),
        () => {
            const c = activeAssetCheck(form);
            if (c.status !== 'idle') {
                return p({class: repoMsgClass(c.status)}, c.message);
            }
            // With a custom download script the asset is no longer a release
            // asset — it names the file the script leaves in the download dir.
            const scripted = form.githubDownloadScript.val.trim() !== '';
            return span({class: "text-[11px] text-gray-500"}, scripted
                ? "Output file the script leaves in the download dir. Defaults to the only file it produces."
                : "Release asset to download. Defaults to the release's only asset.");
        },
    );
}

function activeAssetCheck(form) {
    const c = form.assetCheck.val;
    const repo = form.githubRepo.val.trim();
    const asset = form.githubAsset.val.trim();
    // A custom download script means the asset isn't a release asset, so the
    // GitHub asset check doesn't apply.
    if (!asset || form.githubDownloadScript.val.trim() || c.repo !== repo || c.asset !== asset) {
        return {status: 'idle', message: ''};
    }
    return c;
}

async function validateAsset(form) {
    const repo = form.githubRepo.val.trim();
    const asset = form.githubAsset.val.trim();
    if (form.githubDownloadScript.val.trim()) {
        form.assetCheck.val = {status: 'idle', message: '', repo: '', asset: ''};
        return;
    }
    if (!asset) {
        form.assetCheck.val = {status: 'idle', message: '', repo, asset: ''};
        return;
    }
    if (!repo) {
        form.assetCheck.val = {status: 'error', message: 'Set the repository first.', repo: '', asset};
        return;
    }
    const c = form.assetCheck.val;
    // Don't re-check an asset/repo pair we already have a verdict for.
    if (c.repo === repo && c.asset === asset && (c.status === 'ok' || c.status === 'error')) {
        return;
    }
    form.assetCheck.val = {status: 'checking', message: 'Checking asset…', repo, asset};
    try {
        const res = await capi.postV1GithubAssetValidate({repo, asset});
        form.assetCheck.val = {
            status: res.ok ? 'ok' : 'error',
            message: res.message || (res.ok ? 'Asset found.' : 'Asset not found.'),
            repo,
            asset,
        };
    } catch (e) {
        form.assetCheck.val = {status: 'error', message: e.message || 'Validation failed.', repo, asset};
    }
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
        envSummary(form),
    );
}

// envSummary shows the configured env-var count and a link that opens the
// editor pane (rendered at the overlay level via envVarsPane).
function envSummary(form) {
    return div(
        {class: "flex items-center justify-between gap-3 border-t border-gray-700 pt-3"},
        span({class: "text-xs text-gray-400"}, () => {
            const n = envVarCount(form.envVars.val);
            return n === 0 ? "No environment variables" : `${n} environment variable${n === 1 ? '' : 's'}`;
        }),
        button({
            type: "button",
            class: "text-xs text-blue-400 hover:text-blue-300 cursor-pointer",
            onclick: () => { form.envPaneOpen.val = !form.envPaneOpen.val; },
        }, () => form.envPaneOpen.val ? "Close" : "View / edit"),
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
                class: "flex-1 min-h-0 w-full resize-none rounded-lg bg-gray-800 text-gray-100 border border-gray-600 px-3 py-2 font-mono text-xs leading-relaxed focus:outline-none focus:ring-1 focus:ring-brand",
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
    const repoState = sourceType === SOURCE_GITHUB ? form.githubRepo : form.nixRepo;
    return label(
        {class: "flex-1 flex flex-col gap-1 text-xs text-gray-400"},
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

// selectField renders a label-above dropdown, consistent with the text fields.
// widthClass constrains the select so source/runner type sit left-aligned
// rather than stretching the full row.
function selectField(text, state, options, widthClass = "w-56") {
    return field(text, select({
        class: `${widthClass} px-3 py-2 rounded-lg bg-gray-800 text-gray-100 border border-gray-600 focus:outline-none focus:ring-1 focus:ring-brand`,
        onchange: e => { state.val = e.target.value; },
    },
        ...options.map(opt => option({value: opt.value, selected: state.rawVal === opt.value}, opt.label)),
    ));
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
        } else if (typeof val === 'string' && val.includes('\n')) {
            // Multi-line strings are emitted as a literal block scalar so
            // newlines and shell syntax survive the round-trip intact.
            lines.push(`${pad}${key}: |`);
            const blockPad = '  '.repeat(indent + 1);
            for (const line of val.replace(/\n+$/, '').split('\n')) {
                lines.push(line === '' ? '' : `${blockPad}${line}`);
            }
        } else {
            lines.push(`${pad}${key}: ${String(val)}`);
        }
    }
    return lines.join('\n');
}
