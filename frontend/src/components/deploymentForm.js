import van from "vanjs-core";

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
        {class: "flex flex-col gap-3"},
        sectionCardWithAside(
            "Deployment",
            identityLocked ? span({class: "text-xs text-orange-300"}, "Deployment identity is fixed after creation.") : null,
            div(
                {class: "grid grid-cols-1 md:grid-cols-3 gap-3"},
                field("Name", input({
                    type: "text",
                    value: form.name.val,
                    disabled: identityLocked,
                    class: () => textInputClass(nameValid(form), identityLocked, nameValid(form)),
                    placeholder: "coflip_server",
                    oninput: e => { form.name.val = e.target.value; },
                })),
                field("Environment (optional)", div(
                    input({
                        type: "text",
                        list: environmentDatalistID,
                        value: form.environment.val,
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
        sectionCardWithAside(
            "Binary Source",
            inlineSelect("Source type", form.sourceType, [
                {value: SOURCE_GITHUB, label: "Github release"},
                {value: SOURCE_NIX, label: "Build NIX store"},
            ]),
            div(
                {class: "flex flex-col gap-3"},
                () => form.sourceType.val === SOURCE_GITHUB
                    ? div(
                        {class: "grid grid-cols-1 md:grid-cols-2 gap-3"},
                        field("Repository", textInput(form.githubRepo, "github.com/org/repo")),
                        field("Asset", textInput(form.githubAsset, "server-linux-amd64")),
                    )
                    : div(
                        {class: "grid grid-cols-1 md:grid-cols-3 gap-3"},
                        field("Repository", textInput(form.nixRepo, "github.com/org/repo")),
                        field("Flake", textInput(form.nixFlake, "nix/server/flake.nix")),
                        field("Output executable", textInput(form.nixOutputExecutable, "server")),
                    ),
            ),
        ),
        sectionCardWithAside(
            "Execution",
            inlineSelect("Runner type", form.runnerType, [
                {value: RUNNER_OS, label: "OpsAgent process"},
                {value: RUNNER_SYSTEMD, label: "systemd service"},
            ]),
            div(
                {class: "flex flex-col gap-3"},
                () => form.runnerType.val === RUNNER_SYSTEMD
                    ? div(
                        {class: "grid grid-cols-1 md:grid-cols-2 gap-3"},
                        field("Unit name", textInput(form.systemdName, "coflip.service")),
                        field("Binary path", textInput(form.systemdBinPath, "/var/lib/coflip/bin/server")),
                    )
                    : div(
                        {class: "flex flex-col gap-3"},
                        div(
                            {class: "grid grid-cols-1 md:grid-cols-3 gap-3"},
                            field("Working directory", textInput(form.osWorkingDir, "/var/lib/coflip")),
                            field("Run as", textInput(form.osRunAs, "coflip")),
                            field("Strategy", select({
                                class: selectClass(),
                                onchange: e => { form.osStrategy.val = e.target.value; },
                            },
                                option({value: '', selected: form.osStrategy.val === ''}, "Terminate previous"),
                                option({value: 'leavePrevious', selected: form.osStrategy.val === 'leavePrevious'}, "Leave previous running"),
                            )),
                        ),
                        environmentVarsEditor(form),
                    ),
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
    };
}

function sectionCard(title, ...children) {
    return sectionCardWithAside(title, null, ...children);
}

function sectionCardWithAside(title, aside, ...children) {
    return div(
        {class: "rounded-lg border border-gray-700 bg-gray-900/70 p-4"},
        div(
            {class: "flex items-center justify-between gap-3 mb-3"},
            h3({class: "text-sm font-semibold text-gray-200"}, title),
            aside,
        ),
        ...children,
    );
}

function field(text, control) {
    return label(
        {class: "flex flex-col gap-1 text-xs text-gray-400"},
        span(text),
        control,
    );
}

function inlineSelect(text, state, options) {
    return label(
        {class: "flex items-center justify-end gap-2 text-xs text-gray-400"},
        span({class: "whitespace-nowrap"}, text),
        select({
            class: "h-8 w-48 rounded-lg bg-gray-800 text-gray-100 border border-gray-600 px-2 focus:outline-none focus:ring-1 focus:ring-brand",
            onchange: e => { state.val = e.target.value; },
        },
            ...options.map(opt => option({value: opt.value, selected: state.val === opt.value}, opt.label)),
        ),
    );
}

function textInput(state, placeholder = '') {
    return input({
        type: "text",
        value: state.val,
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
    const extraCurrent = form.machine.val && !opts.machineOptionValues.includes(form.machine.val)
        ? [option({value: form.machine.val, selected: true}, form.machine.val)]
        : [];
    return select({
        class: selectClass(),
        disabled: opts.identityLocked || !opts.machineOptionsLoaded || opts.machineOptionValues.length === 0,
        onchange: e => { form.machine.val = e.target.value; },
    },
        option({value: '', disabled: true, selected: !form.machine.val}, machinePlaceholder(opts.machineOptionsLoaded, opts.machineOptionValues)),
        ...opts.machineOptionValues.map(name => option({value: name, selected: name === form.machine.val}, name)),
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
                h3({class: "text-xs font-semibold text-gray-300"}, "Environment"),
                p({class: "text-xs text-gray-500"}, "Set on the process runner. Ignored for systemd deployments."),
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
