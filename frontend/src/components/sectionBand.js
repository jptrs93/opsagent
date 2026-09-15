import van from "vanjs-core";
import {caretRightIcon, chevronDownIcon, plusIcon} from "../lib/icons.js";

const {button, div, h2, span} = van.tags;

// Collapsible section header for the flush pages (IAM, nodes): a band running
// edge to edge instead of a card heading, set off by background alone — no
// borders. The toggle sits inside a real h2 so the section still reads (and
// tests) as a heading.
// min-h keeps every band the same height whether or not it carries actions;
// 34px fits the tallest occupant (the users search input) so a band with an
// input is no taller than one without.
export const sectionBand = (openState, title, count, ...actions) => div(
    {class: "flex flex-none flex-wrap items-center gap-2 bg-gray-950/40 px-2 py-1 min-h-[34px]"},
    h2({class: "text-[11px] font-semibold leading-none"},
        button({
            type: "button",
            "aria-expanded": () => String(openState.val),
            class: "inline-flex items-center gap-1.5 rounded px-1 py-0.5 text-[11px] font-semibold uppercase tracking-wider text-gray-400 hover:text-gray-200 cursor-pointer",
            onclick: () => { openState.val = !openState.val; },
        },
        chevronDownIcon({class: () => `w-3 h-3 transition-transform ${openState.val ? "" : "-rotate-90"}`}),
        title)),
    count ? span({class: "text-[11px] text-gray-500 tabular-nums"}, count) : "",
    div({class: "flex-1"}),
    ...actions,
);

export const bandButton = (text, onclick, icon = plusIcon({class: "w-3 h-3"})) => button({
    type: "button",
    class: "inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded border border-gray-600 text-gray-300 hover:bg-surface-hover cursor-pointer",
    onclick,
}, icon, text);

// The space band's style from the Deployments, Assets and Secrets explorers,
// as the Roles tab and the Settings page use it between their sections: a
// recessed row with a caret, a semibold mono title and a small count, the
// whole band toggling the section. `count` may be a function for a live
// number.
export const explorerBand = (openState, title, count) => div(
    {
        class: "flex flex-none cursor-default items-center gap-1.5 border-b border-gray-800/80 bg-gray-950/30 px-2 py-1 font-mono text-[13px] hover:bg-gray-700/35",
        onclick: () => { openState.val = !openState.val; },
    },
    button({
        type: "button",
        "aria-expanded": () => String(openState.val),
        "aria-label": () => openState.val ? `Collapse ${title}` : `Expand ${title}`,
        class: "flex h-4 w-4 flex-none items-center justify-center rounded-sm text-gray-500 hover:text-gray-100 hover:bg-white/10 cursor-pointer",
        onclick: (e) => { e.stopPropagation(); openState.val = !openState.val; },
    }, caretRightIcon({class: () => `w-[11px] h-[11px] transition-transform ${openState.val ? "rotate-90" : ""}`})),
    h2({class: "font-semibold text-gray-100"}, title),
    span({class: "text-[10.5px] text-gray-500"}, count),
);
