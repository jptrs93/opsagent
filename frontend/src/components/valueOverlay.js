import van from "vanjs-core";
import {checkIcon, closeIcon, copyIcon} from "../lib/icons.js";
import {secretGenerator} from "./secretGenerator.js";

const {button, div, h2, input, p, span} = van.tags;

let codeEditorLoader;

const loadCodeEditor = () => {
    codeEditorLoader ||= import("./assetCodeEditor.js")
        .then(module => module.assetCodeEditor);
    return codeEditorLoader;
};

function lazyCodeEditor(args) {
    const editor = van.state("");
    const loadError = van.state("");
    loadCodeEditor()
        .then(codeEditor => { editor.val = codeEditor(args); })
        .catch(error => { loadError.val = error.message || "Unable to load editor"; });

    return div(
        {class: "flex-1 min-h-0"},
        () => editor.val || p({class: "p-4 text-sm text-red-400"}, () => loadError.val || "Loading editor..."),
    );
}

const formatDate = (value) => value instanceof Date && !Number.isNaN(value.getTime()) ? value.toLocaleString() : "";

const normalizeEditorValue = (value) => value.replace(/\r\n?/g, "\n");

export function valueOverlay({name = "", type, value = "", version = 0, createdAt, deploymentCount = 0, mode = "edit", onSave, onClose}) {
    const creating = mode === "create";
    const copied = van.state(false);
    const copyFailed = van.state(false);
    const saving = van.state(false);
    const saveError = van.state("");
    const updateReferencedDeployments = van.state(false);
    const currentValue = () => typeof value === "function" ? value() : value;
    const initialValue = currentValue();
    const draft = van.state(initialValue);
    const nameDraft = van.state(name);
    const isDirty = () => normalizeEditorValue(draft.val) !== normalizeEditorValue(initialValue);
    const isEmpty = () => !draft.val;
    const saveDisabled = () => saving.val || isEmpty() || (creating ? !nameDraft.val.trim() : !isDirty());

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
        const resourceName = creating ? nameDraft.val.trim() : name;
        if (!resourceName) {
            saveError.val = `${type === "secret" ? "Secret" : "Config"} name is required`;
            return;
        }
        if (isEmpty()) {
            saveError.val = `${type === "secret" ? "Secret" : "Config"} cannot be empty.`;
            return;
        }
        saving.val = true;
        saveError.val = "";
        try {
            await onSave(draft.val, resourceName);
            onClose();
        } catch (e) {
            saveError.val = e.message || "Could not save value";
        } finally {
            saving.val = false;
        }
    };

    const discardChanges = () => {
        draft.val = initialValue;
        saveError.val = "";
    };

    return div(
        div({class: "fixed inset-0 z-40 bg-black/70", onclick: onClose}),
        div(
            {
                class: "fixed inset-0 z-50 flex items-center justify-center p-3 md:p-6 pointer-events-none",
                "data-testid": creating ? `create-${type}-overlay` : "resource-value-overlay",
            },
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
                    creating
                        ? h2({id: "resource-value-title", class: "text-base font-semibold"}, `Add ${type}`)
                        : h2({
                            id: "resource-value-title",
                            class: `min-w-0 truncate font-mono text-sm font-normal ${type === "secret" ? "text-purple-300" : "text-blue-300"}`,
                        }, name),
                    div({class: "flex shrink-0 items-center gap-3"},
                        creating ? "" : p({class: "text-xs text-gray-400 whitespace-nowrap"}, `Version ${version} created ${formatDate(createdAt)}.`),
                        div({class: "flex items-center gap-1"},
                            creating ? "" : button({
                                type: "button",
                                class: "inline-flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm text-gray-300 hover:bg-gray-800 hover:text-gray-100 cursor-pointer",
                                onclick: copyValue,
                                title: () => copyFailed.val ? "Copy failed" : "Copy value",
                            }, () => copied.val ? checkIcon({class: "w-4 h-4 text-green-400"}) : copyIcon(), () => copied.val ? "Copied" : copyFailed.val ? "Copy failed" : "Copy"),
                            button({
                                type: "button",
                                title: "Close editor",
                                "aria-label": "Close editor",
                                class: "p-1.5 rounded text-gray-400 hover:text-gray-100 hover:bg-surface transition-colors cursor-pointer",
                                onclick: onClose,
                            }, closeIcon()),
                        ),
                    ),
                ),
                creating ? div(
                    {class: "flex shrink-0 items-center gap-3 border-b border-gray-700 px-4 py-2"},
                    p({class: "text-xs font-medium text-gray-400"}, "Name"),
                    input({
                        class: "text-input min-w-0 flex-1 py-1 font-mono text-sm",
                        placeholder: `${type} name`,
                        autocomplete: "off",
                        value: nameDraft,
                        disabled: saving,
                        oninput: event => nameDraft.val = event.target.value,
                        "aria-label": `${type === "secret" ? "Secret" : "Config"} name`,
                    }),
                ) : "",
                type === "secret" ? secretGenerator({
                    onGenerate: generated => draft.val = generated,
                    disabled: saving,
                    className: "shrink-0 border-b border-gray-700 bg-gray-950/30 px-4 py-2",
                }) : "",
                lazyCodeEditor({
                    value: draft,
                    disabled: saving,
                    ariaLabel: creating ? `Value for new ${type}` : `Value for ${name}`,
                    bare: true,
                }),
                div(
                    {class: "flex shrink-0 items-center justify-between gap-4 border-t border-gray-700 px-4 py-3"},
                    div({class: "flex min-w-0 flex-col gap-1.5"},
                        creating ? "" : button({
                            type: "button",
                            role: "switch",
                            "aria-checked": () => String(updateReferencedDeployments.val),
                            "aria-label": `Update ${deploymentCount} referenced deployments`,
                            class: "inline-flex w-fit items-center gap-2 text-xs text-gray-300 cursor-pointer",
                            onclick: () => updateReferencedDeployments.val = !updateReferencedDeployments.val,
                        },
                        span({
                            class: () => `relative h-4 w-7 shrink-0 rounded-full transition-colors ${updateReferencedDeployments.val
                                ? "bg-brand" : "bg-gray-700"}`,
                        }, span({
                            class: () => `absolute top-0.5 h-3 w-3 rounded-full bg-white shadow-sm transition-all ${updateReferencedDeployments.val
                                ? "left-3.5" : "left-0.5"}`,
                        })),
                        `Update ${deploymentCount} referenced deployment${deploymentCount === 1 ? "" : "s"}.`),
                        () => saveError.val ? p({class: "text-sm text-red-400"}, saveError.val) : "",
                        () => isEmpty() ? p({class: "text-xs text-orange-300"},
                            `${type === "secret" ? "Secret" : "Config"} cannot be empty.`) : "",
                    ),
                    div({class: "flex shrink-0 items-center gap-2"},
                        creating ? button({
                            type: "button",
                            disabled: saving,
                            class: () => `rounded-lg px-3 py-1.5 text-sm font-medium transition-colors ${saving.val
                                ? "cursor-not-allowed bg-gray-700 text-gray-400 opacity-50"
                                : "cursor-pointer bg-gray-700 text-gray-200 hover:bg-gray-600"}`,
                            onclick: onClose,
                        }, "Cancel") : () => isDirty() ? button({
                            type: "button",
                            disabled: saving,
                            class: () => `rounded-lg px-3 py-1.5 text-sm font-medium transition-colors ${saving.val
                                ? "cursor-not-allowed bg-gray-700 text-gray-400 opacity-50"
                                : "cursor-pointer bg-gray-700 text-gray-200 hover:bg-gray-600"}`,
                            onclick: discardChanges,
                        }, "Discard changes") : "",
                        button({
                            type: "button",
                            disabled: saveDisabled,
                            class: () => `rounded-lg px-3 py-1.5 text-sm font-medium transition-colors ${saveDisabled()
                                ? "cursor-not-allowed bg-gray-700 text-gray-400 opacity-50"
                                : "cursor-pointer bg-brand text-white hover:bg-blue-600"}`,
                            onclick: () => { void save(); },
                        }, () => saving.val ? "Saving..." : creating ? `Add ${type}` : `Save version ${version + 1}`),
                    ),
                ),
            ),
        ),
    );
}
