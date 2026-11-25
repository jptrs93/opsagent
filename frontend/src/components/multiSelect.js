import van from "vanjs-core";
import {checkIcon, chevronDownIcon, closeIcon, plusIcon, searchIcon} from "../lib/icons.js";
import {
    cleanPresetName, findPresetByName, matchItems, matchingPreset, presetSelection, sameSelection,
} from "../lib/selectionPresets.js";
import {activateOnKey, anchoredMenu, capitalised, menuSearch, menuTrigger, roveFocus, tailOf} from "./selectMenu.js";

const {button, div, form, input, span} = van.tags;

const POPOVER_WIDTH = 288;
const MAX_LEADS = 6;

// Rows are divs, not buttons: Firefox does not deliver a click to a button
// nested inside a button, and each row carries one ("only", delete).
//
// A row's tick and aria-checked are their own bindings, so a toggle never
// rebuilds the list and keyboard focus stays on the row.
export function multiSelect({
    items,
    value,
    onChange,
    noun = ["item", "items"],
    ariaLabel,
    defaultValue,
    onReset,
    presets,
    buttonClass = "select-menu-trigger",
    labelClass = "",
    testid = "multiselect",
}) {
    const [singular, plural] = noun;
    const query = van.state("");
    const naming = van.state(false);
    const draftName = van.state("");
    let popoverEl = null;
    let searchEl = null;

    const knownIds = () => new Set(items().map((item) => item.id));
    const selectedItems = () => {
        const chosen = value();
        return items().filter((item) => chosen.has(item.id));
    };
    const selection = () => new Set(selectedItems().map((item) => item.id));
    const isSelected = (id) => value().has(id);
    const presetList = () => presets ? presets.list.val : [];
    const activePreset = () => matchingPreset(presetList(), selection(), knownIds());
    const shownItems = () => matchItems(items(), query.val);
    const shownPresets = () => matchItems(presetList().map((preset) => ({...preset, label: preset.name})), query.val);
    const searching = () => query.val.trim() !== "";
    const dirty = () => {
        if (!defaultValue) return false;
        const known = knownIds();
        return !sameSelection(selection(), new Set([...defaultValue()].filter((id) => known.has(id))));
    };

    const commit = (next) => onChange(new Set(next));

    const toggle = (id) => {
        const next = selection();
        next.has(id) ? next.delete(id) : next.add(id);
        commit(next);
    };

    const selectShown = () => commit([...selection(), ...shownItems().map((item) => item.id)]);

    const clearShown = () => {
        const next = selection();
        for (const item of shownItems()) next.delete(item.id);
        commit(next);
    };

    const reset = () => {
        if (onReset) { onReset(); return; }
        const known = knownIds();
        commit([...defaultValue()].filter((id) => known.has(id)));
    };

    const applyPreset = (preset) => {
        const members = presetSelection(preset, knownIds());
        if (members.size > 0) commit(members);
    };

    const stopNaming = () => {
        naming.val = false;
        draftName.val = "";
    };

    const savePreset = () => {
        const chosen = [...selection()];
        if (!cleanPresetName(draftName.val) || chosen.length === 0) return;
        presets.save(draftName.val, chosen);
        stopNaming();
        searchEl?.focus();
    };

    const menu = anchoredMenu({
        width: POPOVER_WIDTH,
        onOpen: () => setTimeout(() => searchEl?.focus(), 0),
        onClose: () => {
            query.val = "";
            stopNaming();
        },
    });

    const onPopoverKey = (event) => {
        if (event.key === "Escape") {
            event.preventDefault();
            event.stopPropagation();
            if (naming.val) {
                stopNaming();
                searchEl?.focus();
            } else {
                menu.close({refocus: true});
            }
            return;
        }
        if (event.target.dataset?.naming !== undefined) return;
        if (event.key === "ArrowDown") { event.preventDefault(); roveFocus(popoverEl, 1); }
        if (event.key === "ArrowUp") { event.preventDefault(); roveFocus(popoverEl, -1); }
    };

    const tick = (on) => span({class: () => `select-menu-check ${on() ? "" : "invisible"}`}, checkIcon({class: "h-3.5 w-3.5"}));

    const itemRow = (item) => div(
        {
            role: "menuitemcheckbox",
            "aria-checked": () => String(isSelected(item.id)),
            tabindex: 0,
            "data-nav": "",
            "data-testid": `${testid}-row-${item.id}`,
            class: "menu-item select-menu-row",
            onclick: () => toggle(item.id),
            onkeydown: activateOnKey(() => toggle(item.id)),
        },
        tick(() => isSelected(item.id)),
        item.lead ? item.lead() : "",
        span({class: `min-w-0 flex-1 truncate ${labelClass}`}, item.label),
        item.tail ? span({class: "select-menu-tail"}, tailOf(item)) : "",
        button({
            type: "button",
            tabindex: -1,
            class: "select-menu-act",
            title: `Select only ${item.label}`,
            "data-testid": `${testid}-only-${item.id}`,
            onclick: (event) => {
                event.stopPropagation();
                commit([item.id]);
            },
        }, "only"),
    );

    const presetRow = (preset) => {
        const size = presetSelection(preset, knownIds()).size;
        const isActive = () => activePreset()?.id === preset.id;
        const remove = () => {
            presets.remove(preset.id);
            searchEl?.focus();
        };
        return div(
            {
                role: "menuitemradio",
                "aria-checked": () => String(isActive()),
                "aria-disabled": String(size === 0),
                tabindex: 0,
                "data-nav": "",
                "data-testid": `${testid}-preset`,
                "data-preset": preset.name,
                class: "menu-item select-menu-row",
                title: size === 0 ? `None of its ${plural} exist any more` : "",
                onclick: () => applyPreset(preset),
                onkeydown: (event) => {
                    if (event.target !== event.currentTarget) return;
                    if (event.key === "Delete" || event.key === "Backspace") {
                        event.preventDefault();
                        remove();
                        return;
                    }
                    activateOnKey(() => applyPreset(preset))(event);
                },
            },
            tick(isActive),
            span({class: "min-w-0 flex-1 truncate"}, preset.name),
            span({class: "select-menu-tail"}, String(size)),
            button({
                type: "button",
                tabindex: -1,
                class: "select-menu-act",
                title: `Delete preset ${preset.name}`,
                "aria-label": `Delete preset ${preset.name}`,
                "data-testid": `${testid}-preset-delete`,
                onclick: (event) => {
                    event.stopPropagation();
                    remove();
                },
            }, closeIcon({class: "h-3 w-3"})),
        );
    };

    const presetsSection = () => {
        const rows = shownPresets();
        if (rows.length === 0) return "";
        return div({role: "menu", "aria-label": "Presets", class: "select-menu-section"},
            div({class: "select-menu-label"}, "Presets"),
            ...rows.map(presetRow));
    };

    const itemsSection = () => {
        const rows = shownItems();
        return div({role: "menu", "aria-label": capitalised(plural)},
            presetList().length > 0 ? div({class: "select-menu-label"}, capitalised(plural)) : "",
            ...rows.map(itemRow),
            rows.length === 0
                ? div({class: "select-menu-empty"}, items().length === 0 ? `No ${plural}` : `No ${plural} match`)
                : "");
    };

    const link = (label, onclick, key) => button({
        type: "button",
        "data-nav": "",
        "data-testid": `${testid}-${key}`,
        class: "select-menu-link",
        onclick,
    }, label);

    const actions = () => div({class: "select-menu-actions"},
        link(() => searching() ? "Select matching" : "Select all", selectShown, "all"),
        link(() => searching() ? "Clear matching" : "Clear", clearShown, "clear"),
        span({class: "flex-1"}),
        () => dirty() ? link("Reset to default", reset, "reset") : "");

    const saveRow = () => {
        if (!presets) return "";
        if (naming.val) {
            return form(
                {
                    class: "select-menu-save",
                    onsubmit: (event) => {
                        event.preventDefault();
                        savePreset();
                    },
                },
                input({
                    type: "text",
                    class: "select-menu-name",
                    placeholder: "Preset name",
                    maxLength: 40,
                    autocomplete: "off",
                    spellcheck: false,
                    "aria-label": "Preset name",
                    "data-naming": "",
                    "data-testid": `${testid}-preset-name`,
                    oninput: (event) => { draftName.val = event.target.value; },
                }),
                button({
                    type: "submit",
                    class: "select-menu-primary",
                    "data-testid": `${testid}-preset-save`,
                    disabled: () => !cleanPresetName(draftName.val),
                }, () => findPresetByName(presetList(), draftName.val) ? "Replace" : "Save"),
            );
        }
        if (selection().size === 0 || activePreset()) return "";
        return button({
            type: "button",
            "data-nav": "",
            "data-testid": `${testid}-preset-add`,
            class: "menu-item select-menu-row",
            onclick: () => {
                naming.val = true;
                setTimeout(() => popoverEl?.querySelector("[data-naming]")?.focus(), 0);
            },
        }, plusIcon({class: "h-3.5 w-3.5 flex-none text-gray-400"}), "Save selection as preset…");
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
                if (first) toggle(first.id);
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
            div({class: "select-menu-list app-scroll"}, presetsSection, itemsSection),
            div({class: "select-menu-foot"}, actions(), saveRow),
        );
        return popoverEl;
    };

    const summary = () => {
        const chosen = selectedItems();
        const preset = activePreset();
        return span({class: "inline-flex items-center gap-1.5 whitespace-nowrap"},
            chosen.some((item) => item.lead)
                ? span({class: "inline-flex items-center gap-1"}, ...chosen.slice(0, MAX_LEADS).map((item) => item.lead ? item.lead() : ""))
                : "",
            preset
                ? span({class: "max-w-40 truncate"}, preset.name)
                : `${chosen.length} ${chosen.length === 1 ? singular : plural}`,
            chevronDownIcon({class: "h-3 w-3 opacity-60"}));
    };

    const trigger = menuTrigger(menu, {
        "data-testid": testid,
        "aria-label": ariaLabel || `Select ${plural}`,
        class: buttonClass,
    }, summary);

    const root = span({class: "relative inline-flex"}, trigger, popover);
    menu.attach(root, trigger);
    return root;
}
