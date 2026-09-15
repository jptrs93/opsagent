import van from "vanjs-core";
import {checkIcon, chevronDownIcon, closeIcon} from "../lib/icons.js";
import {LOG_LEVELS, LOG_LEVEL_NONE, LOG_LEVEL_OTHER, logLevelMeta} from "../lib/logLevels.js";

const {button, div, span} = van.tags;

// Menu order runs from the quietest level up, then the two catch-all buckets.
const MENU_ORDER = ['DEBUG', 'INFO', 'WARN', 'ERROR', LOG_LEVEL_OTHER, LOG_LEVEL_NONE];
const rowKey = (level) => (level || 'none').toLowerCase();

// logLevelPicker is the multi-select level filter on the Logs search bar. onS
// holds {level: bool} over LOG_LEVELS (OTHER is any parsed level outside the
// four named ones, '' a line with no parsed level); onChange fires after every
// edit. The button reads "All levels", the selected names, "All but ..." or
// "No levels" depending on how much of the set is on.
export function logLevelPicker({onS, onChange, testid = "logs-level"}) {
    const open = van.state(false);
    const labelOf = (level) => logLevelMeta(level).label;
    const on = () => MENU_ORDER.filter(l => onS.val[l]);
    const off = () => MENU_ORDER.filter(l => !onS.val[l]);

    const label = () => {
        const selected = on(), hidden = off();
        if (hidden.length === 0) return 'All levels';
        if (selected.length === 0) return 'No levels';
        if (selected.length <= 3) return selected.map(labelOf).join(', ');
        return `All but ${hidden.map(labelOf).join(', ')}`;
    };

    const set = (next) => {
        onS.val = next;
        onChange?.();
    };

    const item = (attrs, selected, onclick, ...children) => button({
        type: "button",
        ...attrs,
        class: "flex w-full cursor-pointer items-center gap-1.5 px-2 py-1 text-left text-xs text-gray-200 hover:bg-gray-800",
        onclick,
    }, span({class: "w-3.5 flex-none"}, selected ? checkIcon({class: "w-3.5 h-3.5 text-brand"}) : ''), ...children);

    const menu = () => div(
        {"data-testid": `${testid}-panel`, class: "absolute left-0 top-full z-30 mt-1 flex min-w-40 flex-col rounded border border-gray-700 bg-gray-900 py-1 shadow-xl"},
        ...MENU_ORDER.map(level => item(
            {
                "data-testid": `${testid}-row-${rowKey(level)}`,
                role: "menuitemcheckbox",
                "aria-checked": String(Boolean(onS.val[level])),
            },
            Boolean(onS.val[level]),
            () => set({...onS.val, [level]: !onS.val[level]}),
            span({class: "h-2 w-2 flex-none rounded-[2px]", style: `background:${logLevelMeta(level).fill}`}),
            labelOf(level),
        )),
        ...(off().length === 0 ? [] : [
            div({class: "my-1 border-t border-gray-800"}),
            item({"data-testid": `${testid}-row-all`}, false, () => set(Object.fromEntries(LOG_LEVELS.map(l => [l, true]))),
                closeIcon({class: "w-3.5 h-3.5 flex-none text-brand"}), "Show all levels"),
        ]),
    );

    return div(
        {class: "relative"},
        button({
            "data-testid": `${testid}-button`,
            type: "button",
            "aria-haspopup": "true",
            "aria-expanded": () => String(open.val),
            "aria-label": "Filter levels",
            class: "input flex h-[30px] items-center gap-1.5 whitespace-nowrap text-xs text-gray-200 cursor-pointer hover:bg-gray-700",
            onclick: () => { open.val = !open.val; },
        }, () => label(), chevronDownIcon({class: "w-3 h-3 text-gray-500"})),
        () => !open.val ? '' : div(
            div({class: "fixed inset-0 z-20", onclick: () => { open.val = false; }}),
            () => menu(),
        ),
    );
}
