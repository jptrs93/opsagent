import van from "vanjs-core";
import {caretRightIcon} from "../lib/icons.js";

const {button, div, h2, span} = van.tags;

// The space band's style from the Deployments, Assets and Secrets explorers,
// as the Roles tab and the Settings page use it between their sections: a
// recessed row with a caret, a semibold mono title and a small count, the
// whole band toggling the section. `count` may be a function for a live
// number.
export const explorerBand = (openState, title, count) => div(
    {
        class: "band cursor-default",
        onclick: () => { openState.val = !openState.val; },
    },
    button({
        type: "button",
        "aria-expanded": () => String(openState.val),
        "aria-label": () => openState.val ? `Collapse ${title}` : `Expand ${title}`,
        class: "flex h-4 w-4 flex-none items-center justify-center rounded-sm text-gray-500 hover:text-gray-100 hover:bg-white/10 cursor-pointer",
        onclick: (e) => { e.stopPropagation(); openState.val = !openState.val; },
    }, caretRightIcon({class: () => `w-[11px] h-[11px] transition-transform ${openState.val ? "rotate-90" : ""}`})),
    h2({class: "band-title"}, title),
    span({class: "band-count"}, count),
    span({class: "band-rule"}),
);
