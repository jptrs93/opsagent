import van from "vanjs-core";
import {infoIcon} from "../lib/icons.js";

const {button, div} = van.tags;

const HOVER_DELAY_MS = 180;
const WIDTH = 272;
const EDGE = 8;
const GAP = 6;
const MIN_BELOW = 150;
const TIP_ID = "info-tip-card";

const shown = van.state(null);
let mounted = false;
let hoverTimer = null;
let nextId = 0;

const triggerSelector = (id) => `[data-info-tip="${CSS.escape(id)}"]`;
const triggersOf = (id) => document.querySelectorAll(triggerSelector(id));
const insideTrigger = (target, id) => Boolean(target?.closest?.(triggerSelector(id)));

function place(el) {
    const rect = el.getBoundingClientRect();
    const centre = rect.left + rect.width / 2;
    const left = Math.round(Math.max(EDGE, Math.min(centre - WIDTH / 2, window.innerWidth - WIDTH - EDGE)));
    const below = window.innerHeight - rect.bottom - GAP - EDGE;
    return below >= MIN_BELOW || below >= rect.top
        ? {left, top: Math.round(rect.bottom + GAP)}
        : {left, bottom: Math.round(window.innerHeight - rect.top + GAP)};
}

const onPointerDown = (event) => {
    const tip = shown.rawVal;
    if (!tip) return;
    if (insideTrigger(event.target, tip.id) || event.target?.closest?.(`#${TIP_ID}`)) return;
    hide();
};

const onPointerMove = (event) => {
    const tip = shown.rawVal;
    if (tip && !tip.pinned && tip.via === "hover" && !insideTrigger(event.target, tip.id)) hide();
};

const onFocusIn = (event) => {
    const tip = shown.rawVal;
    if (tip && !tip.pinned && tip.via === "focus" && !insideTrigger(event.target, tip.id)) hide();
};

const onKeyDown = (event) => {
    if (event.key === "Escape" && shown.rawVal) hide();
};

function listen(on) {
    const act = on ? "addEventListener" : "removeEventListener";
    document[act]("pointerdown", onPointerDown, true);
    document[act]("pointermove", onPointerMove, true);
    document[act]("focusin", onFocusIn, true);
    document[act]("keydown", onKeyDown, true);
    window[act]("scroll", hide, true);
    window[act]("resize", hide);
}

function hide() {
    clearTimeout(hoverTimer);
    const tip = shown.rawVal;
    if (!tip) return;
    for (const el of triggersOf(tip.id)) el.removeAttribute("aria-describedby");
    shown.val = null;
    listen(false);
}

const card = () => {
    const tip = shown.val;
    if (!tip) return "";
    const paragraphs = Array.isArray(tip.text) ? tip.text : [tip.text];
    const vertical = tip.at.top !== undefined ? `top:${tip.at.top}px` : `bottom:${tip.at.bottom}px`;
    return div(
        {
            id: TIP_ID,
            role: "tooltip",
            "data-testid": "info-tip-card",
            "data-for": tip.id,
            "data-pinned": String(tip.pinned),
            class: `info-tip-card ${tip.pinned ? "" : "pointer-events-none"}`,
            style: `left:${tip.at.left}px;${vertical};width:${WIDTH}px`,
        },
        tip.label ? div({class: "info-tip-title"}, tip.label) : "",
        ...paragraphs.map((text) => div({class: "info-tip-text"}, text)),
        tip.note ? div({class: "info-tip-note"}, tip.note) : "",
    );
};

function show(el, content, {pinned, via}) {
    clearTimeout(hoverTimer);
    if (!mounted) {
        mounted = true;
        van.add(document.body, card);
    }
    const before = shown.rawVal;
    if (before && before.id !== content.id) hide();
    if (!shown.rawVal) listen(true);
    shown.val = {...content, at: place(el), pinned, via};
    el.setAttribute("aria-describedby", TIP_ID);
}

// One card for the whole page, kept outside the trigger: a trigger inside a
// reactive table header is rebuilt whenever the table is, and a card owned by
// it would vanish mid-read. Give a stable id where that can happen.
export function infoTip({id, label, text, note, testid}) {
    const content = {id: id || `info-tip-${++nextId}`, label, text, note};
    const isShown = () => shown.rawVal?.id === content.id;
    const el = button({
        type: "button",
        class: "info-tip",
        "data-info-tip": content.id,
        ...(testid ? {"data-testid": testid} : {}),
        "aria-label": label ? `About ${label}` : "More information",
        onclick: (event) => {
            event.stopPropagation();
            if (isShown() && shown.rawVal.pinned) hide();
            else show(el, content, {pinned: true, via: "click"});
        },
        onmouseenter: () => {
            if (isShown()) return;
            clearTimeout(hoverTimer);
            hoverTimer = setTimeout(() => { if (el.isConnected) show(el, content, {pinned: false, via: "hover"}); }, HOVER_DELAY_MS);
        },
        onmouseleave: () => clearTimeout(hoverTimer),
        onfocus: () => {
            if (!isShown() && el.matches(":focus-visible")) show(el, content, {pinned: false, via: "focus"});
        },
        onblur: () => {
            if (isShown() && !shown.rawVal.pinned && shown.rawVal.via === "focus") hide();
        },
    }, infoIcon({class: "h-full w-full"}));
    return el;
}
