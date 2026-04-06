import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {configHistory} from "../components/configHistory.js";
import {clusterConfigS, deploymentsStreamS, setClusterConfig} from "../state/deployments.js";
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
    const showSystem = van.state(false);
    const historyVersions = van.state([]);

    const editorContainer = div({class: "flex-1 min-h-0 overflow-auto border border-gray-700 rounded-lg"});
    const systemEditorContainer = div({class: "flex-1 min-h-0 overflow-auto border border-gray-700 rounded-lg"});
    const offlineMessage = p({class: "flex-1 text-gray-400"}, () => deploymentsStreamS.val.sentence);

    let editorView = null;
    let systemEditorView = null;
    let lastSyncedYaml = '';
    let lastSyncedSystemYaml = '';

    const userYaml = () => clusterConfigS.val?.userConfig?.yamlContent || '';
    const systemYaml = () => clusterConfigS.val?.systemConfig?.yamlContent || '';
    const isOffline = () => clusterConfigS.val === null && deploymentsStreamS.val.status !== 'connected';
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

    const initSystemEditor = (content) => {
        if (systemEditorView) {
            systemEditorView.destroy();
        }
        systemEditorView = new EditorView({
            state: EditorState.create({
                doc: content,
                extensions: [
                    basicSetup,
                    yaml(),
                    oneDark,
                    EditorView.editable.of(false),
                    EditorView.theme({
                        "&": {height: "100%"},
                        ".cm-scroller": {overflow: "auto"},
                    }),
                ],
            }),
            parent: systemEditorContainer,
        });
        lastSyncedSystemYaml = content;
    };

    const replaceSystemEditorContent = (content) => {
        if (!systemEditorView) {
            initSystemEditor(content);
            return;
        }
        systemEditorView.dispatch({
            changes: {from: 0, to: systemEditorView.state.doc.length, insert: content}
        });
        lastSyncedSystemYaml = content;
    };

    setTimeout(async () => {
        initEditor(isOffline() ? '' : resolveYaml());
        initSystemEditor(systemYaml());
        try {
            const history = await capi.getV1ConfigHistory({});
            historyVersions.val = history?.versions || [];
        } catch (e) {}
    }, 0);

    van.derive(() => {
        const offline = isOffline();
        const system = showSystem.val;
        // Access the state so this derive re-runs on sentence changes too.
        deploymentsStreamS.val;
        editorContainer.style.display = offline || system ? 'none' : '';
        systemEditorContainer.style.display = offline || !system ? 'none' : '';
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

    van.derive(() => {
        const nextSystemYaml = systemYaml();
        if (!systemEditorView) return;
        if (nextSystemYaml !== lastSyncedSystemYaml) {
            replaceSystemEditorContent(nextSystemYaml);
        }
    });

    const saveButton = spinnerButton("Save", async () => {
        status.val = '';
        try {
            const content = editorView.state.doc.toString();
            const result = await capi.putV1Config({yamlContent: content});
            // result is the updated ClusterConfig; mirror it into state so
            // the UI stays in sync without waiting for the next stream tick.
            setClusterConfig(result);
            lastSyncedYaml = result?.userConfig?.yamlContent || content;
            const userVersion = result?.userConfig?.version || 0;
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

    const systemToggle = spinnerButton(
        () => showSystem.val ? "Show main config" : "Show system config",
        () => { showSystem.val = !showSystem.val; },
        "btn-secondary",
        'button'
    );

    const onRestoreVersion = (version) => {
        if (version?.yamlContent) {
            replaceEditorContent(version.yamlContent);
        }
    };

    van.derive(() => {
        saveButton.style.display = showSystem.val ? 'none' : '';
    });

    return div(
        {class: "flex h-dvh"},
        div(
            {class: "flex-1 min-h-0 flex flex-col p-6 gap-4"},
            div(
                {class: "flex items-center justify-between"},
                h1({class: "text-xl font-bold"}, () => showSystem.val ? "System Deployment Config (read-only)" : "Deployment Config"),
                div({class: "flex gap-2 items-center"}, status, saveButton, systemToggle, historyToggle),
            ),
            editorContainer,
            systemEditorContainer,
            offlineMessage,
        ),
        () => showHistory.val
            ? configHistory(historyVersions, onRestoreVersion)
            : div()
    );
}
