import van from "vanjs-core";
import {closeIcon} from "../lib/icons.js";

const {button, div, span} = van.tags;

export const tabStrip = ({label, testid} = {}, ...children) => div(
    {class: "page-tabs", role: "tablist", "aria-label": label, ...(testid ? {"data-testid": testid} : {})},
    ...children,
);

export function tabButton({
    id,
    activeTab,
    title,
    testid,
    count = null,
    icon = null,
    lead = null,
    trailing = null,
    mono = false,
    deep = false,
    dirty = null,
    dirtyTestid = "",
    onClose = null,
}) {
    const active = () => activeTab.val === id;
    const select = () => { activeTab.val = id; };
    return div(
        {
            role: "tab",
            "aria-selected": () => String(active()),
            ...(testid ? {"data-testid": testid} : {}),
            tabindex: 0,
            class: `tab ${onClose ? "" : "tab-pinned"} ${deep ? "tab-deep" : ""}`,
            onclick: select,
            onkeydown: (event) => {
                if (event.key === "Enter" || event.key === " ") { event.preventDefault(); select(); }
                if (onClose && (event.key === "Delete" || event.key === "Backspace")) onClose();
            },
            onauxclick: (event) => {
                if (onClose && event.button === 1) { event.preventDefault(); onClose(); }
            },
        },
        icon ? icon({class: "tab-icon"}) : "",
        lead || "",
        span({class: `tab-title ${mono ? "font-mono font-normal" : ""}`}, title),
        count ? span({class: "tab-count"}, count) : "",
        trailing || "",
        dirty ? span({
            class: () => dirty.val ? "inline-block h-1.5 w-1.5 flex-none rounded-full bg-amber-300" : "hidden",
            title: "Unsaved changes",
            ...(dirtyTestid ? {"data-testid": dirtyTestid} : {}),
        }) : "",
        onClose ? button({
            type: "button",
            "aria-label": `Close ${title}`,
            title: "Close tab",
            class: "tab-close",
            onclick: (event) => { event.stopPropagation(); onClose(); },
        }, closeIcon({class: "w-3 h-3"})) : "",
    );
}

export const tabPanel = (activeTab, id, content) => div(
    {class: () => `h-full min-h-0 min-w-0 ${activeTab.val === id ? "" : "hidden"}`},
    content,
);
