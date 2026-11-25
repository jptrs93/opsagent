import van from "vanjs-core";
import {
    MAX_COLUMN_WIDTH,
    MIN_COLUMN_WIDTH,
    clampWidth,
    columnWidthVars,
    readColumnWidths,
    withoutColumn,
} from "../lib/columnWidths.js";

const {span} = van.tags;

const STEP = 16;
const BIG_STEP = 48;

export function columnGrip({label, min = MIN_COLUMN_WIDTH, max = MAX_COLUMN_WIDTH, width, onPreview, onCommit, onReset}) {
    let el = null;
    const current = () => width ? width() : el.parentElement.getBoundingClientRect().width;

    const startDrag = (event) => {
        if (event.button !== 0) return;
        event.preventDefault();
        event.stopPropagation();
        const startX = event.clientX;
        const startWidth = current();
        let next = startWidth;
        let moved = false;
        const move = (ev) => {
            const delta = ev.clientX - startX;
            if (!moved && delta === 0) return;
            moved = true;
            next = clampWidth(startWidth + delta, min, max);
            onPreview?.(next, el);
        };
        const end = () => {
            el.removeEventListener("pointermove", move);
            el.removeEventListener("pointerup", end);
            el.removeEventListener("pointercancel", end);
            document.body.classList.remove("resizing");
            if (moved) onCommit(next, el);
        };
        try { el.setPointerCapture(event.pointerId); } catch {}
        el.addEventListener("pointermove", move);
        el.addEventListener("pointerup", end);
        el.addEventListener("pointercancel", end);
        document.body.classList.add("resizing");
    };

    const onKey = (event) => {
        if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
            event.preventDefault();
            const step = (event.shiftKey ? BIG_STEP : STEP) * (event.key === "ArrowRight" ? 1 : -1);
            onCommit(clampWidth(current() + step, min, max), el);
        } else if (event.key === "Enter" && onReset) {
            event.preventDefault();
            onReset(el);
        }
    };

    el = span({
        class: "colgrip",
        tabindex: "0",
        role: "separator",
        "aria-orientation": "vertical",
        "aria-label": `Resize ${label} column`,
        title: onReset ? "Drag to resize. Double-click to fit the content." : "Drag to resize.",
        onclick: (event) => event.stopPropagation(),
        onpointerdown: startDrag,
        onkeydown: onKey,
        ...(onReset ? {ondblclick: (event) => { event.stopPropagation(); onReset(el); }} : {}),
    });
    return el;
}

export function fitColumns({storageKey, prefix, columns}) {
    const byKey = new Map(columns.map((column) => [column.key, column]));
    const load = () => {
        try { return readColumnWidths(localStorage.getItem(storageKey), columns); } catch { return {}; }
    };
    const widths = van.state(load());
    const save = (next) => {
        widths.val = next;
        try { localStorage.setItem(storageKey, JSON.stringify(next)); } catch {}
    };
    const varName = (key) => `--${prefix}-${key}`;
    const tableOf = (el) => el.closest("table");

    return {
        widths,
        tableStyle: () => columnWidthVars(widths.val, prefix),
        cellClass: (key) => byKey.get(key).flex ? "fit-col-flex" : "fit-col",
        cellStyle: (key) => byKey.get(key).flex ? "" : `--w:var(${varName(key)})`,
        grip: (key) => {
            const column = byKey.get(key);
            if (column.flex) return "";
            return columnGrip({
                label: column.label,
                min: column.min,
                max: column.max,
                onPreview: (px, el) => tableOf(el)?.style.setProperty(varName(key), `${px}px`),
                onCommit: (px) => save({...widths.val, [key]: px}),
                onReset: () => { if (key in widths.val) save(withoutColumn(widths.val, key)); },
            });
        },
    };
}
