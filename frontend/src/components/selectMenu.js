import van from "vanjs-core";

const {button, input} = van.tags;

const EDGE = 8;
const GAP = 6;
const MIN_HEIGHT_BELOW = 260;

export function anchoredMenu({width, onOpen, onClose}) {
    const anchor = van.state(null);
    let root = null;
    let trigger = null;

    const onOutsidePointer = (event) => {
        if (!root.contains(event.target)) close();
    };
    const onResize = () => close();

    const open = () => {
        const rect = trigger.getBoundingClientRect();
        const left = Math.max(EDGE, Math.min(rect.left, window.innerWidth - width - EDGE));
        const below = window.innerHeight - rect.bottom - GAP - EDGE;
        const above = rect.top - GAP - EDGE;
        anchor.val = below >= MIN_HEIGHT_BELOW || below >= above
            ? {left, top: rect.bottom + GAP, maxHeight: below}
            : {left, bottom: window.innerHeight - rect.top + GAP, maxHeight: above};
        document.addEventListener("pointerdown", onOutsidePointer, true);
        window.addEventListener("resize", onResize);
        onOpen?.();
    };

    function close({refocus = false} = {}) {
        if (!anchor.val) return;
        anchor.val = null;
        document.removeEventListener("pointerdown", onOutsidePointer, true);
        window.removeEventListener("resize", onResize);
        onClose?.();
        if (refocus) trigger.focus();
    }

    return {
        anchor,
        isOpen: () => anchor.val !== null,
        open,
        close,
        attach: (rootEl, triggerEl) => {
            root = rootEl;
            trigger = triggerEl;
        },
        style: (at) => {
            const vertical = at.top !== undefined ? `top:${at.top}px` : `bottom:${at.bottom}px`;
            return `left:${at.left}px;${vertical};max-height:${at.maxHeight}px`;
        },
    };
}

export const menuTrigger = (menu, attrs, ...children) => button({
    type: "button",
    "aria-haspopup": "true",
    "aria-expanded": () => String(menu.isOpen()),
    ...attrs,
    onclick: () => { menu.isOpen() ? menu.close() : menu.open(); },
    onkeydown: (event) => {
        if (event.key !== "ArrowDown" || menu.isOpen()) return;
        event.preventDefault();
        menu.open();
    },
}, ...children);

export const menuSearch = ({testid, plural, query, onEnter}) => input({
    type: "text",
    role: "searchbox",
    inputMode: "search",
    autocomplete: "off",
    spellcheck: false,
    "data-nav": "",
    "data-testid": `${testid}-search`,
    placeholder: `Search ${plural}`,
    "aria-label": `Search ${plural}`,
    oninput: (event) => { query.val = event.target.value; },
    onkeydown: (event) => {
        if (event.key !== "Enter") return;
        event.preventDefault();
        onEnter();
    },
});

export const roveFocus = (container, delta) => {
    const targets = [...(container?.querySelectorAll("[data-nav]") || [])];
    if (targets.length === 0) return;
    const at = targets.indexOf(document.activeElement);
    targets[at < 0 ? 0 : Math.max(0, Math.min(targets.length - 1, at + delta))].focus();
};

export const activateOnKey = (run) => (event) => {
    if (event.target !== event.currentTarget) return;
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    run();
};

export const tailOf = (item) => typeof item.tail === "function" ? item.tail() : item.tail;

export const capitalised = (text) => text.charAt(0).toUpperCase() + text.slice(1);
