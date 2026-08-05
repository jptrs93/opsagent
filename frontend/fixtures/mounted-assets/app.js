import van from "vanjs-core";
import {
    assetMountEditorOverlay,
    assetMountsPane,
    collapsibleSection,
    emptyDeploymentForm,
} from "/src/components/deploymentForm.js";
import {assetPreviewOverlay} from "/src/components/assetPreviewOverlay.js";
import {mockAssetContent, mockAssetMounts, mockAssets} from "./mockData.js";

const {button, div, h1, header, label, main, p, pre, section, span} = van.tags;

const scenario = van.state('typical');
const assets = van.state([...mockAssets]);
const events = van.state([]);
const paneHost = div({class: 'contents'});

let nextAssetID = 900;

const wait = (milliseconds = 200) => new Promise(resolve => setTimeout(resolve, milliseconds));

const record = (kind, payload) => {
    events.val = [{time: new Date(), kind, payload}, ...events.val].slice(0, 30);
};

const latestVersionForKey = key => (assets.val || [])
    .filter(asset => asset.key === key)
    .reduce((latest, asset) => Math.max(latest, Number(asset.version || 0)), 0);

const loadAsset = async ({key, version}) => {
    record('load-asset', {key, version});
    await wait(150);
    const asset = mockAssetContent.get(`${key}@${version}`);
    if (!asset) throw new Error(`Fixture has no content for ${key} v${version}`);
    return asset;
};

// Every save mints a new id and bumps the version for that key, which is what
// makes the "select the new version afterwards" behaviour observable.
const saveAsset = async ({key, format, blob, spaceId}) => {
    record('save-asset', {key, format, spaceId, bytes: blob?.length || 0});
    await wait(300);
    const saved = {
        id: nextAssetID++,
        key,
        version: latestVersionForKey(key) + 1,
        format: format || 'text',
        spaceId: Number(spaceId || 1),
        createdAt: new Date(),
        blob,
        location: '',
        sizeBytes: blob?.length || 0,
    };
    mockAssetContent.set(`${saved.key}@${saved.version}`, saved);
    const {blob: _blob, location: _location, sizeBytes: _sizeBytes, ...meta} = saved;
    assets.val = [...assets.val, meta];
    return saved;
};

const scenarioMounts = {
    typical: () => mockAssetMounts.map(mount => ({...mount})),
    many: () => [
        ...mockAssetMounts.map(mount => ({...mount})),
        ...Array.from({length: 6}, (_, index) => ({
            id: 100 + index,
            assetId: mockAssets[index % mockAssets.length].id,
            path: `/etc/api/conf.d/${String(index + 1).padStart(2, '0')}-overlay.conf`,
            executable: false,
            originalAssetId: mockAssets[index % mockAssets.length].id,
            originalPath: `/etc/api/conf.d/${String(index + 1).padStart(2, '0')}-overlay.conf`,
            originalExecutable: false,
        })),
    ],
    empty: () => [],
};

function buildPane() {
    const form = emptyDeploymentForm();
    form.assetMounts.val = scenarioMounts[scenario.val]();
    form.assetMountsPaneOpen.val = true;

    const previewAsset = van.state(null);
    const editAssetTarget = van.state(null);

    const summary = () => {
        const valid = (form.assetMounts.val || []).filter(m => m && m.assetId && m.path);
        return valid.length === 0 ? 'No mounted assets' : `${valid.length} mounted asset${valid.length === 1 ? '' : 's'}`;
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
                        onclick: () => { form.assetMountsPaneOpen.val = !form.assetMountsPaneOpen.val; },
                    }, () => form.assetMountsPaneOpen.val ? 'Close' : 'Click to manage'),
                ),
                p({class: 'text-[11px] text-gray-600'},
                    'The rest of the deployment form is omitted. Only the mounted-assets summary row and its pane are wired up.'),
            )),
            div(
                {class: 'rounded-sm border border-gray-800 bg-gray-950/60 p-3'},
                p({class: 'mb-2 text-[10px] font-medium uppercase tracking-wide text-gray-500'}, 'Form state'),
                () => pre({class: 'overflow-auto text-[11px] leading-relaxed text-gray-400'},
                    JSON.stringify((form.assetMounts.val || []).map(m => ({
                        assetId: m.assetId,
                        path: m.path,
                        mode: m.executable ? 'read+exec' : 'read-only',
                    })), null, 2)),
            ),
        ),
        assetMountsPane(form, {
            assets,
            enableAssetEditor: true,
            previewAsset: asset => { previewAsset.val = asset; },
            editAsset: target => { editAssetTarget.val = target; },
        }),
        () => editAssetTarget.val
            ? assetMountEditorOverlay(form, editAssetTarget.val, {
                assets,
                loadAsset,
                saveAsset,
                onClose: () => { editAssetTarget.val = null; },
            })
            : '',
        () => previewAsset.val
            ? assetPreviewOverlay(previewAsset.val, loadAsset, () => { previewAsset.val = null; })
            : '',
    );
}

const renderPane = () => paneHost.replaceChildren(buildPane());

const controls = div(
    {class: 'flex flex-wrap items-end gap-4'},
    label(
        {class: 'flex flex-col gap-1 text-xs text-gray-400'},
        span('Mounts'),
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
    button({
        type: 'button',
        class: 'h-9 rounded-md px-3 text-xs text-gray-400 hover:bg-gray-900 hover:text-gray-200',
        onclick: () => {
            assets.val = [...mockAssets];
            events.val = [];
            nextAssetID = 900;
            renderPane();
        },
    }, 'Reset asset catalog'),
);

const eventLog = section(
    {class: 'flex min-h-48 flex-col overflow-hidden rounded-xl border border-gray-800 bg-gray-950/80 lg:w-[22rem] lg:min-w-[22rem]'},
    div(
        {class: 'border-b border-gray-800 px-4 py-3'},
        p({class: 'text-xs font-semibold text-gray-300'}, 'Mock requests'),
        p({class: 'mt-1 text-xs text-gray-600'}, 'Newest first'),
    ),
    div(
        {class: 'app-scroll min-h-0 flex-1 overflow-auto p-3'},
        () => events.val.length
            ? div({class: 'flex flex-col gap-2'}, ...events.val.map(event => div(
                {class: 'rounded-lg border border-gray-800 bg-gray-900/70 p-2.5'},
                div(
                    {class: 'mb-1 flex items-center justify-between gap-3'},
                    span({class: 'text-xs font-medium text-blue-300'}, event.kind),
                    span({class: 'text-[10px] text-gray-600'}, event.time.toLocaleTimeString()),
                ),
                pre({class: 'overflow-auto whitespace-pre-wrap break-all text-[11px] leading-relaxed text-gray-400'},
                    JSON.stringify(event.payload, null, 2)),
            )))
            : p({class: 'p-2 text-xs text-gray-600'}, 'Edit or preview an asset to see requests.'),
    ),
);

const assetCatalog = section(
    {class: 'rounded-xl border border-gray-800 bg-gray-950/60 px-4 py-3'},
    p({class: 'text-[10px] font-medium uppercase tracking-wide text-gray-500'}, 'Asset catalog'),
    () => div({class: 'mt-2 flex flex-wrap gap-x-4 gap-y-1'}, ...assets.val.map(asset => span(
        {class: 'font-mono text-[11px] text-asset'},
        `${asset.key} v${asset.version}`,
    ))),
);

van.add(document.body,
    div(
        {class: 'flex h-full min-h-0 flex-col'},
        header(
            {class: 'shrink-0 border-b border-gray-800 bg-gray-950/85 px-4 py-3 backdrop-blur md:px-6'},
            div(
                {class: 'mx-auto flex max-w-[1900px] flex-col justify-between gap-3 xl:flex-row xl:items-end'},
                div(
                    h1({class: 'text-lg font-semibold text-white'}, 'Mounted assets sidebar fixture'),
                    p({class: 'mt-1 text-xs text-gray-500'},
                        'The production mounted-assets pane with an in-memory asset store that has real content and versions.'),
                ),
                controls,
            ),
        ),
        main(
            {class: 'mx-auto flex min-h-0 w-full max-w-[1900px] flex-1 flex-col gap-4 overflow-auto p-4 lg:flex-row lg:overflow-hidden md:p-6'},
            div(
                {class: 'flex min-h-[36rem] min-w-0 flex-1 flex-col gap-4 lg:min-h-0'},
                div({class: 'card flex min-h-0 flex-1 flex-col overflow-hidden !p-0'}, paneHost),
                assetCatalog,
            ),
            eventLog,
        ),
    ),
);

renderPane();
