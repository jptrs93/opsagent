import van from "vanjs-core";
import {basicSetup} from "codemirror";
import {yaml} from "@codemirror/lang-yaml";
import {Compartment, EditorState} from "@codemirror/state";
import {EditorView, keymap} from "@codemirror/view";
import {checkIcon, xIcon} from "../lib/icons.js";

const {button, div} = van.tags;

const codeEditorTheme = EditorView.theme({
    "&": {height: "100%", color: "#f3f4f6", backgroundColor: "#1f2937"},
    ".cm-scroller": {overflow: "auto", scrollbarColor: "#4b5563 #111827", scrollbarWidth: "thin"},
    ".cm-scroller::-webkit-scrollbar": {width: "8px", height: "8px"},
    ".cm-scroller::-webkit-scrollbar-track": {background: "#111827"},
    ".cm-scroller::-webkit-scrollbar-thumb": {
        background: "#4b5563",
        border: "2px solid #111827",
        borderRadius: "999px",
    },
    ".cm-content": {
        minHeight: "100%",
        padding: "8px 0",
        fontFamily: 'ui-monospace, "SFMono-Regular", Menlo, Monaco, Consolas, "Liberation Mono", monospace',
        fontSize: "0.875rem",
        lineHeight: "1.625",
        caretColor: "#93c5fd",
    },
    ".cm-line": {padding: "0 12px"},
    ".cm-gutters": {backgroundColor: "#1f2937", color: "#6b7280", border: "none"},
    ".cm-activeLine": {backgroundColor: "#273449"},
    ".cm-activeLineGutter": {backgroundColor: "#273449", color: "#9ca3af"},
    ".cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection": {
        backgroundColor: "#1e3a5f !important",
    },
    ".cm-cursor": {borderLeftColor: "#93c5fd"},
}, {dark: true});

const stateValue = value => typeof value === "function" ? value() : (value?.val ?? value);

export function assetCodeEditor({
    value,
    dirty,
    valid,
    disabled,
    onSave,
    onDiscard,
    ariaLabel,
    saveAriaLabel,
    discardAriaLabel,
    yamlSyntax = false,
}) {
    const saving = van.state(false);
    const editable = new Compartment();
    let view;

    const isDirty = () => Boolean(stateValue(dirty));
    const isDisabled = () => saving.val || Boolean(stateValue(disabled));
    const saveDisabled = () => isDisabled() || !Boolean(stateValue(valid));
    const saveActionDisabled = () => !isDirty() || saveDisabled();
    const discardActionDisabled = () => !isDirty() || isDisabled();

    const save = async () => {
        if (saveActionDisabled()) return;
        saving.val = true;
        try {
            await onSave();
        } finally {
            saving.val = false;
            queueMicrotask(() => view?.focus());
        }
    };
    const discard = () => {
        if (discardActionDisabled()) return;
        onDiscard();
        queueMicrotask(() => view?.focus());
    };

    const host = div({
        class: "h-full min-h-0 flex-1 overflow-hidden rounded-lg border border-gray-600 focus-within:ring-1 focus-within:ring-brand",
        "aria-label": ariaLabel,
    });

    const createEditor = () => {
        if (!host.isConnected || view) return;
        view = new EditorView({
            parent: host,
            state: EditorState.create({
                doc: stateValue(value) || "",
                extensions: [
                    basicSetup,
                    codeEditorTheme,
                    ...(yamlSyntax ? [yaml()] : []),
                    editable.of(EditorView.editable.of(!isDisabled())),
                    keymap.of([{
                        key: "Mod-Enter",
                        run: () => { void save(); return true; },
                    }, {
                        key: "Escape",
                        run: () => { discard(); return true; },
                    }]),
                    EditorView.updateListener.of(update => {
                        if (update.docChanged) value.val = update.state.doc.toString();
                    }),
                ],
            }),
        });
    };

    requestAnimationFrame(createEditor);
    van.derive(() => {
        const next = stateValue(value) || "";
        if (view && next !== view.state.doc.toString()) {
            view.dispatch({changes: {from: 0, to: view.state.doc.length, insert: next}});
        }
        return "";
    });
    van.derive(() => {
        const nextEditable = !isDisabled();
        if (view) view.dispatch({effects: editable.reconfigure(EditorView.editable.of(nextEditable))});
        return "";
    });

    return div(
        {class: "flex h-full w-full min-w-0 items-start gap-1", onclick: event => event.stopPropagation()},
        host,
        div(
            {
                class: () => `flex shrink-0 items-center gap-1 mt-1 ${isDirty() ? "" : "invisible pointer-events-none"}`,
                "aria-hidden": () => String(!isDirty()),
            },
            button({
                type: "button",
                title: saveAriaLabel,
                "aria-label": saveAriaLabel,
                disabled: saveActionDisabled,
                class: () => `inline-flex h-7 w-7 items-center justify-center rounded border border-blue-500/40 bg-blue-500/15 text-blue-300 transition-colors hover:bg-blue-500/25 ${saveActionDisabled() ? "cursor-not-allowed opacity-50" : "cursor-pointer"}`,
                onclick: event => { event.stopPropagation(); void save(); },
            }, checkIcon()),
            button({
                type: "button",
                title: discardAriaLabel,
                "aria-label": discardAriaLabel,
                disabled: discardActionDisabled,
                class: () => `inline-flex h-7 w-7 items-center justify-center rounded border border-gray-600 bg-gray-800 text-gray-400 transition-colors hover:bg-gray-700 hover:text-gray-100 ${discardActionDisabled() ? "cursor-not-allowed opacity-50" : "cursor-pointer"}`,
                onclick: event => { event.stopPropagation(); discard(); },
            }, xIcon()),
        ),
    );
}
