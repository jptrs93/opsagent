import van from "vanjs-core";
import {deploymentEditorWidget} from "/src/components/deploymentEditorWidget.js";
import {
    fixturePresets,
    imageTags,
    mockAssets,
    mockConfigRefs,
    mockDeployments,
    mockNodes,
    mockSecretRefs,
    mockSpaces,
    nixBranches,
    nixCommits,
} from "./mockData.js";

const {div, header, h1, p, label, select, option, button, input, span, pre, main} = van.tags;

const selectedPreset = van.state('create');
const failSourceRequests = van.state(false);
const events = van.state([]);
const assets = van.state([...mockAssets]);
const eventPanelOpen = van.state(false);
const editorHost = div({class: 'min-w-0 min-h-0 flex items-center justify-center'});
let nextDeploymentID = 900;
let nextAssetID = 500;

const wait = (milliseconds = 250) => new Promise(resolve => setTimeout(resolve, milliseconds));

const record = (kind, payload) => {
    events.val = [{time: new Date(), kind, payload}, ...events.val].slice(0, 30);
};

const validateSource = async request => {
    record('validate-source', request);
    await wait();
    if (failSourceRequests.val) throw new Error('Fixture source validation failure');
    if (request.containerImage) {
        return {
            containerImage: {
                image: {checked: true, ok: true, message: 'Mock registry is accessible.'},
                tags: imageTags,
            },
        };
    }

    const source = request.nixDockerBuild || {};
    const branch = source.selectedBranch || 'main';
    return {
        nixDockerBuild: {
            checkedRepoUrl: source.repoUrl,
            gitRepository: {checked: true, ok: true, message: 'Mock repository is accessible.'},
            checkedBranch: branch,
            branchCheck: {checked: Boolean(source.selectedBranch), ok: true, message: 'Branch exists.'},
            checkedCommit: source.selectedCommit,
            commitCheck: {checked: Boolean(source.selectedCommit?.id), ok: true, message: 'Commit exists.'},
            checkedFlakePath: source.selectedFlakePath,
            nixFlakeFile: {checked: true, ok: true, message: 'Mock flake path exists.'},
            availableBranches: {loaded: true, branches: nixBranches},
            availableCommits: {loaded: true, branch, commits: nixCommits[branch] || []},
        },
    };
};

const actions = {
    loadNodes: async () => {
        record('load-nodes', {});
        await wait(150);
        return {machines: mockNodes};
    },
    validateSource,
    loadDeploymentVersions: async request => {
        record('load-deployment-versions', request);
        await wait();
        return {githubRelease: {releases: imageTags}};
    },
    saveAsset: async request => {
        record('save-asset', request);
        await wait();
        const asset = {id: nextAssetID++, key: request.key, spaceId: request.spaceId, version: 1, format: request.format};
        assets.val = [...assets.val, asset];
        return asset;
    },
    createDeployment: async request => {
        record('create-deployment', request);
        await wait(400);
        return {id: nextDeploymentID++, ...request};
    },
    updateDeployment: async request => {
        record(request.stop ? 'stop-deployment' : (request.targetVersion ? 'deploy-version' : 'update-deployment'), request);
        await wait(400);
        return {running: !request.stop, version: request.targetVersion || ''};
    },
};

function renderEditor() {
    const preset = fixturePresets[selectedPreset.val];
    const editor = deploymentEditorWidget({
        ...preset,
        catalogs: {
            spaces: mockSpaces,
            nodes: mockNodes,
            nodesLoaded: true,
            deployments: mockDeployments,
            assets,
            secretRefs: mockSecretRefs,
            configRefs: mockConfigRefs,
        },
        actions,
        maxHeight: 'calc(100vh - 10rem)',
        onCancel: () => record('cancel', {preset: selectedPreset.val}),
        onSuccess: event => record(`${event.kind}-success`, event.payload),
    });
    editorHost.replaceChildren(editor);
}

const controls = div(
    {class: 'flex flex-wrap items-end gap-3'},
    label(
        {class: 'flex flex-col gap-1 text-xs text-gray-400'},
        span('Scenario'),
        select({
            class: 'h-9 min-w-56 rounded-md border border-gray-700 bg-gray-900 px-3 text-sm text-gray-100',
            onchange: event => {
                selectedPreset.val = event.target.value;
                renderEditor();
            },
        }, ...Object.entries(fixturePresets).map(([key, preset]) => option({value: key}, preset.label))),
    ),
    button({class: 'btn-secondary h-9 py-1.5 text-sm', onclick: renderEditor, type: 'button'}, 'Reset editor'),
    label(
        {class: 'flex h-9 items-center gap-2 rounded-md border border-gray-800 bg-gray-900 px-3 text-xs text-gray-300'},
        input({
            type: 'checkbox',
            checked: failSourceRequests,
            onchange: event => { failSourceRequests.val = event.target.checked; },
        }),
        span('Fail source requests'),
    ),
    button({
        class: 'h-9 rounded-md border border-gray-800 bg-gray-900 px-3 text-xs text-gray-300 hover:border-gray-700 hover:text-white',
        onclick: () => { eventPanelOpen.val = !eventPanelOpen.val; },
        type: 'button',
        'aria-expanded': () => String(eventPanelOpen.val),
    }, () => `${eventPanelOpen.val ? 'Hide' : 'Show'} mock requests${events.val.length ? ` (${events.val.length})` : ''}`),
    button({
        class: 'h-9 rounded-md px-3 text-xs text-gray-400 hover:bg-gray-900 hover:text-gray-200',
        onclick: () => { events.val = []; },
        type: 'button',
    }, 'Clear events'),
);

const eventPanel = div(
    {class: () => eventPanelOpen.val
        ? 'flex min-h-48 flex-col overflow-hidden rounded-xl border border-gray-800 bg-gray-950/80 lg:w-[26rem] lg:min-w-[26rem]'
        : 'hidden'},
    div(
        {class: 'flex items-start justify-between gap-3 border-b border-gray-800 px-4 py-3'},
        div(
            p({class: 'text-xs font-semibold text-gray-300'}, 'Mock requests'),
            p({class: 'mt-1 text-xs text-gray-600'}, 'Newest request first'),
        ),
        button({
            class: 'text-xs text-gray-500 hover:text-gray-200',
            onclick: () => { eventPanelOpen.val = false; },
            type: 'button',
        }, 'Hide'),
    ),
    div(
        {class: 'min-h-0 flex-1 overflow-auto p-3'},
        () => events.val.length
            ? div({class: 'flex flex-col gap-3'}, ...events.val.map(event => div(
                {class: 'rounded-lg border border-gray-800 bg-gray-900/70 p-3'},
                div(
                    {class: 'mb-2 flex items-center justify-between gap-3'},
                    span({class: 'text-xs font-medium text-blue-300'}, event.kind),
                    span({class: 'text-[10px] text-gray-600'}, event.time.toLocaleTimeString()),
                ),
                pre({class: 'overflow-auto whitespace-pre-wrap break-all text-[11px] leading-relaxed text-gray-400'}, stringify(event.payload)),
            )))
            : p({class: 'p-3 text-xs text-gray-600'}, 'Interact with the editor to inspect requests.'),
    ),
);

van.add(document.body,
    div(
        {class: 'flex h-full min-h-0 flex-col'},
        header(
            {class: 'shrink-0 border-b border-gray-800 bg-gray-950/85 px-4 py-3 backdrop-blur md:px-6'},
            div(
                {class: 'mx-auto flex max-w-[1900px] flex-col justify-between gap-3 xl:flex-row xl:items-end'},
                div(
                    h1({class: 'text-lg font-semibold text-white'}, 'Deployment editor fixture'),
                    p({class: 'mt-1 text-xs text-gray-500'}, 'The production widget with in-memory catalogs and API actions.'),
                ),
                controls,
            ),
        ),
        main(
            {class: 'mx-auto flex min-h-0 w-full max-w-[1900px] flex-1 flex-col gap-4 overflow-auto p-4 lg:flex-row lg:overflow-hidden md:p-6'},
            div({class: 'min-h-[36rem] min-w-0 flex-1 lg:min-h-0'}, editorHost),
            eventPanel,
        ),
    ),
);

renderEditor();

function stringify(value) {
    return JSON.stringify(value, (key, item) => {
        if (item instanceof Uint8Array) return `[Uint8Array ${item.byteLength} bytes]`;
        if (item instanceof Date) return item.toISOString();
        return item;
    }, 2);
}
