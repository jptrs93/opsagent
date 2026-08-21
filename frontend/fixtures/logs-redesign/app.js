import van from "vanjs-core";
import {
    caretRightIcon,
    checkIcon,
    chevronDownIcon,
    columnsIcon,
    copyIcon,
    searchIcon,
    xIcon,
} from "/src/lib/icons.js";
import {FIELD_KEYS, LEVELS, recordField, recordJson, store} from "./mockData.js";

const {button, div, h1, header, input, main, option, p, pre, select, span} = van.tags;
const {svg: svgEl, rect, line: svgLine} = van.tags("http://www.w3.org/2000/svg");

// ---------------------------------------------------------------------------
// Proposed page. Everything below until the fixture chrome section is what
// would move into src/pages/logs.js if the design is accepted.
// ---------------------------------------------------------------------------

const MIN = 60_000;
const HOUR = 3_600_000;
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const pad2 = (n) => String(n).padStart(2, '0');

// Histogram fills validated for CVD separation and contrast on this surface
// (dataviz six-check validator); row/legend text stays on text tokens.
const LEVEL_META = {
    ERROR: {fill: '#c42121', text: 'text-red-400'},
    WARN: {fill: '#c67b04', text: 'text-amber-400'},
    INFO: {fill: '#3b82f6', text: 'text-blue-400'},
    DEBUG: {fill: '#0e9488', text: 'text-teal-500'},
};

const COLUMN_DEFS = {
    time: {label: 'time', px: 152},
    level: {label: 'level', px: 58},
    msg: {label: 'message', flex: true, minPx: 320, mono: true},
    service: {label: 'service', px: 90},
    host: {label: 'host', px: 74},
    version: {label: 'version', px: 76},
    logger: {label: 'logger', px: 100},
    method: {label: 'method', px: 68},
    path: {label: 'path', px: 170, mono: true},
    status: {label: 'status', px: 58, num: true},
    duration_ms: {label: 'duration', px: 78, num: true},
    trace_id: {label: 'trace', px: 136, mono: true},
    job: {label: 'job', px: 100},
    user: {label: 'user', px: 84},
    err: {label: 'err', px: 130, mono: true},
};

const PRESETS = [
    {key: '15m', label: 'Last 15 minutes', ms: 15 * MIN},
    {key: '1h', label: 'Last hour', ms: HOUR},
    {key: '6h', label: 'Last 6 hours', ms: 6 * HOUR},
    {key: '24h', label: 'Last 24 hours', ms: 24 * HOUR},
    {key: '48h', label: 'Last 48 hours', ms: 48 * HOUR},
];

// Deployment scope stands in for the real page's space/deployment selectors;
// in the fixture each mock service is one deployment.
const SCOPES = [['', 'All deployments'], ['api', 'prod / api'], ['worker', 'prod / worker'],
    ['ingress', 'prod / ingress'], ['netproxy', 'prod / netproxy'],
    ['postgres', 'prod / postgres'], ['scheduler', 'prod / scheduler']];

const ROW_H = 22;
const ROW_H_WRAP = 54;
const DETAIL_H = 320;
const HEADER_H = 25;
const OVERSCAN = 12;

function fmtRowTime(ts) {
    const d = new Date(ts);
    return `${MONTHS[d.getMonth()]} ${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}.${String(d.getMilliseconds()).padStart(3, '0')}`;
}

function fmtClock(ts) {
    const d = new Date(ts);
    return `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}.${String(d.getMilliseconds()).padStart(3, '0')}`;
}

function fmtShort(ts) {
    const d = new Date(ts);
    return `${MONTHS[d.getMonth()]} ${d.getDate()} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

function fmtNum(n) {
    if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
    if (n >= 1e4) return `${Math.round(n / 1e3)}k`;
    return n.toLocaleString();
}

function toLocalInputValue(ts) {
    const d = new Date(ts);
    return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}T${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

// --- query language --------------------------------------------------------
// One string is the whole filter state: bare words and "quoted phrases" match
// the message, key:value matches a field (key:* = field exists), a leading -
// negates. Sidebar and row actions edit this string rather than owning
// side-channel filter state.

function parseQuery(text) {
    const tokens = [];
    const re = /(-)?(?:([\w.]+):)?(?:"([^"]*)"|([^\s"]+))/g;
    let m;
    while ((m = re.exec(text))) {
        const neg = Boolean(m[1]);
        const key = m[2] ? m[2].toLowerCase() : '';
        const value = (m[3] !== undefined ? m[3] : (m[4] || '')).toLowerCase();
        if (!value && !key) continue;
        tokens.push(key ? {type: 'pair', key, value, neg} : {type: 'text', value, neg});
    }
    return tokens;
}

const quoteValue = (v) => /\s/.test(v) ? `"${v}"` : v;
const tokenText = (t) => `${t.neg ? '-' : ''}${t.type === 'pair' ? `${t.key}:${quoteValue(t.value)}` : quoteValue(t.value)}`;

function logsPageProposal() {
    // --- state -------------------------------------------------------------
    const queryText = van.state('');
    const scope = van.state('');
    const range = van.state({kind: 'preset', key: '48h'});
    const levelOn = van.state({ERROR: true, WARN: true, INFO: true, DEBUG: true});
    const columns = van.state(['time', 'msg', 'level']);
    const wrap = van.state(false);
    const result = van.state(null);
    const searching = van.state(false);
    const progress = van.state(0);
    const expanded = van.state(null);       // {pos, idx} — pos is display position (newest first)
    const detailTab = van.state('fields');
    const context = van.state(null);        // {anchorIdx}
    const sidebarOpen = van.state(true);
    const fieldOpen = van.state('');
    const timeOpen = van.state(false);
    const jsonCopied = van.state(false);
    let searchGen = 0;
    let scroller;

    const resolveRange = () => {
        const r = range.val;
        if (r.kind === 'custom') return {startTs: r.startTs, endTs: r.endTs};
        const preset = PRESETS.find(pr => pr.key === r.key) || PRESETS[PRESETS.length - 1];
        return {startTs: Math.max(store.startTs, store.endTs - preset.ms), endTs: store.endTs};
    };

    const rangeLabel = () => {
        const r = range.val;
        if (r.kind === 'custom') return `${fmtShort(r.startTs)} – ${fmtShort(r.endTs)}`;
        return (PRESETS.find(pr => pr.key === r.key) || PRESETS[PRESETS.length - 1]).label;
    };

    const runSearch = async () => {
        const gen = ++searchGen;
        const {startTs, endTs} = resolveRange();
        searching.val = true;
        progress.val = 0;
        expanded.val = null;
        const res = await store.runQuery({
            startTs, endTs,
            tokens: parseQuery(queryText.val),
            levels: new Set(LEVELS.filter(l => levelOn.val[l])),
            scope: scope.val || undefined,
            isCancelled: () => gen !== searchGen,
            onProgress: (f) => { if (gen === searchGen) progress.val = f; },
        });
        if (gen !== searchGen || !res) return;
        result.val = res;
        searching.val = false;
        if (scroller) scroller.scrollTop = 0;
    };

    const addFilter = (key, value, neg = false) => {
        const token = `${neg ? '-' : ''}${key}:${quoteValue(String(value).toLowerCase())}`;
        if (queryText.val.split(/\s+/).includes(token)) return;
        queryText.val = `${queryText.val} ${token}`.trim();
        void runSearch();
    };

    const removeToken = (pos) => {
        const tokens = parseQuery(queryText.val);
        tokens.splice(pos, 1);
        queryText.val = tokens.map(tokenText).join(' ');
        void runSearch();
    };

    const toggleColumn = (key) => {
        columns.val = columns.val.includes(key)
            ? columns.val.filter(k => k !== key || k === 'msg')
            : [...columns.val, key];
    };

    const moveColumn = (key, dir) => {
        const cols = [...columns.val];
        const i = cols.indexOf(key);
        const j = i + dir;
        if (i < 0 || j < 0 || j >= cols.length) return;
        [cols[i], cols[j]] = [cols[j], cols[i]];
        columns.val = cols;
    };

    const levelTotals = (res) => {
        const totals = [0, 0, 0, 0];
        if (res) for (let b = 0; b < res.bucketN; b++) for (let l = 0; l < 4; l++) totals[l] += res.counts[b * 4 + l];
        return totals;
    };

    // --- search bar ----------------------------------------------------------

    const queryInput = input({
        "data-testid": "logs2-query-input",
        class: "input search-input-iconed w-full py-1 pr-2 font-mono text-xs bg-gray-900",
        placeholder: 'free text and field filters — e.g. level:error service:api "pool exhausted" -host:node-2',
        value: () => queryText.val,
        oninput: (e) => { queryText.val = e.target.value; },
        onkeydown: (e) => { if (e.key === 'Enter') void runSearch(); },
    });

    const presetRow = (preset) => button({
        type: "button",
        class: "flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs text-gray-200 hover:bg-gray-800 cursor-pointer",
        onclick: () => {
            range.val = {kind: 'preset', key: preset.key};
            timeOpen.val = false;
            void runSearch();
        },
    },
        span({class: "w-4"}, () => range.val.kind === 'preset' && range.val.key === preset.key ? checkIcon({class: "w-3.5 h-3.5 text-brand"}) : ''),
        preset.label);

    const customRange = () => {
        const startS = van.state(toLocalInputValue(resolveRange().startTs));
        const endS = van.state(toLocalInputValue(resolveRange().endTs));
        return div(
            {class: "border-t border-gray-800 p-3 flex flex-col gap-1.5"},
            span({class: "text-[10px] uppercase tracking-wide text-gray-500"}, "custom range"),
            input({class: "input py-1 text-xs", type: "datetime-local", value: startS.val, oninput: (e) => { startS.val = e.target.value; }}),
            input({class: "input py-1 text-xs", type: "datetime-local", value: endS.val, oninput: (e) => { endS.val = e.target.value; }}),
            button({
                type: "button",
                class: "btn-secondary mt-1 py-1 text-xs",
                onclick: () => {
                    const start = new Date(startS.val).getTime();
                    const end = new Date(endS.val).getTime();
                    if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start) return;
                    range.val = {kind: 'custom', startTs: start, endTs: end};
                    timeOpen.val = false;
                    void runSearch();
                },
            }, "Apply"),
        );
    };

    const timePicker = div(
        {class: "relative"},
        button({
            "data-testid": "logs2-time-button",
            type: "button",
            class: "input flex items-center gap-1.5 whitespace-nowrap py-1 text-xs text-gray-200 cursor-pointer hover:bg-gray-700",
            onclick: () => { timeOpen.val = !timeOpen.val; },
        }, () => rangeLabel(), chevronDownIcon({class: "w-3 h-3 text-gray-500"})),
        () => !timeOpen.val ? '' : div(
            div({class: "fixed inset-0 z-20", onclick: () => { timeOpen.val = false; }}),
            div(
                {class: "absolute right-0 top-full z-30 mt-1 w-64 rounded border border-gray-700 bg-gray-900 py-1 shadow-xl"},
                ...PRESETS.map(presetRow),
                customRange(),
            ),
        ),
    );

    const chip = (token, pos) => span(
        {class: `inline-flex items-center gap-1 rounded border px-1.5 py-px font-mono text-[11px] ${token.neg
            ? "border-red-900/60 bg-red-950/40 text-red-300"
            : "border-gray-700 bg-gray-800 text-gray-200"}`},
        token.neg ? span({class: "font-sans text-[9px] uppercase text-red-400"}, "not") : '',
        token.type === 'pair' ? `${token.key}:${token.value}` : `"${token.value}"`,
        button({
            type: "button",
            class: "cursor-pointer text-gray-500 hover:text-gray-200",
            "aria-label": "Remove filter",
            onclick: () => removeToken(pos),
        }, xIcon({size: 10})),
    );

    const searchBar = div(
        {class: "flex-none border-b border-gray-700"},
        div(
            {class: "flex items-center gap-1.5 px-2 py-1.5"},
            select({
                "data-testid": "logs2-scope-select",
                class: "input min-w-40 py-1 text-xs",
                onchange: (e) => { scope.val = e.target.value; void runSearch(); },
            }, ...SCOPES.map(([value, text]) => option({value, selected: () => scope.val === value}, text))),
            div(
                {class: "relative min-w-0 flex-1"},
                span({class: "pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-gray-500"}, searchIcon({class: "w-3.5 h-3.5"})),
                queryInput,
            ),
            timePicker,
            button({
                "data-testid": "logs2-search-button",
                type: "button",
                class: "btn-primary px-3 py-1 text-xs disabled:opacity-50",
                disabled: () => searching.val,
                onclick: () => void runSearch(),
            }, () => searching.val ? 'Searching…' : 'Search'),
        ),
        () => {
            const tokens = parseQuery(queryText.val);
            return tokens.length === 0 ? '' : div(
                {class: "flex flex-wrap items-center gap-1 px-2 pb-1.5"},
                ...tokens.map(chip),
            );
        },
    );

    // --- histogram -----------------------------------------------------------

    const histTooltip = div({class: "pointer-events-none absolute z-30 hidden rounded border border-gray-700 bg-gray-900 px-2 py-1 text-[10px] shadow-xl"});
    const histHover = div({class: "pointer-events-none absolute inset-y-0 hidden bg-white/5"});
    const histBrush = div({class: "pointer-events-none absolute inset-y-0 hidden border-x border-brand/70 bg-brand/15"});
    const histSvgHost = div({class: "absolute inset-0"});
    const histHost = div(
        {"data-testid": "logs2-histogram", class: "relative h-[76px] min-w-0 flex-1 cursor-crosshair"},
        histSvgHost, histHover, histBrush, histTooltip,
    );
    const histMax = span({class: "w-10 flex-none self-start text-right text-[10px] tabular-nums text-gray-600"});
    const histAxis = div({class: "flex justify-between pl-12 pr-1 text-[10px] tabular-nums text-gray-600"});

    let drag = null;

    const renderHistogram = () => {
        const res = result.val;
        const w = histHost.clientWidth;
        if (!res || !w) { histSvgHost.replaceChildren(); histMax.textContent = ''; histAxis.replaceChildren(); return; }
        const h = 76;
        const bucketW = w / res.bucketN;
        let maxTotal = 1;
        for (let b = 0; b < res.bucketN; b++) {
            const total = res.counts[b * 4] + res.counts[b * 4 + 1] + res.counts[b * 4 + 2] + res.counts[b * 4 + 3];
            if (total > maxTotal) maxTotal = total;
        }
        const bars = [];
        // Stack bottom→top DEBUG, INFO, WARN, ERROR so errors ride on top,
        // with a 2px gap between bars and a 1px surface gap between segments
        // tall enough to afford one. Segments keep their exact share of the
        // bucket height — a non-empty bucket gets a 1px presence mark in its
        // dominant level rather than every level being inflated to 1px.
        for (let b = 0; b < res.bucketN; b++) {
            const x = b * bucketW + 1;
            const barW = Math.max(1, bucketW - 2);
            const total = res.counts[b * 4] + res.counts[b * 4 + 1] + res.counts[b * 4 + 2] + res.counts[b * 4 + 3];
            if (!total) continue;
            const totalH = (total / maxTotal) * (h - 4);
            if (totalH < 1) {
                let top = 3;
                for (let l = 0; l < 4; l++) if (res.counts[b * 4 + l] > res.counts[b * 4 + top]) top = l;
                bars.push(rect({x: x.toFixed(1), y: String(h - 1), width: barW.toFixed(1), height: "1", fill: LEVEL_META[LEVELS[top]].fill}));
                continue;
            }
            let y = h;
            let topDrawn = false;
            for (let l = 3; l >= 0; l--) {
                const count = res.counts[b * 4 + l];
                if (!count) continue;
                const segH = (count / total) * totalH;
                y -= segH;
                if (segH < 0.5) continue;
                const isTop = [0, 1, 2].slice(0, l).every(u => !res.counts[b * 4 + u]);
                bars.push(rect({
                    x: x.toFixed(1), y: Math.max(0, y).toFixed(1),
                    width: barW.toFixed(1), height: Math.max(0.5, segH - (!topDrawn || segH < 3 ? 0 : 1)).toFixed(1),
                    rx: isTop && segH >= 3 ? 1.5 : 0,
                    fill: LEVEL_META[LEVELS[l]].fill,
                }));
                topDrawn = true;
            }
        }
        histSvgHost.replaceChildren(svgEl(
            {width: String(w), height: String(h), viewBox: `0 0 ${w} ${h}`},
            svgLine({x1: "0", y1: String(h / 2), x2: String(w), y2: String(h / 2), stroke: "#1f2937", "stroke-width": "1"}),
            ...bars,
        ));
        histMax.textContent = fmtNum(maxTotal);
        const mid = res.startTs + (res.endTs - res.startTs) / 2;
        histAxis.replaceChildren(span(fmtShort(res.startTs)), span(fmtShort(mid)), span(fmtShort(res.endTs)));
    };

    histHost.onpointerdown = (e) => {
        const rectBox = histHost.getBoundingClientRect();
        drag = {x0: e.clientX - rectBox.left, x1: e.clientX - rectBox.left};
        histHost.setPointerCapture(e.pointerId);
    };
    histHost.onpointermove = (e) => {
        const res = result.val;
        const rectBox = histHost.getBoundingClientRect();
        const x = Math.max(0, Math.min(rectBox.width, e.clientX - rectBox.left));
        if (drag) {
            drag.x1 = x;
            const lo = Math.min(drag.x0, drag.x1), hi = Math.max(drag.x0, drag.x1);
            histBrush.style.left = `${lo}px`;
            histBrush.style.width = `${hi - lo}px`;
            histBrush.classList.remove('hidden');
            histTooltip.classList.add('hidden');
            histHover.classList.add('hidden');
            return;
        }
        if (!res) return;
        const bucketW = rectBox.width / res.bucketN;
        const b = Math.max(0, Math.min(res.bucketN - 1, Math.floor(x / bucketW)));
        histHover.style.left = `${b * bucketW}px`;
        histHover.style.width = `${bucketW}px`;
        histHover.classList.remove('hidden');
        const t0 = res.startTs + b * res.bucketMs;
        histTooltip.replaceChildren(
            div({class: "mb-0.5 text-gray-400"}, `${fmtShort(t0)} – ${fmtClock(t0 + res.bucketMs).slice(0, 5)}`),
            ...LEVELS.map((lvl, l) => div(
                {class: "flex items-center gap-1.5"},
                span({class: "h-2 w-2 rounded-[2px]", style: `background:${LEVEL_META[lvl].fill}`}),
                span({class: "w-10 text-gray-300"}, lvl),
                span({class: "tabular-nums text-gray-100"}, res.counts[b * 4 + l].toLocaleString()),
            )),
        );
        histTooltip.classList.remove('hidden');
        const tipX = Math.min(rectBox.width - 130, x + 10);
        histTooltip.style.left = `${Math.max(0, tipX)}px`;
        histTooltip.style.top = `-6px`;
    };
    histHost.onpointerleave = () => { histTooltip.classList.add('hidden'); histHover.classList.add('hidden'); };
    histHost.onpointerup = () => {
        const res = result.val;
        const d = drag;
        drag = null;
        histBrush.classList.add('hidden');
        if (!d || !res || Math.abs(d.x1 - d.x0) < 5) return;
        const w = histHost.clientWidth;
        const t0 = res.startTs + (Math.min(d.x0, d.x1) / w) * (res.endTs - res.startTs);
        const t1 = res.startTs + (Math.max(d.x0, d.x1) / w) * (res.endTs - res.startTs);
        range.val = {kind: 'custom', startTs: Math.floor(t0), endTs: Math.ceil(t1)};
        void runSearch();
    };

    const legendButton = (lvl, l) => button({
        type: "button",
        class: () => `flex cursor-pointer items-center gap-1 rounded px-1 py-px transition-opacity ${levelOn.val[lvl] ? '' : 'opacity-35'}`,
        title: () => levelOn.val[lvl] ? `Hide ${lvl}` : `Show ${lvl}`,
        onclick: () => {
            levelOn.val = {...levelOn.val, [lvl]: !levelOn.val[lvl]};
            void runSearch();
        },
    },
        span({class: "h-2 w-2 rounded-[2px]", style: `background:${LEVEL_META[lvl].fill}`}),
        span({class: "text-[10px] text-gray-400"}, lvl),
        span({class: "text-[10px] tabular-nums text-gray-500"}, () => fmtNum(levelTotals(result.val)[l])),
    );

    const statusLine = () => {
        if (searching.val) return `Scanning ${fmtNum(store.total)} events… ${Math.round(progress.val * 100)}%`;
        const res = result.val;
        if (!res) return 'No search yet.';
        let text = `${res.scanned.toLocaleString()} events scanned in ${res.tookMs} ms · ${res.total.toLocaleString()} match${res.total === 1 ? '' : 'es'}`;
        if (res.capped) text += ` · list shows newest ${res.idxs.length.toLocaleString()}`;
        return text;
    };

    const histogramBand = div(
        {class: "flex-none border-b border-gray-800 bg-gray-950/40 px-2 pb-1 pt-1"},
        div(
            {class: "flex items-center gap-3 pb-1"},
            p({class: "min-w-0 flex-1 truncate text-[11px] text-gray-500", "aria-live": "polite"}, statusLine),
            () => range.val.kind !== 'custom' ? '' : button({
                type: "button",
                class: "cursor-pointer rounded border border-gray-700 px-1.5 py-px text-[10px] text-gray-400 hover:text-gray-200",
                onclick: () => { range.val = {kind: 'preset', key: '48h'}; void runSearch(); },
            }, "clear zoom"),
            div({class: "flex items-center gap-2"}, ...LEVELS.map(legendButton)),
            button({
                type: "button",
                class: () => `cursor-pointer rounded border px-1.5 py-px text-[10px] ${wrap.val
                    ? 'border-brand/60 bg-brand/15 text-blue-300' : 'border-gray-700 text-gray-400 hover:text-gray-200'}`,
                "aria-pressed": () => String(wrap.val),
                onclick: () => { wrap.val = !wrap.val; },
            }, "wrap"),
        ),
        div({class: "flex items-end gap-2"}, histMax, histHost),
        histAxis,
    );

    // --- sidebar: columns + fields ------------------------------------------

    const microHeader = (text) => div({class: "px-2 pb-0.5 pt-2 text-[10px] font-medium uppercase tracking-wide text-gray-500"}, text);

    const columnRow = (key) => div(
        {class: "group flex items-center gap-1 px-2 py-0.5 text-[11px] text-gray-300 hover:bg-gray-800/40"},
        span({class: "min-w-0 flex-1 truncate font-mono"}, COLUMN_DEFS[key]?.label || key),
        div(
            {class: "hidden items-center gap-0.5 group-hover:flex"},
            button({type: "button", class: "cursor-pointer rounded px-1 text-gray-500 hover:text-gray-200", title: "Move left", onclick: () => moveColumn(key, -1)}, "↑"),
            button({type: "button", class: "cursor-pointer rounded px-1 text-gray-500 hover:text-gray-200", title: "Move right", onclick: () => moveColumn(key, 1)}, "↓"),
            key === 'msg' ? '' : button({
                type: "button", class: "cursor-pointer rounded px-0.5 text-gray-500 hover:text-red-400",
                title: "Remove column", onclick: () => toggleColumn(key),
            }, xIcon({size: 11})),
        ),
    );

    const fieldValueRow = (key, entry) => div(
        {class: "group/val px-2 py-0.5"},
        div(
            {class: "flex items-center gap-1 text-[11px]"},
            span({class: "min-w-0 flex-1 truncate font-mono text-gray-300", title: entry.value}, entry.value),
            span({class: "tabular-nums text-gray-500"}, `${Math.round(entry.frac * 100)}%`),
            div(
                {class: "hidden gap-0.5 group-hover/val:flex"},
                button({
                    type: "button", class: "cursor-pointer rounded bg-gray-800 px-1 text-gray-300 hover:text-green-400",
                    title: `Filter for ${key}:${entry.value}`, onclick: () => addFilter(key, entry.value),
                }, "+"),
                button({
                    type: "button", class: "cursor-pointer rounded bg-gray-800 px-1 text-gray-300 hover:text-red-400",
                    title: `Filter out ${key}:${entry.value}`, onclick: () => addFilter(key, entry.value, true),
                }, "−"),
            ),
        ),
        div({class: "mt-px h-[3px] rounded-sm bg-gray-800"},
            div({class: "h-full rounded-sm bg-brand/70", style: `width:${Math.max(2, entry.frac * 100)}%`})),
    );

    const fieldStatsPanel = (key) => {
        const res = result.val;
        if (!res || res.idxs.length === 0) return div({class: "px-2 py-1 text-[11px] text-gray-600"}, "No matches to sample.");
        const stats = store.fieldStats(res.idxs, key);
        return div(
            {class: "border-b border-gray-800/60 pb-1"},
            div({class: "px-2 py-0.5 text-[10px] text-gray-500"},
                `in ${Math.round(stats.coverage * 100)}% of ${fmtNum(stats.sampled)} sampled · ${stats.distinct} distinct`),
            ...stats.top.map(entry => fieldValueRow(key, entry)),
        );
    };

    const fieldRow = (key) => div(
        div(
            {class: "flex cursor-pointer items-center gap-1 px-1 py-0.5 hover:bg-gray-800/40",
                "data-testid": `logs2-field-${key}`,
                onclick: () => { fieldOpen.val = fieldOpen.val === key ? '' : key; }},
            caretRightIcon({class: () => `w-3 h-3 flex-none text-gray-600 transition-transform ${fieldOpen.val === key ? 'rotate-90' : ''}`}),
            span({class: "min-w-0 flex-1 truncate font-mono text-[11px] text-gray-300"}, key),
            button({
                type: "button",
                class: () => `cursor-pointer rounded p-0.5 ${columns.val.includes(key) ? 'text-brand' : 'text-gray-600 hover:text-gray-300'}`,
                title: () => columns.val.includes(key) ? "Remove column" : "Add as column",
                onclick: (e) => { e.stopPropagation(); toggleColumn(key); },
            }, columnsIcon({size: 12})),
        ),
        () => fieldOpen.val === key ? fieldStatsPanel(key) : '',
    );

    const sidebar = () => !sidebarOpen.val
        ? div(
            {class: "flex w-7 flex-none flex-col items-center border-r border-gray-800 pt-1.5"},
            button({
                type: "button", class: "cursor-pointer rounded p-1 text-gray-500 hover:bg-gray-800 hover:text-gray-200",
                title: "Show fields", onclick: () => { sidebarOpen.val = true; },
            }, columnsIcon({size: 14})),
        )
        : div(
            {class: "flex w-60 flex-none flex-col border-r border-gray-800"},
            div(
                {class: "flex flex-none items-center border-b border-gray-800 py-0.5 pl-2 pr-1"},
                span({class: "flex-1 text-[10px] font-medium uppercase tracking-wide text-gray-500"}, "fields"),
                button({
                    type: "button", class: "cursor-pointer rounded p-1 text-gray-500 hover:bg-gray-800 hover:text-gray-200",
                    title: "Hide fields", onclick: () => { sidebarOpen.val = false; },
                }, caretRightIcon({class: "w-3 h-3 rotate-180"})),
            ),
            div(
                {class: "app-scroll min-h-0 flex-1 overflow-y-auto pb-2"},
                microHeader("columns"),
                () => div(...columns.val.map(columnRow)),
                microHeader("available fields"),
                ...FIELD_KEYS.map(fieldRow),
            ),
        );

    // --- results table (virtualised) ----------------------------------------

    const colTemplate = (cols) => cols.map(k => {
        const d = COLUMN_DEFS[k] || {px: 100};
        return d.flex ? `minmax(${d.minPx}px,1fr)` : `${d.px}px`;
    }).join(' ');

    const colMinWidth = (cols) => cols.reduce((n, k) => {
        const d = COLUMN_DEFS[k] || {px: 100};
        return n + (d.flex ? d.minPx : d.px);
    }, 0);

    const headerRow = div({class: "sticky top-0 z-20 grid border-b border-gray-700 bg-gray-950"});
    const rowsHost = div({class: "relative"});
    const inner = div({}, headerRow, rowsHost);
    scroller = div({
        "data-testid": "logs2-results",
        class: "app-scroll min-h-0 flex-1 overflow-auto bg-gray-950",
    }, inner);

    const headerCell = (key) => {
        const d = COLUMN_DEFS[key] || {label: key};
        return div(
            {class: "group flex items-center gap-1 border-r border-gray-800/40 px-2 py-1 text-[10px] font-medium uppercase tracking-wide text-gray-500 last:border-r-0"},
            span({class: "truncate"}, d.label),
            key === 'msg' ? '' : span(
                {class: "hidden items-center group-hover:inline-flex"},
                button({
                    type: "button", class: "cursor-pointer px-0.5 text-gray-600 hover:text-red-400",
                    title: "Remove column", onclick: () => toggleColumn(key),
                }, xIcon({size: 10})),
            ),
        );
    };

    const cellEl = (key, rec) => {
        const d = COLUMN_DEFS[key] || {};
        const base = "overflow-hidden border-r border-gray-800/25 px-2 py-[3px] last:border-r-0";
        if (key === 'time') return div({class: `${base} whitespace-nowrap font-mono tabular-nums text-gray-400`}, fmtRowTime(rec.ts));
        if (key === 'level') return div({class: `${base} font-medium ${LEVEL_META[rec.level].text}`}, rec.level);
        if (key === 'msg') return div({
            class: `${base} font-mono text-gray-200 ${wrap.val ? 'line-clamp-3 whitespace-pre-wrap break-all' : 'truncate whitespace-pre'}`,
            title: wrap.val ? '' : rec.msg,
        }, rec.msg);
        const value = recordField(rec, key);
        if (value === undefined || value === '') return div({class: `${base} text-gray-700`}, "—");
        return div({class: `${base} truncate whitespace-nowrap ${d.num ? 'text-right tabular-nums' : ''} ${d.mono ? 'font-mono' : ''} text-gray-300`}, String(value));
    };

    const toggleExpand = (pos, idx) => {
        expanded.val = expanded.val && expanded.val.pos === pos ? null : {pos, idx};
        detailTab.val = 'fields';
        jsonCopied.val = false;
    };

    const detailFieldRow = (rec, key, value) => div(
        {class: "group grid grid-cols-[9rem_minmax(0,1fr)_auto] items-baseline gap-2 rounded px-1 py-0.5 hover:bg-gray-800/40"},
        span({class: "truncate font-mono text-[11px] text-gray-500"}, key),
        span({class: "break-all font-mono text-[11px] text-gray-200"}, String(value)),
        key === 'time' || key === 'msg' ? span() : div(
            {class: "hidden gap-0.5 group-hover:flex"},
            button({
                type: "button", class: "cursor-pointer rounded bg-gray-800 px-1 text-[11px] text-gray-300 hover:text-green-400",
                title: `Filter for ${key}:${value}`, onclick: () => addFilter(key, value),
            }, "+"),
            button({
                type: "button", class: "cursor-pointer rounded bg-gray-800 px-1 text-[11px] text-gray-300 hover:text-red-400",
                title: `Filter out ${key}:${value}`, onclick: () => addFilter(key, value, true),
            }, "−"),
            button({
                type: "button",
                class: () => `cursor-pointer rounded bg-gray-800 px-1 ${columns.val.includes(key) ? 'text-brand' : 'text-gray-300 hover:text-gray-100'}`,
                title: "Toggle column", onclick: () => toggleColumn(key),
            }, columnsIcon({size: 11})),
        ),
    );

    const detailEl = (y, rec) => {
        const tabButton = (key, text) => button({
            type: "button",
            class: () => `cursor-pointer border-b-2 px-2 py-1 text-[11px] ${detailTab.val === key
                ? 'border-brand text-gray-100' : 'border-transparent text-gray-500 hover:text-gray-300'}`,
            onclick: () => { detailTab.val = key; },
        }, text);
        const kvPairs = [
            ['time', new Date(rec.ts).toISOString()],
            ['level', rec.level], ['service', rec.service], ['host', rec.host],
            ['version', rec.version], ['logger', rec.logger],
            ...Object.entries(rec.extra),
            ['msg', rec.msg],
        ];
        return div(
            {style: `position:absolute;top:${y}px;left:0;right:0;`},
            div(
                {"data-testid": "logs2-detail",
                    class: "sticky left-0 z-10 flex flex-col overflow-hidden border-y border-gray-600 bg-gray-950 shadow-xl",
                    style: `height:${DETAIL_H}px;width:${scroller.clientWidth}px`,
                    onclick: (e) => e.stopPropagation()},
                div(
                    {class: "flex flex-none items-center gap-1 border-b border-gray-800 bg-gray-900/60 px-2"},
                    tabButton('fields', 'Fields'),
                    tabButton('json', 'JSON'),
                    div({class: "flex-1"}),
                    button({
                        type: "button",
                        class: "cursor-pointer rounded border border-gray-700 px-2 py-0.5 text-[11px] text-gray-300 hover:bg-gray-800",
                        "data-testid": "logs2-context-button",
                        onclick: () => { context.val = {anchorIdx: rec.idx}; },
                    }, "View in context"),
                    button({
                        type: "button", class: "cursor-pointer rounded p-1 text-gray-500 hover:bg-gray-800 hover:text-gray-200",
                        title: () => jsonCopied.val ? "Copied" : "Copy JSON",
                        onclick: async () => {
                            await navigator.clipboard.writeText(recordJson(rec));
                            jsonCopied.val = true;
                            setTimeout(() => { jsonCopied.val = false; }, 1500);
                        },
                    }, () => jsonCopied.val ? checkIcon({class: "w-3.5 h-3.5 text-green-400"}) : copyIcon({class: "w-3.5 h-3.5"})),
                    button({
                        type: "button", class: "cursor-pointer rounded p-1 text-gray-500 hover:bg-gray-800 hover:text-gray-200",
                        "aria-label": "Close details", onclick: () => { expanded.val = null; },
                    }, xIcon({size: 13})),
                ),
                div(
                    {class: "app-scroll min-h-0 flex-1 overflow-y-auto p-2"},
                    () => detailTab.val === 'json'
                        ? pre({class: "whitespace-pre-wrap break-all font-mono text-[11px] leading-4 text-gray-200"}, recordJson(rec))
                        : div(...kvPairs.filter(([, v]) => v !== undefined && v !== '').map(([k, v]) => detailFieldRow(rec, k, v))),
                ),
            ),
        );
    };

    const rowEl = (pos, y, rowH, cols, template, idx) => {
        const rec = store.eventFull(idx);
        const exp = expanded.val;
        const isExp = exp && exp.pos === pos;
        const tint = rec.level === 'ERROR' ? '#c42121' : rec.level === 'WARN' ? '#c67b04' : '';
        return div(
            {"data-testid": "logs2-row",
                class: `grid cursor-pointer items-start border-b border-gray-800/40 text-[11px] leading-[15px] ${isExp ? 'bg-gray-800/70' : 'hover:bg-gray-800/40'}`,
                style: `position:absolute;top:${y}px;left:0;right:0;height:${rowH}px;grid-template-columns:${template}`,
                onclick: () => toggleExpand(pos, idx)},
            tint ? span({style: `position:absolute;left:0;top:0;bottom:0;width:2px;background:${tint};opacity:.7`}) : '',
            ...cols.map(k => cellEl(k, rec)),
        );
    };

    let renderQueued = false;
    const renderRows = () => {
        renderQueued = false;
        const res = result.val;
        const cols = columns.val;
        const template = colTemplate(cols);
        const minW = colMinWidth(cols);
        inner.style.minWidth = `${minW}px`;
        headerRow.style.gridTemplateColumns = template;
        headerRow.replaceChildren(...cols.map(headerCell));

        const idxs = res?.idxs || [];
        const len = idxs.length;
        const rowH = wrap.val ? ROW_H_WRAP : ROW_H;
        const exp = expanded.val;
        rowsHost.style.height = `${len * rowH + (exp ? DETAIL_H : 0)}px`;
        if (len === 0) {
            rowsHost.replaceChildren(div(
                {class: "flex h-24 items-center justify-center text-xs text-gray-600"},
                searching.val ? 'Scanning…' : (res ? 'No events match.' : ''),
            ));
            return;
        }
        const stAdj = Math.max(0, scroller.scrollTop - HEADER_H);
        let first = Math.floor((exp && stAdj > (exp.pos + 1) * rowH ? stAdj - DETAIL_H : stAdj) / rowH) - OVERSCAN;
        first = Math.max(0, first);
        const visible = Math.ceil(scroller.clientHeight / rowH) + 2 * OVERSCAN + Math.ceil(DETAIL_H / rowH);
        const last = Math.min(len, first + visible);

        const children = [];
        for (let pos = first; pos < last; pos++) {
            const y = pos * rowH + (exp && pos > exp.pos ? DETAIL_H : 0);
            // Display newest first: position 0 is the newest match.
            children.push(rowEl(pos, y, rowH, cols, template, idxs[len - 1 - pos]));
        }
        if (exp && exp.pos >= first - Math.ceil(DETAIL_H / rowH) && exp.pos < last) {
            children.push(detailEl((exp.pos + 1) * rowH, store.eventFull(exp.idx)));
        }
        rowsHost.replaceChildren(...children);
    };

    const scheduleRender = () => {
        if (renderQueued) return;
        renderQueued = true;
        requestAnimationFrame(() => {
            renderRows();
            renderHistogram();
        });
    };

    scroller.onscroll = () => {
        if (renderQueued) return;
        renderQueued = true;
        requestAnimationFrame(renderRows);
    };
    window.addEventListener('resize', scheduleRender);

    van.derive(() => {
        void result.val; void columns.val; void wrap.val; void expanded.val;
        void searching.val; void detailTab.val; void jsonCopied.val;
        scheduleRender();
    });

    // --- context view --------------------------------------------------------

    const contextPanel = (ctx) => {
        let lo = Math.max(0, ctx.anchorIdx - 50);
        let hi = Math.min(store.total - 1, ctx.anchorIdx + 50);
        let anchorEl;

        const lineEl = (i) => {
            const rec = store.eventFull(i);
            const anchor = i === ctx.anchorIdx;
            const el = div(
                {class: `whitespace-pre-wrap break-all px-3 ${anchor ? 'border-l-2 border-amber-400 bg-amber-500/10' : 'border-l-2 border-transparent'}`},
                span({class: "text-gray-500"}, fmtClock(rec.ts)), '  ',
                span({class: LEVEL_META[rec.level].text}, rec.level.padEnd(5)), ' ',
                span({class: "text-gray-400"}, rec.service.padEnd(10)),
                span({class: "text-gray-600"}, rec.host.padEnd(8)),
                span({class: "text-gray-200"}, rec.msg),
            );
            if (anchor) anchorEl = el;
            return el;
        };

        const linesHost = div({class: "py-1"});
        for (let i = lo; i <= hi; i++) linesHost.append(lineEl(i));

        const moreButton = (text, onclick) => button({
            type: "button",
            class: "block w-full cursor-pointer py-1 text-center text-[11px] text-gray-500 hover:bg-gray-900 hover:text-gray-300 disabled:opacity-30",
            onclick,
        }, text);

        const body = div({class: "app-scroll min-h-0 flex-1 overflow-y-auto font-mono text-[11px] leading-5"});
        const earlierButton = moreButton("· · · load 50 earlier", () => {
            const newLo = Math.max(0, lo - 50);
            const add = [];
            for (let i = newLo; i < lo; i++) add.push(lineEl(i));
            const before = body.scrollHeight;
            linesHost.prepend(...add);
            body.scrollTop += body.scrollHeight - before;
            lo = newLo;
            earlierButton.disabled = lo === 0;
        });
        const laterButton = moreButton("load 50 later · · ·", () => {
            const newHi = Math.min(store.total - 1, hi + 50);
            for (let i = hi + 1; i <= newHi; i++) linesHost.append(lineEl(i));
            hi = newHi;
            laterButton.disabled = hi === store.total - 1;
        });
        earlierButton.disabled = lo === 0;
        laterButton.disabled = hi === store.total - 1;
        body.append(earlierButton, linesHost, laterButton);

        const anchorRec = store.eventFull(ctx.anchorIdx);
        const overlay = div(
            {"data-testid": "logs2-context",
                class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-6",
                tabindex: "-1",
                onkeydown: (e) => { if (e.key === 'Escape') context.val = null; },
                onclick: (e) => { if (e.target === overlay) context.val = null; }},
            div(
                {class: "flex h-[82vh] w-full max-w-5xl flex-col overflow-hidden rounded border border-gray-700 bg-gray-950 shadow-2xl"},
                div(
                    {class: "flex flex-none items-center gap-2 border-b border-gray-800 bg-gray-900/60 px-3 py-1.5"},
                    span({class: "text-xs text-gray-200"},
                        `Context around ${fmtRowTime(anchorRec.ts)} — ${anchorRec.service} on ${anchorRec.host}`),
                    span({class: "text-[10px] text-gray-500"}, "search filters do not apply here"),
                    div({class: "flex-1"}),
                    button({
                        type: "button", class: "cursor-pointer rounded p-1 text-gray-500 hover:bg-gray-800 hover:text-gray-200",
                        "aria-label": "Close context view", onclick: () => { context.val = null; },
                    }, xIcon({size: 14})),
                ),
                body,
            ),
        );
        setTimeout(() => {
            overlay.focus();
            anchorEl?.scrollIntoView({block: 'center'});
        }, 0);
        return overlay;
    };

    // --- assemble ------------------------------------------------------------

    setTimeout(() => void runSearch(), 0);

    return div(
        {class: "flex h-full min-h-0 flex-col overflow-hidden bg-surface"},
        searchBar,
        histogramBand,
        div(
            {class: "flex min-h-0 flex-1"},
            () => sidebar(),
            div({class: "flex min-w-0 flex-1 flex-col"}, scroller),
        ),
        () => context.val ? contextPanel(context.val) : '',
    );
}

// ---------------------------------------------------------------------------
// Fixture chrome.
// ---------------------------------------------------------------------------

const pageHost = div({class: "contents"});
const renderPage = () => pageHost.replaceChildren(logsPageProposal());

van.add(document.body,
    div(
        {class: "flex h-full min-h-0 flex-col"},
        header(
            {class: "shrink-0 border-b border-gray-800 bg-gray-950/85 px-4 py-3"},
            div(
                {class: "mx-auto flex max-w-[1600px] items-end justify-between gap-3"},
                div(
                    h1({class: "text-lg font-semibold text-white"}, "Logs page redesign fixture"),
                    p({class: "mt-1 text-xs text-gray-500"},
                        "Structured events instead of a textarea: virtualised rows, field columns, histogram with brush-zoom, row expansion, ±context. ",
                        span({class: "text-gray-400"}, `Dataset: ${store.total.toLocaleString()} deterministic events over 48 h, generated per-index on demand.`)),
                ),
                button({type: "button", class: "btn-secondary px-3 py-1.5 text-xs", onclick: renderPage}, "Reset"),
            ),
        ),
        main(
            {class: "mx-auto flex min-h-0 w-full max-w-[1600px] flex-1 flex-col overflow-hidden p-4"},
            div({class: "flex min-h-0 flex-1 flex-col overflow-hidden rounded border border-gray-800"}, pageHost),
        ),
    ),
);

renderPage();
