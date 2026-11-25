import van from "vanjs-core";
import {checkIcon, chevronDownIcon, searchIcon} from "../lib/icons.js";
import {matchItems} from "../lib/selectionPresets.js";
import {activateOnKey, anchoredMenu, capitalised, menuSearch, menuTrigger, roveFocus, tailOf} from "./selectMenu.js";

const {div, span} = van.tags;

const POPOVER_WIDTH = 288;

export function singleSelect({
    items,
    value,
    onChange,
    noun = ["item", "items"],
    placeholder,
    ariaLabel,
    buttonClass = "select-menu-trigger",
    labelClass = "",
    testid = "select",
}) {
    const [singular, plural] = noun;
    const query = van.state("");
    let popoverEl = null;
    let searchEl = null;

    const menu = anchoredMenu({
        width: POPOVER_WIDTH,
        onOpen: () => setTimeout(() => {
            searchEl?.focus();
            popoverEl?.querySelector('[aria-checked="true"]')?.scrollIntoView({block: "nearest"});
        }, 0),
        onClose: () => { query.val = ""; },
    });

    const selected = () => items().find((item) => item.id === value()) || null;
    const shownItems = () => matchItems(items(), query.val);

    const choose = (id) => {
        const changed = id !== value();
        menu.close({refocus: true});
        if (changed) onChange(id);
    };

    const named = (item) => span({class: "min-w-0 flex-1 truncate text-left"},
        span({class: labelClass}, item.label),
        item.detail ? " " : "",
        item.detail ? span({class: "select-menu-detail"}, item.detail) : "");

    const itemRow = (item, current) => div(
        {
            role: "menuitemradio",
            "aria-checked": String(item.id === current),
            tabindex: 0,
            "data-nav": "",
            "data-testid": `${testid}-row-${item.id}`,
            class: "menu-item select-menu-row",
            onclick: () => choose(item.id),
            onkeydown: activateOnKey(() => choose(item.id)),
        },
        span({class: `select-menu-check ${item.id === current ? "" : "invisible"}`}, checkIcon({class: "h-3.5 w-3.5"})),
        item.lead ? item.lead() : "",
        named(item),
        item.tail ? span({class: "select-menu-tail"}, tailOf(item)) : "",
    );

    const list = () => {
        const rows = shownItems();
        const current = value();
        return div({role: "menu", "aria-label": capitalised(plural)},
            ...rows.map((item) => itemRow(item, current)),
            rows.length === 0
                ? div({class: "select-menu-empty"}, items().length === 0 ? `No ${plural}` : `No ${plural} match`)
                : "");
    };

    const onPopoverKey = (event) => {
        if (event.key === "Escape") {
            event.preventDefault();
            event.stopPropagation();
            menu.close({refocus: true});
            return;
        }
        if (event.key === "ArrowDown") { event.preventDefault(); roveFocus(popoverEl, 1); }
        if (event.key === "ArrowUp") { event.preventDefault(); roveFocus(popoverEl, -1); }
    };

    const popover = () => {
        const at = menu.anchor.val;
        if (!at) return "";
        searchEl = menuSearch({
            testid,
            plural,
            query,
            onEnter: () => {
                const first = shownItems()[0];
                if (first) choose(first.id);
            },
        });
        popoverEl = div(
            {
                class: "select-menu",
                "data-testid": `${testid}-menu`,
                style: menu.style(at),
                onkeydown: onPopoverKey,
            },
            div({class: "select-menu-search"}, searchIcon({class: "h-3.5 w-3.5 flex-none"}), searchEl),
            div({class: "select-menu-list app-scroll"}, list),
        );
        return popoverEl;
    };

    const name = ariaLabel || capitalised(singular);

    const summary = () => {
        const item = selected();
        return span({class: "flex min-w-0 flex-1 items-center gap-1.5 whitespace-nowrap"},
            item && item.lead ? item.lead() : "",
            item ? named(item) : span({class: "select-menu-placeholder"}, placeholder || `Select ${singular}`),
            item && item.tail ? span({class: "select-menu-tail"}, tailOf(item)) : "",
            chevronDownIcon({class: "h-3 w-3 flex-none opacity-60"}));
    };

    const trigger = menuTrigger(menu, {
        "data-testid": testid,
        "data-value": () => String(selected()?.id ?? ""),
        "aria-label": () => {
            const item = selected();
            return item ? `${name}: ${[item.label, item.detail].filter(Boolean).join(" ")}` : name;
        },
        class: buttonClass,
    }, summary);

    const root = span({class: "relative inline-flex min-w-0"}, trigger, popover);
    menu.attach(root, trigger);
    return root;
}
