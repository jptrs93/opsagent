import van from "vanjs-core";
import {chevronDownIcon, plusIcon} from "../lib/icons.js";

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
