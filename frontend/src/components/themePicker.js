import van from "vanjs-core";
import {THEMES, setTheme, themeS} from "../state/theme.js";
import {checkIcon, cogIcon} from "../lib/icons.js";

const {button, div, span} = van.tags;

const swatch = (theme) => span({
    class: "h-4 w-6 flex-none rounded-[3px] border border-white/15",
    style: `background: linear-gradient(90deg, ${theme.frame} 0 50%, ${theme.surface} 50% 100%)`,
});

// The menu is position: fixed and placed from the button's rectangle, because
// the sidebar clips its overflow and an absolute menu would be cut at its edge.
export function themePicker() {
    const anchor = van.state(null);

    const close = () => { anchor.val = null; };

    const trigger = button({
        type: "button",
        "data-testid": "theme-button",
        title: "Theme",
        "aria-label": "Theme",
        "aria-haspopup": "menu",
        "aria-expanded": () => String(anchor.val !== null),
        class: "icon-button",
        onclick: () => {
            if (anchor.val) { close(); return; }
            const rect = trigger.getBoundingClientRect();
            anchor.val = {left: rect.left, bottom: window.innerHeight - rect.top + 6};
        },
        onkeydown: (event) => { if (event.key === "Escape") close(); },
    }, cogIcon({class: "h-4 w-4"}));

    const option = (theme) => button({
        type: "button",
        role: "menuitemradio",
        "aria-checked": () => String(themeS.val === theme.key),
        "data-testid": `theme-option-${theme.key}`,
        class: "menu-item w-full",
        onclick: () => { setTheme(theme.key); close(); },
    },
    swatch(theme),
    span({class: "flex-1"}, theme.label),
    () => themeS.val === theme.key ? checkIcon({class: "h-3.5 w-3.5 text-brand"}) : span({class: "h-3.5 w-3.5"}));

    const menu = () => {
        const at = anchor.val;
        if (!at) return "";
        return div(
            div({class: "fixed inset-0 z-[80]", onclick: close}),
            div(
                {
                    role: "menu",
                    "aria-label": "Theme",
                    "data-testid": "theme-menu",
                    class: "menu fixed z-[81] w-44",
                    style: `left:${at.left}px;bottom:${at.bottom}px`,
                    onkeydown: (event) => { if (event.key === "Escape") close(); },
                },
                div({class: "px-2 pb-1 pt-1 text-[10.5px] font-semibold uppercase tracking-wider text-gray-500"}, "Theme"),
                ...THEMES.map(option),
            ),
        );
    };

    return div({class: "flex flex-none"}, trigger, menu);
}

export const themeCorner = () => div({class: "fixed bottom-2 left-2 z-30"}, themePicker());
