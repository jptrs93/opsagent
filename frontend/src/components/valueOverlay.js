import van from "vanjs-core";
import {checkIcon, copyIcon} from "../lib/icons.js";

const {button, div, h2, textarea} = van.tags;

export function valueOverlay(name, value, version, onSave, onClose) {
    const copied = van.state(false);
    const copyFailed = van.state(false);
    const saving = van.state(false);
    const saveError = van.state("");
    const currentValue = () => typeof value === "function" ? value() : value;
    const initialValue = currentValue();
    const draft = van.state(initialValue);
    const isDirty = () => draft.val !== initialValue;

    const copyValue = async () => {
        try {
            await navigator.clipboard.writeText(draft.val);
            copied.val = true;
            copyFailed.val = false;
            setTimeout(() => { copied.val = false; }, 1500);
        } catch (_) {
            copyFailed.val = true;
        }
    };

    const save = async () => {
        if (saving.val) return;
        saving.val = true;
        saveError.val = "";
        try {
            await onSave(draft.val);
            onClose();
        } catch (e) {
            saveError.val = e.message || "Could not save value";
        } finally {
            saving.val = false;
        }
    };

    return div(
        div({class: "fixed inset-0 z-40 bg-black/70", onclick: onClose}),
        div(
            {class: "fixed inset-0 z-50 flex items-center justify-center p-3 md:p-6 pointer-events-none", "data-testid": "resource-value-overlay"},
            div(
                {
                    class: "w-full h-full max-w-5xl max-h-[85vh] bg-gray-900 border border-gray-700 rounded-xl shadow-2xl flex flex-col overflow-hidden pointer-events-auto",
                    role: "dialog",
                    "aria-modal": "true",
                    "aria-labelledby": "resource-value-title",
                    onclick: (e) => e.stopPropagation(),
                },
                div(
                    {class: "flex items-center justify-between gap-4 border-b border-gray-700 px-4 py-3"},
                    h2({id: "resource-value-title", class: "min-w-0 truncate font-mono text-sm font-semibold text-gray-100"}, name),
                    div({class: "flex shrink-0 items-center gap-1"},
                        button({
                            type: "button",
                            class: "inline-flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm text-gray-300 hover:bg-gray-800 hover:text-gray-100 cursor-pointer",
                            onclick: copyValue,
                            title: () => copyFailed.val ? "Copy failed" : "Copy value",
                        }, () => copied.val ? checkIcon({class: "w-4 h-4 text-green-400"}) : copyIcon(), () => copied.val ? "Copied" : copyFailed.val ? "Copy failed" : "Copy"),
                        button({type: "button", class: "px-2.5 py-1.5 text-sm text-gray-400 hover:text-gray-100 cursor-pointer", onclick: onClose}, "Close"),
                    ),
                ),
                textarea({
                    class: "w-full flex-1 min-h-0 resize-none bg-gray-950 p-4 font-mono text-sm leading-6 text-gray-200 outline-none",
                    spellcheck: "false",
                    autocomplete: "off",
                    value: draft,
                    disabled: saving,
                    oninput: (e) => draft.val = e.target.value,
                }),
                div(
                    {class: "flex shrink-0 items-center justify-between gap-4 border-t border-gray-700 px-4 py-3"},
                    () => saveError.val ? div({class: "text-sm text-red-400"}, saveError.val) : div(),
                    button({
                        type: "button",
                        disabled: () => saving.val || !isDirty(),
                        class: () => `rounded-lg px-3 py-1.5 text-sm font-medium transition-colors ${saving.val || !isDirty()
                            ? "cursor-not-allowed bg-gray-700 text-gray-400 opacity-50"
                            : "cursor-pointer bg-brand text-white hover:bg-blue-600"}`,
                        onclick: () => { void save(); },
                    }, () => saving.val ? "Saving..." : `Save new version (v${version + 1})`),
                ),
            ),
        ),
    );
}
