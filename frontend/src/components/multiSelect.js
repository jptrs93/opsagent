import van from "vanjs-core";
import {checkIcon, chevronDownIcon, closeIcon, plusIcon, searchIcon} from "../lib/icons.js";
import {
    cleanPresetName, findPresetByName, matchItems, matchingPreset, presetSelection, sameSelection,
} from "../lib/selectionPresets.js";

const {button, div, form, input, span} = van.tags;

const POPOVER_WIDTH = 288;
const EDGE = 8;
const GAP = 6;
const MIN_HEIGHT_BELOW = 260;
const MAX_LEADS = 6;

const capitalised = (text) => text.charAt(0).toUpperCase() + text.slice(1);

const activateOnKey = (run) => (event) => {
    if (event.target !== event.currentTarget) return;
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    run();
};

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
    buttonClass = "multiselect-trigger",
    labelClass = "",
    testid = "multiselect",
}) {
    const [singular, plural] = noun;
    const anchor = van.state(null);
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

    const onOutsidePointer = (event) => {
        if (!root.contains(event.target)) close();
    };
    const onResize = () => close();

    const open = () => {
        const rect = trigger.getBoundingClientRect();
        const left = Math.max(EDGE, Math.min(rect.left, window.innerWidth - POPOVER_WIDTH - EDGE));
        const below = window.innerHeight - rect.bottom - GAP - EDGE;
        const above = rect.top - GAP - EDGE;
        anchor.val = below >= MIN_HEIGHT_BELOW || below >= above
            ? {left, top: rect.bottom + GAP, maxHeight: below}
            : {left, bottom: window.innerHeight - rect.top + GAP, maxHeight: above};
        document.addEventListener("pointerdown", onOutsidePointer, true);
        window.addEventListener("resize", onResize);
        setTimeout(() => searchEl?.focus(), 0);
    };

    function close({refocus = false} = {}) {
        if (!anchor.val) return;
        anchor.val = null;
        query.val = "";
        stopNaming();
        document.removeEventListener("pointerdown", onOutsidePointer, true);
        window.removeEventListener("resize", onResize);
        if (refocus) trigger.focus();
    }

    const moveFocus = (delta) => {
        const targets = [...(popoverEl?.querySelectorAll("[data-nav]") || [])];
        if (targets.length === 0) return;
        const at = targets.indexOf(document.activeElement);
        targets[at < 0 ? 0 : Math.max(0, Math.min(targets.length - 1, at + delta))].focus();
    };

    const onPopoverKey = (event) => {
        if (event.key === "Escape") {
            event.preventDefault();
            event.stopPropagation();
            if (naming.val) {
                stopNaming();
                searchEl?.focus();
            } else {
                close({refocus: true});
            }
            return;
        }
        if (event.target.dataset?.naming !== undefined) return;
        if (event.key === "ArrowDown") { event.preventDefault(); moveFocus(1); }
        if (event.key === "ArrowUp") { event.preventDefault(); moveFocus(-1); }
    };

    const tick = (on) => span({class: () => `multiselect-check ${on() ? "" : "invisible"}`}, checkIcon({class: "h-3.5 w-3.5"}));

    const itemRow = (item) => div(
        {
            role: "menuitemcheckbox",
            "aria-checked": () => String(isSelected(item.id)),
            tabindex: 0,
            "data-nav": "",
            "data-testid": `${testid}-row-${item.id}`,
            class: "menu-item multiselect-row",
            onclick: () => toggle(item.id),
            onkeydown: activateOnKey(() => toggle(item.id)),
        },
        tick(() => isSelected(item.id)),
        item.lead ? item.lead() : "",
        span({class: `min-w-0 flex-1 truncate ${labelClass}`}, item.label),
        item.tail ? span({class: "multiselect-tail"}, item.tail) : "",
        button({
            type: "button",
            tabindex: -1,
            class: "multiselect-act",
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
                class: "menu-item multiselect-row",
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
            span({class: "multiselect-tail"}, String(size)),
            button({
                type: "button",
                tabindex: -1,
                class: "multiselect-act",
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
        return div({role: "menu", "aria-label": "Presets", class: "multiselect-section"},
            div({class: "multiselect-label"}, "Presets"),
            ...rows.map(presetRow));
    };

    const itemsSection = () => {
        const rows = shownItems();
        return div({role: "menu", "aria-label": capitalised(plural)},
            presetList().length > 0 ? div({class: "multiselect-label"}, capitalised(plural)) : "",
            ...rows.map(itemRow),
            rows.length === 0
                ? div({class: "multiselect-empty"}, items().length === 0 ? `No ${plural}` : `No ${plural} match`)
                : "");
    };

    const link = (label, onclick, key) => button({
        type: "button",
        "data-nav": "",
        "data-testid": `${testid}-${key}`,
        class: "multiselect-link",
        onclick,
    }, label);

    const actions = () => div({class: "multiselect-actions"},
        link(() => searching() ? "Select matching" : "Select all", selectShown, "all"),
        link(() => searching() ? "Clear matching" : "Clear", clearShown, "clear"),
        span({class: "flex-1"}),
        () => dirty() ? link("Reset to default", reset, "reset") : "");

    const saveRow = () => {
        if (!presets) return "";
        if (naming.val) {
            return form(
                {
                    class: "multiselect-save",
                    onsubmit: (event) => {
                        event.preventDefault();
                        savePreset();
                    },
                },
                input({
                    type: "text",
                    class: "multiselect-name",
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
                    class: "multiselect-primary",
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
            class: "menu-item multiselect-row",
            onclick: () => {
                naming.val = true;
                setTimeout(() => popoverEl?.querySelector("[data-naming]")?.focus(), 0);
            },
        }, plusIcon({class: "h-3.5 w-3.5 flex-none text-gray-400"}), "Save selection as preset…");
    };

    const popover = () => {
        const at = anchor.val;
        if (!at) return "";
        searchEl = input({
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
                const first = shownItems()[0];
                if (first) toggle(first.id);
            },
        });
        const vertical = at.top !== undefined ? `top:${at.top}px` : `bottom:${at.bottom}px`;
        popoverEl = div(
            {
                class: "multiselect",
                "data-testid": `${testid}-menu`,
                style: `left:${at.left}px;${vertical};max-height:${at.maxHeight}px`,
                onkeydown: onPopoverKey,
            },
            div({class: "multiselect-search"}, searchIcon({class: "h-3.5 w-3.5 flex-none"}), searchEl),
            div({class: "multiselect-list app-scroll"}, presetsSection, itemsSection),
            div({class: "multiselect-foot"}, actions(), saveRow),
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

    const trigger = button({
        type: "button",
        "data-testid": testid,
        "aria-haspopup": "true",
        "aria-expanded": () => String(anchor.val !== null),
        "aria-label": ariaLabel || `Select ${plural}`,
        class: buttonClass,
        onclick: () => { anchor.val ? close() : open(); },
        onkeydown: (event) => {
            if (event.key !== "ArrowDown" || anchor.val) return;
            event.preventDefault();
            open();
        },
    }, summary);

    const root = span({class: "relative inline-flex"}, trigger, popover);
    return root;
}
