import van from "vanjs-core";
import {checkIcon, copyIcon} from "../lib/icons.js";

const {button, div, h2, p, span} = van.tags;

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

const formatDate = (value) => {
    if (!(value instanceof Date) || Number.isNaN(value.getTime())) return "-";
    return `${String(value.getDate()).padStart(2, "0")}/${String(value.getMonth() + 1).padStart(2, "0")}/${value.getFullYear()}`;
};

export function valueOverlay({name, type, value, version, createdAt, referenceCount, deploymentCount, onSave, onClose}) {
    const copied = van.state(false);
    const copyFailed = van.state(false);
    const saving = van.state(false);
    const saveError = van.state("");
    const updateReferencedDeployments = van.state(false);
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
                    div({class: "flex min-w-0 items-baseline gap-3"},
                        h2({
                            id: "resource-value-title",
                            class: `min-w-0 truncate font-mono text-sm font-semibold ${type === "secret" ? "text-purple-300" : "text-blue-300"}`,
                        }, name),
                        p({class: "shrink-0 text-xs font-normal text-orange-300"},
                            `Version ${version} - ${formatDate(createdAt)}. ${referenceCount} references.`),
                    ),
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
                lazyCodeEditor({
                    value: draft,
                    disabled: saving,
                    ariaLabel: `Value for ${name}`,
                }),
                div(
                    {class: "flex shrink-0 items-center justify-between gap-4 border-t border-gray-700 px-4 py-3"},
                    div({class: "flex min-w-0 flex-col gap-1.5"},
                        button({
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
                    ),
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
