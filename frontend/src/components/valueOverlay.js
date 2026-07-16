import van from "vanjs-core";
import {checkIcon, copyIcon} from "../lib/icons.js";

const {button, div, h2, textarea} = van.tags;

export function valueOverlay(name, value, onClose) {
    const copied = van.state(false);
    const copyFailed = van.state(false);
    const currentValue = () => typeof value === "function" ? value() : value;

    const copyValue = async () => {
        try {
            await navigator.clipboard.writeText(currentValue());
            copied.val = true;
            copyFailed.val = false;
            setTimeout(() => { copied.val = false; }, 1500);
        } catch (_) {
            copyFailed.val = true;
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
                    readOnly: true,
                    spellcheck: "false",
                    value: currentValue,
                }),
            ),
        ),
    );
}
