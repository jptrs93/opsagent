import van from "vanjs-core";

const {div, span} = van.tags;
const {svg, path, line, text: svgText, rect} = van.tags("http://www.w3.org/2000/svg");

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const pad2 = (n) => String(n).padStart(2, '0');

const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];

function fmtBytes(v, suffix = '') {
    if (!Number.isFinite(v)) return '–';
    let i = 0;
    let n = Math.abs(v);
    while (n >= 1024 && i < BYTE_UNITS.length - 1) { n /= 1024; i++; }
    const digits = n >= 100 || i === 0 ? 0 : n >= 10 ? 1 : 2;
    return `${v < 0 ? '-' : ''}${n.toFixed(digits)} ${BYTE_UNITS[i]}${suffix}`;
}

function fmtCount(v) {
    if (!Number.isFinite(v)) return '–';
    const a = Math.abs(v);
    if (a >= 1e9) return `${(v / 1e9).toFixed(2)}G`;
    if (a >= 1e6) return `${(v / 1e6).toFixed(2)}M`;
    if (a >= 1e4) return `${(v / 1e3).toFixed(1)}k`;
    if (Number.isInteger(v)) return String(v);
    return v.toFixed(a >= 10 ? 1 : 2);
}

export function formatValue(v, unit) {
    if (!Number.isFinite(v)) return '–';
    switch (unit) {
        case 'bytes': return fmtBytes(v);
        case 'bytes/s': return fmtBytes(v, '/s');
        case 'cores': return v.toFixed(v >= 10 ? 1 : v >= 1 ? 2 : 3);
        case 'pct': return `${v.toFixed(v >= 10 ? 0 : 1)}%`;
        case 'per_s': return `${fmtCount(v)}/s`;
        default: return fmtCount(v);
    }
}

function fmtTick(ts, spanMs) {
    const d = new Date(ts);
    if (spanMs > 3 * 24 * 3_600_000) return `${MONTHS[d.getMonth()]} ${d.getDate()}`;
    if (spanMs > 24 * 3_600_000) return `${d.getDate()}/${pad2(d.getMonth() + 1)} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
    return `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

function fmtTooltipTime(ts) {
    const d = new Date(ts);
    return `${MONTHS[d.getMonth()]} ${d.getDate()} ${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
}

function niceTicks(max, n) {
    if (!(max > 0)) return [0, 1];
    const raw = max / n;
    const mag = Math.pow(10, Math.floor(Math.log10(raw)));
    const norm = raw / mag;
    const step = (norm <= 1 ? 1 : norm <= 2 ? 2 : norm <= 2.5 ? 2.5 : norm <= 5 ? 5 : 10) * mag;
    const ticks = [];
    for (let v = 0; v <= max + step * 0.001; v += step) ticks.push(v);
    if (ticks[ticks.length - 1] < max) ticks.push(ticks[ticks.length - 1] + step);
    return ticks;
}

function byteTicks(max, n) {
    if (!(max > 0)) return [0, 1024];
    let scale = 1;
    while (max / scale >= 1024 && scale < 1024 ** 4) scale *= 1024;
    return niceTicks(max / scale, n).map(t => t * scale);
}

const PAD_L = 52, PAD_R = 8, PAD_T = 8, PAD_B = 20;

export function lineChart({title, unit, dataS, height = 160, testid, emptyText = 'No data in range'}) {
    const plotHost = div({class: "relative w-full", style: `height:${height}px`});
    const tooltip = div({class: "pointer-events-none absolute z-30 hidden rounded border border-gray-700 bg-gray-900 px-2 py-1 text-[10px] shadow-xl"});
    const legend = div({class: "flex flex-wrap items-center gap-x-3 gap-y-0.5 px-1 pt-1"});
    let hidden = new Set();
    let lastData = null;

    const visibleSeries = () => (lastData?.series || []).filter(s => !hidden.has(s.label));

    const render = () => {
        const w = plotHost.clientWidth;
        const data = lastData;
        if (!w) return;
        const h = height;
        if (!data || !data.buckets || !data.series.length) {
            plotHost.replaceChildren(
                div({class: "flex h-full items-center justify-center text-[11px] text-gray-600"}, emptyText),
                tooltip,
            );
            legend.replaceChildren();
            return;
        }
        const series = visibleSeries();
        const n = data.buckets;
        let max = 0;
        for (const s of series) for (const v of s.values) if (Number.isFinite(v) && v > max) max = v;
        const ticks = unit === 'bytes' || unit === 'bytes/s' ? byteTicks(max, 4) : niceTicks(unit === 'pct' ? Math.max(max, 1) : max, 4);
        const yMax = ticks[ticks.length - 1] || 1;
        const plotW = Math.max(1, w - PAD_L - PAD_R);
        const plotH = h - PAD_T - PAD_B;
        const xOf = (b) => PAD_L + ((b + 0.5) / n) * plotW;
        const yOf = (v) => PAD_T + plotH - (v / yMax) * plotH;

        const children = [];
        for (const t of ticks) {
            const y = yOf(t);
            children.push(line({x1: String(PAD_L), y1: y.toFixed(1), x2: String(w - PAD_R), y2: y.toFixed(1), stroke: "#1f2937", "stroke-width": "1"}));
            children.push(svgText({x: String(PAD_L - 6), y: (y + 3).toFixed(1), "text-anchor": "end", fill: "#6b7280", "font-size": "9"}, formatValue(t, unit)));
        }
        const spanMs = n * data.stepMs;
        const xTickCount = Math.max(2, Math.min(6, Math.floor(plotW / 90)));
        for (let i = 0; i <= xTickCount; i++) {
            const b = Math.round((i / xTickCount) * n);
            const ts = data.startTs + b * data.stepMs;
            const x = PAD_L + (b / n) * plotW;
            children.push(svgText({
                x: x.toFixed(1), y: String(h - 6),
                "text-anchor": i === 0 ? "start" : i === xTickCount ? "end" : "middle",
                fill: "#6b7280", "font-size": "9",
            }, fmtTick(ts, spanMs)));
        }
        for (const s of series) {
            let d = '';
            let pen = false;
            let lone = [];
            for (let b = 0; b < n; b++) {
                const v = s.values[b];
                if (!Number.isFinite(v)) { pen = false; continue; }
                const x = xOf(b), y = yOf(v);
                const prevOk = b > 0 && Number.isFinite(s.values[b - 1]);
                const nextOk = b < n - 1 && Number.isFinite(s.values[b + 1]);
                if (!prevOk && !nextOk) lone.push([x, y]);
                d += `${pen ? 'L' : 'M'}${x.toFixed(1)} ${y.toFixed(1)}`;
                pen = true;
            }
            if (d) children.push(path({d, fill: "none", stroke: s.color, "stroke-width": "1.5", "stroke-linejoin": "round"}));
            for (const [x, y] of lone) children.push(rect({x: (x - 1.5).toFixed(1), y: (y - 1.5).toFixed(1), width: "3", height: "3", fill: s.color}));
        }
        const crosshair = line({x1: "0", y1: String(PAD_T), x2: "0", y2: String(PAD_T + plotH), stroke: "#6b7280", "stroke-width": "1", "stroke-dasharray": "2 2", visibility: "hidden"});
        children.push(crosshair);
        const el = svg({width: String(w), height: String(h), viewBox: `0 0 ${w} ${h}`, class: "block", ...(testid ? {"data-testid": `${testid}-svg`} : {})}, ...children);
        plotHost.replaceChildren(el, tooltip);
        plotHost.__crosshair = crosshair;
        plotHost.__geom = {n, plotW, xOf};

        legend.replaceChildren(...(data.series.map(s => {
            const off = hidden.has(s.label);
            const last = [...s.values].reverse().find(Number.isFinite);
            return span({
                class: `flex cursor-pointer items-center gap-1 text-[10px] ${off ? 'opacity-40' : ''}`,
                title: off ? 'Show series' : 'Hide series',
                onclick: () => {
                    hidden.has(s.label) ? hidden.delete(s.label) : hidden.add(s.label);
                    render();
                },
            },
                span({class: "inline-block h-[3px] w-3 rounded-sm", style: `background:${s.color}`}),
                span({class: "text-gray-400"}, s.label),
                span({class: "tabular-nums text-gray-500"}, formatValue(last, unit)),
            );
        })));
    };

    plotHost.onpointermove = (e) => {
        const data = lastData;
        const g = plotHost.__geom;
        if (!data || !g) return;
        const box = plotHost.getBoundingClientRect();
        const x = e.clientX - box.left;
        const b = Math.max(0, Math.min(g.n - 1, Math.floor(((x - PAD_L) / g.plotW) * g.n)));
        const cx = g.xOf(b);
        plotHost.__crosshair.setAttribute('x1', cx.toFixed(1));
        plotHost.__crosshair.setAttribute('x2', cx.toFixed(1));
        plotHost.__crosshair.setAttribute('visibility', 'visible');
        const t0 = data.startTs + b * data.stepMs;
        tooltip.replaceChildren(
            div({class: "mb-0.5 text-gray-400"}, fmtTooltipTime(t0)),
            ...visibleSeries().map(s => div(
                {class: "flex items-center gap-1.5"},
                span({class: "h-[3px] w-2.5 rounded-sm", style: `background:${s.color}`}),
                span({class: "text-gray-300"}, s.label),
                span({class: "ml-auto tabular-nums text-gray-100"}, formatValue(s.values[b], unit)),
            )),
        );
        tooltip.classList.remove('hidden');
        const tipW = 170;
        tooltip.style.left = `${cx + 12 + tipW > box.width ? cx - 12 - tipW : cx + 12}px`;
        tooltip.style.top = `4px`;
        tooltip.style.minWidth = `${tipW}px`;
    };
    plotHost.onpointerleave = () => {
        tooltip.classList.add('hidden');
        plotHost.__crosshair?.setAttribute('visibility', 'hidden');
    };

    van.derive(() => {
        lastData = dataS.val;
        hidden = new Set();
        render();
    });
    if (typeof ResizeObserver !== 'undefined') {
        new ResizeObserver(() => render()).observe(plotHost);
    }

    return div(
        {"data-testid": testid, class: "flex min-w-0 flex-col rounded border border-gray-800 bg-gray-950/40 p-2"},
        div({class: "flex items-center justify-between pb-1"},
            span({class: "text-[11px] font-medium text-gray-300"}, title),
            span({class: "text-[10px] text-gray-600"}, unit === 'count' ? '' : unit)),
        plotHost,
        legend,
    );
}
