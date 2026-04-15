import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {spinnerButton} from "./spinnerbutton.js";
import {EditorView, basicSetup} from "codemirror";
import {yaml} from "@codemirror/lang-yaml";
import {oneDark} from "@codemirror/theme-one-dark";
import {EditorState} from "@codemirror/state";
import {Edit2, RefreshCw} from "vanjs-feather";

const { div, h3, span, select, option, button, p } = van.tags;

const versionLabel = (v) => {
    const date = v.time instanceof Date && v.time.getTime() > 0
        ? v.time.toISOString().substring(0, 10)
        : '';
    const shortId = v.id.length > 7 && /^[0-9a-f]+$/i.test(v.id) ? v.id.substring(0, 7) : v.id;
    const label = (v.label || '').substring(0, 30);
    const ellipsis = (v.label || '').length > 30 ? '...' : '';
    return `${date}\t\t${shortId}\t\t${label}${ellipsis}`;
};

// deployment: the view model from mapDeploymentsToView
// deploymentConfig: the raw DeploymentConfig proto
// onClose: callback to close the overlay
// onDeployed: callback after successful deploy
export function deployOverlay(deployment, deploymentConfig, onClose, onDeployed) {
    const editing = van.state(false);
    const scopes = van.state([]);
    const selectedScope = van.state('');
    const versions = van.state([]);
    const selectedVersion = van.state('');
    const loadingVersions = van.state(false);
    const versionError = van.state('');

    // Build initial YAML from the deployment config.
    const yamlContent = configToYaml(deploymentConfig);

    // --- YAML editor ---
    const editorContainer = div({class: "flex-1 min-h-0 overflow-auto border border-gray-700 rounded"});
    let editorView = null;

    const initEditor = (content, readOnly) => {
        if (editorView) editorView.destroy();
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
                    ...(readOnly ? [EditorState.readOnly.of(true)] : []),
                ],
            }),
            parent: editorContainer,
        });
    };

    setTimeout(() => initEditor(yamlContent, true), 0);

    const toggleEdit = () => {
        const isEditing = !editing.val;
        editing.val = isEditing;
        const content = editorView ? editorView.state.doc.toString() : yamlContent;
        editorContainer.innerHTML = '';
        initEditor(content, !isEditing);
    };

    // --- Version loading ---
    const loadVersions = async (scope) => {
        loadingVersions.val = true;
        versionError.val = '';
        try {
            const result = await capi.postV1DeploymentVersions({
                deploymentId: deployment.id,
                scope: scope || '',
            });
            scopes.val = result?.scopes || [];
            const byScope = result?.versionsByScope || {};
            // Find the scope key that has versions.
            const scopeKey = scope || Object.keys(byScope)[0] || '';
            const sv = byScope[scopeKey];
            const vsList = sv?.versions || [];
            versions.val = vsList;
            if (!selectedScope.val && scopeKey) {
                selectedScope.val = scopeKey;
            }
            // Auto-select the currently deployed version if it's in the list.
            const deployedId = deployment.deployedVersion || '';
            if (deployedId && vsList.some(v => v.id === deployedId)) {
                selectedVersion.val = deployedId;
            }
        } catch (e) {
            versionError.val = e.message || 'Failed to load versions';
            versions.val = [];
        }
        loadingVersions.val = false;
    };

    // Auto-load versions if the deployment has a prepare config.
    if (deployment.variant) {
        setTimeout(() => loadVersions(''), 0);
    }

    const onScopeChange = (e) => {
        selectedScope.val = e.target.value;
        loadVersions(e.target.value);
    };

    const onRefresh = () => {
        loadVersions(selectedScope.val);
    };

    // --- Deploy ---
    const doDeploy = async () => {
        const payload = {
            deploymentId: deployment.id,
            version: deployment.currentVersion + 1,
        };

        // If editing was enabled and content changed, include yaml.
        if (editing.val && editorView) {
            const newYaml = editorView.state.doc.toString();
            if (newYaml !== yamlContent) {
                payload.yamlContent = newYaml;
            }
        }

        if (selectedVersion.val) {
            payload.targetVersion = selectedVersion.val;
        }

        await capi.postV1DeploymentUpdate(payload);
        if (onDeployed) onDeployed();
        onClose();
    };

    // --- Layout ---
    const backdrop = div({
        class: "fixed inset-0 bg-black/60 z-40",
        onclick: onClose,
    });

    const hasVersions = deployment.variant;

    const dialog = div(
        {class: "fixed inset-0 z-50 flex items-center justify-center p-8 pointer-events-none"},
        div(
            {class: "bg-gray-900 border border-gray-700 rounded-xl shadow-2xl flex flex-col pointer-events-auto",
             style: "width: 640px; max-height: 80vh;",
             onclick: (e) => e.stopPropagation()},
            // Spec section
            div(
                {class: "flex flex-col min-h-0", style: hasVersions ? "max-height: 50%" : "flex: 1"},
                div(
                    {class: "flex items-center justify-between px-4 py-3 border-b border-gray-700"},
                    h3({class: "text-sm font-semibold text-gray-300"}, "Deployment specification"),
                    button({
                        class: () => `p-1 rounded transition-colors cursor-pointer ${editing.val ? 'text-brand' : 'text-gray-500 hover:text-gray-300'}`,
                        onclick: toggleEdit,
                        title: "Toggle edit",
                    }, Edit2({size: 14})),
                ),
                div({class: "flex-1 min-h-0 overflow-auto p-3"}, editorContainer),
            ),
            // Version section
            hasVersions ? div(
                {class: "flex flex-col border-t border-gray-700 min-h-0", style: "flex: 1"},
                div(
                    {class: "flex items-center justify-between px-4 py-3 border-b border-gray-700"},
                    h3({class: "text-sm font-semibold text-gray-300"}, "Version"),
                    button({
                        class: "flex items-center gap-1.5 px-2 py-1 rounded text-xs text-gray-500 hover:text-gray-300 transition-colors cursor-pointer",
                        onclick: onRefresh,
                    }, RefreshCw({size: 12}), "Refresh available versions"),
                ),
                div(
                    {class: "p-4 flex flex-col gap-3"},
                    // Scope selector
                    () => {
                        const s = scopes.val;
                        if (s.length === 0) return span();
                        return div(
                            {class: "flex gap-2 items-center"},
                            span({class: "text-xs text-gray-400 w-16"}, "Branch"),
                            select(
                                {
                                    class: "flex-1 text-xs font-mono bg-gray-700 text-gray-200 border border-gray-600 rounded px-2 py-1.5 focus:outline-none focus:ring-1 focus:ring-brand",
                                    onchange: onScopeChange,
                                },
                                ...s.map(b => option({value: b, selected: b === selectedScope.val}, b))
                            ),
                        );
                    },
                    // Version selector
                    () => {
                        if (loadingVersions.val) {
                            return p({class: "text-xs text-gray-500"}, "Loading versions...");
                        }
                        if (versionError.val) {
                            return p({class: "text-xs text-red-400"}, versionError.val);
                        }
                        const vs = versions.val;
                        const deployedId = deployment.deployedVersion || '';
                        return div(
                            {class: "flex gap-2 items-center"},
                            span({class: "text-xs text-gray-400 w-16"}, "Version"),
                            select(
                                {
                                    class: "flex-1 text-xs font-mono bg-gray-700 text-gray-200 border border-gray-600 rounded px-2 py-1.5 focus:outline-none focus:ring-1 focus:ring-brand",
                                    onchange: (e) => { selectedVersion.val = e.target.value; },
                                },
                                option({value: '', disabled: true, selected: true}, vs.length ? "Select a version..." : "No versions loaded"),
                                ...vs.map(v => option({value: v.id, selected: v.id === deployedId}, versionLabel(v)))
                            ),
                        );
                    },
                ),
            ) : null,
            // Actions
            div(
                {class: "flex items-center justify-end gap-3 px-4 py-3 border-t border-gray-700"},
                button({
                    class: "text-sm text-gray-400 hover:text-gray-200 cursor-pointer px-3 py-1.5",
                    onclick: onClose,
                }, "Cancel"),
                spinnerButton("Deploy", doDeploy, "btn-primary text-sm py-1.5 px-4"),
            ),
        ),
    );

    return div(backdrop, dialog);
}

// Convert a DeploymentConfig proto to per-deployment YAML string.
function configToYaml(cfg) {
    if (!cfg) return '';
    const obj = {};
    if (cfg.configId) {
        obj.name = cfg.configId.name || '';
        obj.environment = cfg.configId.environment || '';
        obj.machine = cfg.configId.machine || '';
    }
    if (cfg.spec) {
        if (cfg.spec.prepare) {
            obj.prepare = {};
            if (cfg.spec.prepare.nixBuild) {
                const nb = cfg.spec.prepare.nixBuild;
                obj.prepare.nixBuild = {repo: nb.repo};
                if (nb.flake) obj.prepare.nixBuild.flake = nb.flake;
                if (nb.outputExecutable) obj.prepare.nixBuild.outputExecutable = nb.outputExecutable;
            }
            if (cfg.spec.prepare.githubRelease) {
                const gr = cfg.spec.prepare.githubRelease;
                obj.prepare.githubRelease = {repo: gr.repo};
                if (gr.asset) obj.prepare.githubRelease.asset = gr.asset;
                if (gr.tag) obj.prepare.githubRelease.tag = gr.tag;
            }
        }
        if (cfg.spec.runner) {
            obj.runner = {};
            if (cfg.spec.runner.osProcess) {
                const op = cfg.spec.runner.osProcess;
                obj.runner.osProcess = {};
                if (op.workingDir) obj.runner.osProcess.workingDir = op.workingDir;
                if (op.runAs) obj.runner.osProcess.runAs = op.runAs;
                if (op.strategy) obj.runner.osProcess.strategy = op.strategy;
            }
            if (cfg.spec.runner.systemd) {
                const sd = cfg.spec.runner.systemd;
                obj.runner.systemd = {name: sd.name, binPath: sd.binPath};
            }
        }
    }
    return toYaml(obj);
}

// Minimal YAML serializer for simple nested objects (no arrays, no special chars).
function toYaml(obj, indent = 0) {
    const lines = [];
    const pad = '  '.repeat(indent);
    for (const [key, val] of Object.entries(obj)) {
        if (val === undefined || val === null || val === '') continue;
        if (typeof val === 'object' && !Array.isArray(val)) {
            lines.push(`${pad}${key}:`);
            lines.push(toYaml(val, indent + 1));
        } else {
            lines.push(`${pad}${key}: ${val}`);
        }
    }
    return lines.join('\n');
}
