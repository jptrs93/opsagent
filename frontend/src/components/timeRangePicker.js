import van from "vanjs-core";
import {checkIcon, chevronDownIcon} from "../lib/icons.js";

const {button, div, input, span} = van.tags;

const MIN = 60_000;
const HOUR = 3_600_000;
const DAY = 24 * HOUR;
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const pad2 = (n) => String(n).padStart(2, '0');

export const RANGE_PRESETS = [
    {key: '5m', label: 'Last 5 minutes', ms: 5 * MIN},
    {key: '15m', label: 'Last 15 minutes', ms: 15 * MIN},
    {key: '30m', label: 'Last 30 minutes', ms: 30 * MIN},
    {key: '1h', label: 'Last hour', ms: HOUR},
    {key: '3h', label: 'Last 3 hours', ms: 3 * HOUR},
    {key: '6h', label: 'Last 6 hours', ms: 6 * HOUR},
    {key: '12h', label: 'Last 12 hours', ms: 12 * HOUR},
    {key: '24h', label: 'Last 24 hours', ms: 24 * HOUR},
    {key: '2d', label: 'Last 2 days', ms: 2 * DAY},
    {key: '4d', label: 'Last 4 days', ms: 4 * DAY},
    {key: '7d', label: 'Last 7 days', ms: 7 * DAY},
    {key: '14d', label: 'Last 14 days', ms: 14 * DAY},
    {key: '21d', label: 'Last 21 days', ms: 21 * DAY},
    {key: '30d', label: 'Last 30 days', ms: 30 * DAY},
];

export function fmtShortTime(ts) {
    const d = new Date(ts);
    return `${MONTHS[d.getMonth()]} ${d.getDate()} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

function toLocalInputValue(ts) {
    const d = new Date(ts);
    return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}T${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

const presetOf = (key) => RANGE_PRESETS.find(pr => pr.key === key) || RANGE_PRESETS[RANGE_PRESETS.length - 1];

export function resolveRange(r) {
    if (r.kind === 'custom') return {startTs: r.startTs, endTs: r.endTs};
    const endTs = Date.now();
    return {startTs: endTs - presetOf(r.key).ms, endTs};
}

export function rangeLabel(r) {
    if (r.kind === 'custom') return `${fmtShortTime(r.startTs)} – ${fmtShortTime(r.endTs)}`;
    return presetOf(r.key).label;
}

export function timeRangePicker({rangeS, onChange, testid}) {
    const open = van.state(false);

    const presetRow = (preset) => button({
        type: "button",
        class: "flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs text-gray-200 hover:bg-gray-800 cursor-pointer",
        onclick: () => {
            rangeS.val = {kind: 'preset', key: preset.key};
            open.val = false;
            onChange?.();
        },
    },
        span({class: "w-4"}, () => rangeS.val.kind === 'preset' && rangeS.val.key === preset.key ? checkIcon({class: "w-3.5 h-3.5 text-brand"}) : ''),
        preset.label);

    const customRange = () => {
        const rangeError = van.state('');
        const dtInput = (suffix, ts) => input({
            "data-testid": `${testid}-custom-${suffix}`,
            class: "input min-w-0 flex-1 py-1 text-xs [color-scheme:dark]",
            type: "datetime-local",
            value: toLocalInputValue(ts),
            oninput: () => { rangeError.val = ''; },
        });
        const {startTs, endTs} = resolveRange(rangeS.val);
        const startInput = dtInput("start", startTs);
        const endInput = dtInput("end", endTs);
        const dtField = (label, el) => div(
            {class: "flex items-center gap-2"},
            span({class: "w-8 flex-none text-[10px] text-gray-500"}, label),
            el,
        );
        return div(
            {class: "border-t border-gray-800 p-3 flex flex-col gap-1.5"},
            span({class: "text-[10px] uppercase tracking-wide text-gray-500"}, "custom range"),
            dtField("from", startInput),
            dtField("to", endInput),
            () => rangeError.val ? span({class: "text-[10px] text-red-400"}, rangeError.val) : '',
            button({
                "data-testid": `${testid}-custom-apply`,
                type: "button",
                class: "mt-1 cursor-pointer rounded-[0.3rem] border border-gray-600 bg-gray-700 py-1 text-xs text-gray-200 transition-colors hover:bg-gray-600",
                onclick: () => {
                    const start = new Date(startInput.value).getTime();
                    const end = new Date(endInput.value).getTime();
                    if (!Number.isFinite(start) || !Number.isFinite(end)) { rangeError.val = 'Enter a complete start and end date.'; return; }
                    if (end <= start) { rangeError.val = 'End must be after start.'; return; }
                    rangeS.val = {kind: 'custom', startTs: start, endTs: end};
                    open.val = false;
                    onChange?.();
                },
            }, "Apply"),
        );
    };

    return div(
        {class: "relative"},
        button({
            "data-testid": `${testid}-time-button`,
            type: "button",
            class: "input flex h-[30px] items-center gap-1.5 whitespace-nowrap text-xs text-gray-200 cursor-pointer hover:bg-gray-700",
            onclick: () => { open.val = !open.val; },
        }, () => rangeLabel(rangeS.val), chevronDownIcon({class: "w-3 h-3 text-gray-500"})),
        () => !open.val ? '' : div(
            div({class: "fixed inset-0 z-20", onclick: () => { open.val = false; }}),
            div(
                {class: "absolute right-0 top-full z-30 mt-1 w-[22rem] rounded border border-gray-700 bg-gray-900 py-1 shadow-xl"},
                div({class: "grid grid-flow-col grid-cols-2 grid-rows-[repeat(7,auto)]"}, ...RANGE_PRESETS.map(presetRow)),
                customRange(),
            ),
        ),
    );
}
