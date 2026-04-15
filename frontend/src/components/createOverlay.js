import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {spinnerButton} from "./spinnerbutton.js";
import {EditorView, basicSetup} from "codemirror";
import {yaml} from "@codemirror/lang-yaml";
import {oneDark} from "@codemirror/theme-one-dark";
import {EditorState} from "@codemirror/state";

const { div, h3, span, button, p } = van.tags;

const templateYaml = `name:
environment:
machine:
prepare:
  nixBuild:
    repo:
    flake:
runner:
  osProcess:
    workingDir:
`;

// onClose: callback to close the overlay
// onCreated: callback after successful creation
export function createOverlay(onClose, onCreated) {
    const errorMsg = van.state('');

    const editorContainer = div({class: "flex-1 min-h-0 overflow-auto border border-gray-700 rounded"});
    let editorView = null;

    const initEditor = () => {
        editorView = new EditorView({
            state: EditorState.create({
                doc: templateYaml,
                extensions: [
                    basicSetup,
                    yaml(),
                    oneDark,
                    EditorView.theme({
                        "&": {height: "100%"},
                        ".cm-scroller": {overflow: "auto"},
                    }),
                ],
            }),
            parent: editorContainer,
        });
    };

    setTimeout(() => initEditor(), 0);

    const doCreate = async () => {
        errorMsg.val = '';
        if (!editorView) return;
        const yamlContent = editorView.state.doc.toString();
        if (!yamlContent.trim()) {
            errorMsg.val = 'YAML content is required';
            throw new Error(errorMsg.val);
        }

        try {
            await capi.postV1DeploymentCreate({yamlContent});
        } catch (e) {
            errorMsg.val = e.message || 'Failed to create deployment';
            throw e;
        }
        if (onCreated) onCreated();
        onClose();
    };

    const backdrop = div({
        class: "fixed inset-0 bg-black/60 z-40",
        onclick: onClose,
    });

    const dialog = div(
        {class: "fixed inset-0 z-50 flex items-center justify-center p-8 pointer-events-none"},
        div(
            {class: "bg-gray-900 border border-gray-700 rounded-xl shadow-2xl flex flex-col pointer-events-auto",
             style: "width: 640px; max-height: 80vh;",
             onclick: (e) => e.stopPropagation()},
            // Spec section
            div(
                {class: "flex flex-col min-h-0 flex-1"},
                div(
                    {class: "flex items-center justify-between px-4 py-3 border-b border-gray-700"},
                    h3({class: "text-sm font-semibold text-gray-300"}, "Deployment specification"),
                ),
                div({class: "flex-1 min-h-0 overflow-auto p-3"}, editorContainer),
            ),
            // Error message
            () => {
                if (!errorMsg.val) return span();
                return div(
                    {class: "px-4"},
                    p({class: "text-xs text-red-400"}, errorMsg.val),
                );
            },
            // Actions
            div(
                {class: "flex items-center justify-end gap-3 px-4 py-3 border-t border-gray-700"},
                button({
                    class: "text-sm text-gray-400 hover:text-gray-200 cursor-pointer px-3 py-1.5",
                    onclick: onClose,
                }, "Cancel"),
                spinnerButton("Create", doCreate, "btn-primary text-sm py-1.5 px-4"),
            ),
        ),
    );

    return div(backdrop, dialog);
}
