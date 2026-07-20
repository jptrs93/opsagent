import van from "vanjs-core";
import {checkIcon, xIcon} from "../lib/icons.js";

const {button, div, input, textarea} = van.tags;

const valueOf = value => typeof value === "function" ? value() : (value?.val ?? value);

export function inlineEditableInput({
    value,
    oninput,
    dirty,
    onSave,
    onDiscard,
    valid = true,
    disabled = false,
    multiline = false,
    inputAttrs = {},
    inputClass = "",
    containerClass = "",
    inputRef,
    placeholder = "",
    ariaLabel = "Editable name",
    saveAriaLabel = "Save name",
    discardAriaLabel = "Discard name change",
}) {
    const saving = van.state(false);
    let inputElement;
    const isDirty = () => Boolean(valueOf(dirty));
    const isDisabled = () => saving.val || Boolean(valueOf(disabled));
    const saveDisabled = () => isDisabled() || !Boolean(valueOf(valid));
    const saveActionDisabled = () => !isDirty() || saveDisabled();
    const discardActionDisabled = () => !isDirty() || isDisabled();

    const save = async () => {
        if (!isDirty() || saveDisabled()) return;
        saving.val = true;
        try {
            await onSave();
        } finally {
            saving.val = false;
            queueMicrotask(() => inputElement?.isConnected && inputElement.focus());
        }
    };
    const discard = () => {
        if (!isDirty() || isDisabled()) return;
        onDiscard();
        queueMicrotask(() => inputElement?.isConnected && inputElement.focus());
    };

    const inputTag = multiline ? textarea : input;
    inputElement = inputTag({
        ...inputAttrs,
        class: `min-w-0 flex-1 ${inputClass}`,
        value,
        placeholder,
        disabled: isDisabled,
        "aria-label": ariaLabel,
        "aria-invalid": () => isDirty() && !Boolean(valueOf(valid)),
        oninput,
        onkeydown: event => {
            if (event.isComposing || !isDirty()) return;
            if (event.key === "Enter" && (!multiline || event.metaKey || event.ctrlKey)) {
                event.preventDefault();
                void save();
            } else if (event.key === "Escape") {
                event.preventDefault();
                discard();
            }
        },
    });
    inputRef?.(inputElement);

    return div(
        {
            class: `flex w-full min-w-0 gap-1 ${multiline ? "items-start" : "items-center"} ${containerClass}`,
            onclick: event => event.stopPropagation(),
        },
        inputElement,
        div(
            {
                class: () => `flex shrink-0 items-center gap-1 ${multiline ? "mt-1" : ""} ${
                    isDirty() ? "" : "invisible pointer-events-none"}`,
                "aria-hidden": () => String(!isDirty()),
            },
            button({
                type: "button",
                title: saveAriaLabel,
                "aria-label": saveAriaLabel,
                disabled: saveActionDisabled,
                class: () => `inline-flex h-7 w-7 items-center justify-center rounded border border-blue-500/40 ` +
                    `bg-blue-500/15 text-blue-300 transition-colors hover:bg-blue-500/25 ${
                        saveActionDisabled() ? "cursor-not-allowed opacity-50" : "cursor-pointer"}`,
                onclick: event => {
                    event.stopPropagation();
                    void save();
                },
            }, checkIcon()),
            button({
                type: "button",
                title: discardAriaLabel,
                "aria-label": discardAriaLabel,
                disabled: discardActionDisabled,
                class: () => `inline-flex h-7 w-7 items-center justify-center rounded border border-gray-600 ` +
                    `bg-gray-800 text-gray-400 transition-colors hover:bg-gray-700 hover:text-gray-100 ${
                        discardActionDisabled() ? "cursor-not-allowed opacity-50" : "cursor-pointer"}`,
                onclick: event => {
                    event.stopPropagation();
                    discard();
                },
            }, xIcon()),
        ),
    );
}
