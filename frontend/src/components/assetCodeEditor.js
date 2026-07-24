import van from "vanjs-core";
import {basicSetup} from "codemirror";
import {yaml} from "@codemirror/lang-yaml";
import {HighlightStyle, syntaxHighlighting} from "@codemirror/language";
import {Compartment, EditorState} from "@codemirror/state";
import {EditorView} from "@codemirror/view";
import {tags} from "@lezer/highlight";

const {div} = van.tags;

const codeEditorTheme = EditorView.theme({
    "&": {height: "100%", color: "#f3f4f6", backgroundColor: "#111827"},
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
    ".cm-gutters": {backgroundColor: "#111827", color: "#6b7280", border: "none"},
    ".cm-activeLine": {backgroundColor: "transparent"},
    ".cm-activeLineGutter": {backgroundColor: "transparent", color: "inherit"},
    ".cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection": {
        backgroundColor: "#1e3a5f !important",
    },
    ".cm-cursor": {borderLeftColor: "#93c5fd"},
}, {dark: true});

const yamlHighlightStyle = HighlightStyle.define([
    {tag: [tags.propertyName, tags.labelName], color: "#7dd3fc"},
    {tag: [tags.string, tags.special(tags.string)], color: "#86efac"},
    {tag: [tags.number, tags.bool], color: "#c4b5fd"},
    {tag: [tags.null, tags.tagName, tags.meta], color: "#fbbf24"},
    {tag: tags.comment, color: "#6b7280", fontStyle: "italic"},
    {tag: [tags.punctuation, tags.contentSeparator], color: "#9ca3af"},
    {tag: tags.invalid, color: "#f87171"},
]);

const stateValue = value => typeof value === "function" ? value() : (value?.val ?? value);

export const createEditorValueBridge = value => {
    let applyingExternalValue = false;
    return {
        applyExternalValue: dispatch => {
            applyingExternalValue = true;
            try {
                dispatch();
            } finally {
                applyingExternalValue = false;
            }
        },
        updateFromEditor: next => {
            if (!applyingExternalValue) value.val = next;
        },
    };
};

export function assetCodeEditor({
    value,
    disabled,
    ariaLabel,
    yamlSyntax = false,
    bare = false,
}) {
    const editable = new Compartment();
    const valueBridge = createEditorValueBridge(value);
    let view;

    const isDisabled = () => Boolean(stateValue(disabled));

    const host = div({
        class: `h-full min-h-0 flex-1 overflow-hidden ${bare
            ? "" : "rounded-lg border border-gray-600 focus-within:ring-1 focus-within:ring-brand"}`,
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
                    ...(yamlSyntax ? [yaml(), syntaxHighlighting(yamlHighlightStyle)] : []),
                    editable.of(EditorView.editable.of(!isDisabled())),
                    EditorView.updateListener.of(update => {
                        if (update.docChanged) valueBridge.updateFromEditor(update.state.doc.toString());
                    }),
                ],
            }),
        });
    };

    requestAnimationFrame(createEditor);
    const syncValue = () => {
        const next = stateValue(value) || "";
        if (view && next !== view.state.doc.toString()) {
            valueBridge.applyExternalValue(() => {
                view.dispatch({changes: {from: 0, to: view.state.doc.length, insert: next}});
            });
        }
        return "";
    };
    const syncEditable = () => {
        const nextEditable = !isDisabled();
        if (view) view.dispatch({effects: editable.reconfigure(EditorView.editable.of(nextEditable))});
        return "";
    };

    return div(
        {class: "h-full w-full min-w-0", onclick: event => event.stopPropagation()},
        host,
        syncValue,
        syncEditable,
    );
}
