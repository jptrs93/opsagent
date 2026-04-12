import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {configHistory} from "../components/configHistory.js";
import {userConfigS, deploymentsStreamS} from "../state/deployments.js";
import {EditorView, basicSetup} from "codemirror";
import {yaml} from "@codemirror/lang-yaml";
import {oneDark} from "@codemirror/theme-one-dark";
import {EditorState} from "@codemirror/state";

const { div, h1, p } = van.tags;

const defaultYaml = `# Example config:
# environments:
#   - name: PROD
#     deployments:
#       - name: hello-world
#         machine: localhost
#         prepare:
#           nixBuild:
#             repo: github.com/acme/hello-world
#             flake: flake.nix
#         runner:
#           osProcess:
#             workingDir: /var/lib/hello-world
`;

export function configPage() {
    const status = van.state('');
    const showHistory = van.state(false);
    const historyVersions = van.state([]);

    const editorContainer = div({class: "flex-1 min-h-0 overflow-auto border border-gray-700 rounded-lg"});
    const offlineMessage = p({class: "flex-1 text-gray-400"}, () => deploymentsStreamS.val.sentence);

    let editorView = null;
    let lastSyncedYaml = '';

    const userYaml = () => userConfigS.val?.yamlContent || '';
    const isOffline = () => userConfigS.val === null && deploymentsStreamS.val.status !== 'connected';
    const resolveYaml = () => userYaml() || defaultYaml;

    const initEditor = (content) => {
        if (editorView) {
            editorView.destroy();
        }
        editorView = new EditorView({
            state: EditorState.create({
                doc: content,
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
        lastSyncedYaml = content;
    };

    const replaceEditorContent = (content) => {
        if (!editorView) {
            initEditor(content);
            return;
        }
        editorView.dispatch({
            changes: {from: 0, to: editorView.state.doc.length, insert: content}
        });
        lastSyncedYaml = content;
    };

    setTimeout(async () => {
        initEditor(isOffline() ? '' : resolveYaml());
        try {
            const history = await capi.getV1ConfigHistory({});
            historyVersions.val = history?.versions || [];
        } catch (e) {}
    }, 0);

    van.derive(() => {
        const offline = isOffline();
        deploymentsStreamS.val;
        editorContainer.style.display = offline ? 'none' : '';
        offlineMessage.style.display = offline ? '' : 'none';
        if (!editorView || offline) {
            return;
        }
        const nextYaml = resolveYaml();
        const currentYaml = editorView.state.doc.toString();
        if (currentYaml === lastSyncedYaml && nextYaml !== lastSyncedYaml) {
            replaceEditorContent(nextYaml);
        }
    });

    const saveButton = spinnerButton("Save", async () => {
        status.val = '';
        try {
            const content = editorView.state.doc.toString();
            const result = await capi.putV1Config({yamlContent: content});
            // result is UserConfigVersion; update local state.
            userConfigS.val = result;
            lastSyncedYaml = result?.yamlContent || content;
            const userVersion = result?.version || 0;
            status.val = p({class: "text-green-400 text-sm"}, `Saved as v${userVersion}`);
            const history = await capi.getV1ConfigHistory({});
            historyVersions.val = history?.versions || [];
        } catch (e) {
            status.val = p({class: "text-red-400 text-sm"}, `${e.message}`);
        }
    }, "btn-primary", 'button');

    const historyToggle = spinnerButton(
        () => showHistory.val ? "Hide history" : "Show history",
        () => { showHistory.val = !showHistory.val; },
        "btn-secondary",
        'button'
    );

    const onRestoreVersion = (version) => {
        if (version?.yamlContent) {
            replaceEditorContent(version.yamlContent);
        }
    };

    return div(
        {class: "flex h-dvh"},
        div(
            {class: "flex-1 min-h-0 flex flex-col p-6 gap-4"},
            div(
                {class: "flex items-center justify-between"},
                h1({class: "text-xl font-bold"}, "Deployment Config"),
                div({class: "flex gap-2 items-center"}, status, saveButton, historyToggle),
            ),
            editorContainer,
            offlineMessage,
        ),
        () => showHistory.val
            ? configHistory(historyVersions, onRestoreVersion)
            : div()
    );
}
