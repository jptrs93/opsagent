import van from "vanjs-core";
import {checkIcon, closeIcon, copyIcon} from "../lib/icons.js";

const {button, div, h2, input, p, span} = van.tags;
const RANDOM_SECRET_CHARS = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*()-_=+[]{}";

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

export function valueOverlay({name, type, value, version, createdAt, deploymentCount, onSave, onClose}) {
    const copied = van.state(false);
    const copyFailed = van.state(false);
    const saving = van.state(false);
    const saveError = van.state("");
    const updateReferencedDeployments = van.state(false);
    const generatedSecretLength = van.state("32");
    const currentValue = () => typeof value === "function" ? value() : value;
    const initialValue = currentValue();
    const draft = van.state(initialValue);
    const isDirty = () => normalizeEditorValue(draft.val) !== normalizeEditorValue(initialValue);

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

    const discardChanges = () => {
        draft.val = initialValue;
        saveError.val = "";
    };

    const generateSecret = () => {
        const length = Math.max(1, Math.min(4096, Number.parseInt(generatedSecretLength.val, 10) || 32));
        generatedSecretLength.val = String(length);
        const bytes = new Uint8Array(length);
        const limit = 256 - (256 % RANDOM_SECRET_CHARS.length);
        let result = "";
        while (result.length < length) {
            globalThis.crypto.getRandomValues(bytes);
            for (const byte of bytes) {
                if (byte < limit) result += RANDOM_SECRET_CHARS[byte % RANDOM_SECRET_CHARS.length];
                if (result.length === length) break;
            }
        }
        draft.val = result;
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
                    h2({
                        id: "resource-value-title",
                        class: `min-w-0 truncate font-mono text-sm font-normal ${type === "secret" ? "text-purple-300" : "text-blue-300"}`,
                    }, name),
                    div({class: "flex shrink-0 items-center gap-3"},
                        p({class: "text-xs text-gray-400 whitespace-nowrap"}, `Version ${version} created ${formatDate(createdAt)}.`),
                        div({class: "flex items-center gap-1"},
                            button({
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
                type === "secret" ? div(
                    {class: "flex shrink-0 flex-wrap items-center gap-3 border-b border-gray-700 bg-gray-950/30 px-4 py-2"},
                    p({class: "text-xs text-gray-400"}, "Generate random value"),
                    input({
                        class: "text-input w-20 py-1 text-xs font-mono",
                        type: "number",
                        min: "1",
                        max: "4096",
                        value: generatedSecretLength,
                        disabled: saving,
                        oninput: (e) => generatedSecretLength.val = e.target.value,
                        "aria-label": "Generated secret length",
                    }),
                    button({
                        type: "button",
                        disabled: saving,
                        class: () => `rounded-md px-2.5 py-1 text-xs font-medium transition-colors ${saving.val
                            ? "cursor-not-allowed bg-gray-700 text-gray-400 opacity-50"
                            : "cursor-pointer bg-gray-700 text-gray-200 hover:bg-gray-600"}`,
                        onclick: generateSecret,
                    }, "Generate"),
                ) : "",
                lazyCodeEditor({
                    value: draft,
                    disabled: saving,
                    ariaLabel: `Value for ${name}`,
                    bare: true,
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
                    div({class: "flex shrink-0 items-center gap-2"},
                        () => isDirty() ? button({
                            type: "button",
                            disabled: saving,
                            class: () => `rounded-lg px-3 py-1.5 text-sm font-medium transition-colors ${saving.val
                                ? "cursor-not-allowed bg-gray-700 text-gray-400 opacity-50"
                                : "cursor-pointer bg-gray-700 text-gray-200 hover:bg-gray-600"}`,
                            onclick: discardChanges,
                        }, "Discard changes") : "",
                        button({
                            type: "button",
                            disabled: () => saving.val || !isDirty(),
                            class: () => `rounded-lg px-3 py-1.5 text-sm font-medium transition-colors ${saving.val || !isDirty()
                                ? "cursor-not-allowed bg-gray-700 text-gray-400 opacity-50"
                                : "cursor-pointer bg-brand text-white hover:bg-blue-600"}`,
                            onclick: () => { void save(); },
                        }, () => saving.val ? "Saving..." : `Save version ${version + 1}`),
                    ),
                ),
            ),
        ),
    );
}
