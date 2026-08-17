import van from "vanjs-core";
import {collapsibleSection, emptyDeploymentForm, envVarsPane} from "/src/components/deploymentForm.js";
import {mockConfigRefs, mockSecretRefs, mockSpaces, scenarioEnvVars} from "./mockData.js";

let nextEnvID = 1;
const newEnvRow = spec => ({key: '', type: 'value', value: '', secretId: 0, configId: 0, ...spec, id: nextEnvID++});

const {button, div, h1, header, label, main, p, pre, span} = van.tags;

const scenario = van.state('typical');
const paneHost = div({class: 'contents'});

const savedEnvVars = form => Object.fromEntries((form.envVars.val || [])
    .map(v => {
        const key = (v.key || '').trim();
        if (!key) return null;
        if (v.type === 'secret') return Number(v.secretId || 0) ? [key, {secretVersionId: Number(v.secretId)}] : null;
        if (v.type === 'config') return Number(v.configId || 0) ? [key, {configVersionId: Number(v.configId)}] : null;
        if (v.type === 'address' || v.type === 'asset') return null;
        return [key, {value: v.value || ''}];
    })
    .filter(Boolean));

function buildPane() {
    const form = emptyDeploymentForm();
    form.envVars.val = scenarioEnvVars[scenario.val]().map(spec => newEnvRow(spec));
    form.envPaneOpen.val = true;

    const summary = () => {
        const n = (form.envVars.val || []).filter(v => v && v.key && v.key.trim()).length;
        return n === 0 ? 'No environment variables' : `${n} environment variable${n === 1 ? '' : 's'}`;
    };

    return div(
        {class: 'flex min-h-0 min-w-0 flex-1'},
        div(
            {class: 'app-scroll flex min-h-0 flex-1 flex-col gap-[1.125rem] overflow-auto px-3 py-3.5'},
            collapsibleSection('Runtime', van.state(true), div(
                {class: 'flex flex-col gap-2'},
                div(
                    {class: 'flex items-center justify-between gap-3'},
                    span({class: 'text-xs text-gray-400'}, summary),
                    button({
                        type: 'button',
                        class: 'text-xs text-blue-400 hover:text-blue-300 cursor-pointer',
                        onclick: () => { form.envPaneOpen.val = !form.envPaneOpen.val; },
                    }, () => form.envPaneOpen.val ? 'Close' : 'View / edit'),
                ),
                p({class: 'text-[11px] text-gray-600'},
                    'The rest of the deployment form is omitted. Only the env-vars summary row and the proposed pane are wired up.'),
            )),
            div(
                {class: 'rounded-sm border border-gray-800 bg-gray-950/60 p-3'},
                p({class: 'mb-2 text-[10px] font-medium uppercase tracking-wide text-gray-500'}, 'Saved env vars'),
                () => pre({class: 'overflow-auto text-[11px] leading-relaxed text-gray-400'},
                    JSON.stringify(savedEnvVars(form), null, 2)),
            ),
        ),
        envVarsPane(form, {
            secretRefs: mockSecretRefs,
            configRefs: mockConfigRefs,
            spaces: mockSpaces,
        }),
    );
}

const renderPane = () => paneHost.replaceChildren(buildPane());

const controls = div(
    {class: 'flex flex-wrap items-end gap-4'},
    label(
        {class: 'flex flex-col gap-1 text-xs text-gray-400'},
        span('Env vars'),
        div(
            {class: 'inline-flex rounded-md border border-gray-800 bg-gray-900 p-0.5'},
            ...[['typical', 'Typical'], ['many', 'Many'], ['empty', 'Empty']].map(([value, text]) => button({
                type: 'button',
                class: () => `rounded px-3 py-1.5 text-xs transition-colors cursor-pointer ${scenario.val === value
                    ? 'bg-gray-700 text-white'
                    : 'text-gray-400 hover:text-gray-200'}`,
                'aria-pressed': () => String(scenario.val === value),
                onclick: () => {
                    if (scenario.val === value) return;
                    scenario.val = value;
                    renderPane();
                },
            }, text)),
        ),
    ),
    button({
        type: 'button',
        class: 'btn-secondary h-9 py-1.5 text-sm',
        onclick: renderPane,
    }, 'Reset'),
);

van.add(document.body,
    div(
        {class: 'flex h-full min-h-0 flex-col'},
        header(
            {class: 'shrink-0 border-b border-gray-800 bg-gray-950/85 px-4 py-3 backdrop-blur md:px-6'},
            div(
                {class: 'mx-auto flex max-w-[1900px] flex-col justify-between gap-3 xl:flex-row xl:items-end'},
                div(
                    h1({class: 'text-lg font-semibold text-white'}, 'Environment variables pane fixture'),
                    p({class: 'mt-1 text-xs text-gray-500'},
                        "Proposal: 'Group by prefix' and 'Boolean toggles' header switches, both off by default."),
                ),
                controls,
            ),
        ),
        main(
            {class: 'mx-auto flex min-h-0 w-full max-w-[1900px] flex-1 flex-col overflow-auto p-4 md:p-6'},
            div({class: 'card flex min-h-[36rem] flex-1 flex-col overflow-hidden !p-0'}, paneHost),
        ),
    ),
);

renderPane();
