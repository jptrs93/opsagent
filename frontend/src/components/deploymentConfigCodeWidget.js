import van from "vanjs-core";
import {autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap} from "@codemirror/autocomplete";
import {defaultKeymap, history, historyKeymap, indentWithTab} from "@codemirror/commands";
import {
    bracketMatching,
    defaultHighlightStyle,
    foldGutter,
    foldKeymap,
    indentOnInput,
    syntaxHighlighting,
    syntaxTree,
} from "@codemirror/language";
import {lintGutter, linter} from "@codemirror/lint";
import {EditorState, Transaction} from "@codemirror/state";
import {
    Decoration,
    drawSelection,
    EditorView,
    highlightActiveLine,
    highlightActiveLineGutter,
    keymap,
    lineNumbers,
    ViewPlugin,
} from "@codemirror/view";
import {hcl} from "codemirror-lang-hcl";
import {
    deploymentDocumentToHcl,
    deploymentHclCompletionOptions,
    parseDeploymentHcl,
} from "./deploymentHcl.js";

const {button, div, span} = van.tags;

const schemaProperties = [
    "name", "space", "node", "image", "repo", "flake", "target", "user", "command",
    "working_dir", "data_mount_path", "mounts", "dev_shm_size_kb", "file_descriptor_limit", "strategy",
    "readiness_timeout_seconds", "version", "mode", "ingress", "desired_running",
    "read_only", "executable", "host_port",
].map(label => ({label, type: "property"}));

const completionOptions = [...deploymentHclCompletionOptions, ...schemaProperties];

const editorTheme = EditorView.theme({
    "&": {
        height: "100%",
        minHeight: "0",
        display: "flex",
        flexDirection: "column",
        overflow: "hidden",
        color: "#e5e7eb",
        backgroundColor: "#111827",
    },
    ".cm-content": {
        fontFamily: 'ui-monospace, "SFMono-Regular", Menlo, Monaco, Consolas, "Liberation Mono", monospace',
        fontSize: "13px",
        lineHeight: "1.65",
        caretColor: "#93c5fd",
        padding: "12px 0 48px",
    },
    ".cm-line": {padding: "0 10px"},
    ".cm-scroller": {
        flex: "1 1 auto",
        minHeight: "0",
        overflowX: "auto",
        overflowY: "auto",
        scrollbarColor: "#374151 transparent",
    },
    ".cm-gutters": {
        backgroundColor: "#111827",
        color: "#4b5563",
        border: "none",
        paddingLeft: "3px",
    },
    ".cm-activeLine": {backgroundColor: "#172033"},
    ".cm-activeLineGutter": {backgroundColor: "#172033", color: "#9ca3af"},
    ".cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection": {
        backgroundColor: "#1e3a5f !important",
    },
    ".cm-cursor, .cm-dropCursor": {borderLeftColor: "#93c5fd"},
    ".cm-foldGutter span": {color: "#6b7280"},
    ".cm-tooltip": {
        backgroundColor: "#1f2937",
        border: "1px solid #4b5563",
        color: "#e5e7eb",
    },
    ".cm-tooltip-autocomplete > ul > li[aria-selected]": {
        backgroundColor: "#1d4ed8",
        color: "#ffffff",
    },
    ".cm-lintRange-error": {backgroundImage: "none", borderBottom: "2px solid #f87171"},
    ".cm-lintRange-warning": {backgroundImage: "none", borderBottom: "2px solid #fbbf24"},
    ".cm-reference-function": {color: "#c4b5fd !important", fontWeight: "500"},
    ".cm-reference-symbol": {color: "#fde68a !important"},
}, {dark: true});

function stateValue(value) {
    let current = value;
    const seen = new Set();
    while (current && typeof current === "object" && !Array.isArray(current)
        && "val" in current && !seen.has(current)) {
        seen.add(current);
        current = current.val;
    }
    return current;
}

function catalogArrays(catalogs) {
    const source = stateValue(catalogs) || {};
    const list = name => {
        const value = stateValue(source[name]);
        return Array.isArray(value) ? value.filter(Boolean) : [];
    };
    return {
        spaces: list("spaces"),
        nodes: list("nodes"),
        assets: list("assets"),
        secretRefs: list("secretRefs"),
        configRefs: list("configRefs"),
        deployments: list("deployments"),
    };
}

function deploymentConfig(item) {
    return item?.config || item;
}

function catalogName(item, namespace) {
    if (namespace === "asset") return item?.key;
    if (namespace === "deployment") return deploymentConfig(item)?.identity?.name;
    return item?.name;
}

function catalogID(item, namespace) {
    return namespace === "deployment" ? deploymentConfig(item)?.id : item?.id;
}

function catalogSpaceID(item, namespace) {
    return namespace === "deployment"
        ? deploymentConfig(item)?.identity?.spaceId
        : item?.spaceId;
}

function selectedSpaceID(text, refs) {
    const match = /\bspace\(\s*("(?:\\.|[^"\\])*")\s*\)/.exec(text);
    if (!match) return null;
    let name;
    try {
        name = JSON.parse(match[1]);
    } catch {
        return null;
    }
    return refs.spaces.find(item => !item?.deleted && item?.name === name)?.id ?? null;
}

function selectedNodeID(text, refs) {
    const match = /\bnode\s*=\s*node\(\s*("(?:\\.|[^"\\])*")\s*\)/.exec(text);
    if (!match) return null;
    let name;
    try {
        name = JSON.parse(match[1]);
    } catch {
        return null;
    }
    return refs.nodes.find(item => !item?.deleted && item?.name === name)?.id ?? null;
}

function catalogCompletionOptions(namespace, catalogs, text, insideQuotes, selectedSpaceOverride) {
    const refs = catalogArrays(catalogs);
    const type = namespace === "address" ? "deployment" : namespace;
    const collection = type === "space" ? refs.spaces
        : type === "node" ? refs.nodes
            : type === "asset" ? refs.assets
                : type === "secret" ? refs.secretRefs
                    : type === "config" ? refs.configRefs
                        : refs.deployments;
    if (selectedSpaceOverride === null) return [];
    const spaceID = selectedSpaceOverride !== undefined ? selectedSpaceOverride : selectedSpaceID(text, refs);
    const nodeID = type === "deployment" ? selectedNodeID(text, refs) : null;
    const options = new Map();

    for (const item of collection) {
        if (item?.deleted || deploymentConfig(item)?.deleted) continue;
        const itemSpaceID = catalogSpaceID(item, type);
        if (type === "deployment" && spaceID !== null
            && itemSpaceID !== undefined && itemSpaceID !== null
            && Number(itemSpaceID) !== Number(spaceID)) continue;
        if (type === "deployment" && nodeID !== null
            && Number(deploymentConfig(item)?.nodeId) !== Number(nodeID)) continue;
        const name = catalogName(item, type);
        if (!name) continue;
        const quoted = JSON.stringify(String(name));
        options.set(String(name), {
            label: String(name),
            apply: insideQuotes ? quoted.slice(1, -1) : quoted,
            type: "variable",
            detail: `ID ${catalogID(item, type) ?? "unknown"}`,
        });
    }

    return [...options.values()].sort((left, right) => left.label.localeCompare(right.label));
}

function catalogVersionCompletionOptions(namespace, catalogs, text, name) {
    const refs = catalogArrays(catalogs);
    const collection = namespace === "asset" ? refs.assets
        : namespace === "secret" ? refs.secretRefs : refs.configRefs;
    const versions = new Map();
    for (const item of collection) {
        if (item?.deleted || catalogName(item, namespace) !== name) continue;
        const version = Number(item?.version || 0);
        if (version > 0) versions.set(version, {
            label: String(version),
            type: "constant",
            detail: `ID ${catalogID(item, namespace) ?? "unknown"}`,
        });
    }
    return [...versions.values()].sort((left, right) => Number(right.label) - Number(left.label));
}

function schemaCompletion(catalogs) {
    return context => {
        const prefixStart = Math.max(0, context.pos - 160);
        const prefix = context.state.sliceDoc(prefixStart, context.pos);
        const versionReference = /(secret|config|asset)\(\s*"((?:\\.|[^"\\])*)"\s*,\s*\{\s*version\s*=\s*([0-9]*)$/.exec(prefix);
        if (versionReference) {
            let name;
            try {
                name = JSON.parse(`"${versionReference[2]}"`);
            } catch {
                return null;
            }
            return {
                from: context.pos - versionReference[3].length,
                options: catalogVersionCompletionOptions(
                    versionReference[1],
                    catalogs,
                    context.state.doc.toString(),
                    name,
                ),
                validFor: /^[0-9]*$/,
            };
        }

        const deploymentName = /(address|deployment)\(\s*"((?:\\.|[^"\\])*)"\s*,\s*(?:"([^"\n]*)|([A-Za-z0-9_.-]*))$/.exec(prefix);
        if (deploymentName) {
            const refs = catalogArrays(catalogs);
            let spaceName = deploymentName[2];
            try {
                spaceName = JSON.parse(`"${spaceName}"`);
            } catch {
                return null;
            }
            const spaceID = refs.spaces.find(item => !item?.deleted && item?.name === spaceName)?.id ?? null;
            const partial = deploymentName[3] ?? deploymentName[4] ?? "";
            const insideQuotes = deploymentName[3] !== undefined;
            return {
                from: context.pos - partial.length,
                options: catalogCompletionOptions("deployment", catalogs, context.state.doc.toString(), insideQuotes, spaceID),
                validFor: insideQuotes ? /^[^"\n]*$/ : /^[A-Za-z0-9_.-]*$/,
            };
        }

        const deploymentSpace = /(address|deployment)\(\s*(?:"([^"\n]*)|([A-Za-z0-9_.-]*))$/.exec(prefix);
        if (deploymentSpace) {
            const partial = deploymentSpace[2] ?? deploymentSpace[3] ?? "";
            const insideQuotes = deploymentSpace[2] !== undefined;
            return {
                from: context.pos - partial.length,
                options: catalogCompletionOptions("space", catalogs, context.state.doc.toString(), insideQuotes),
                validFor: insideQuotes ? /^[^"\n]*$/ : /^[A-Za-z0-9_.-]*$/,
            };
        }

        const reference = /(secret|config|asset|address|deployment|space|node)\(\s*(?:"([^"\n]*)|([A-Za-z0-9_.-]*))$/.exec(prefix);
        if (reference) {
            const partial = reference[2] ?? reference[3] ?? "";
            const insideQuotes = reference[2] !== undefined;
            return {
                from: context.pos - partial.length,
                options: catalogCompletionOptions(
                    reference[1],
                    catalogs,
                    context.state.doc.toString(),
                    insideQuotes,
                ),
                validFor: insideQuotes ? /^[^"\n]*$/ : /^[A-Za-z0-9_.-]*$/,
            };
        }

        const protocol = /port_forward\(\s*(?:"([^"\n]*)|([A-Za-z0-9_-]*))$/.exec(prefix);
        if (protocol) {
            const partial = protocol[1] ?? protocol[2] ?? "";
            const insideQuotes = protocol[1] !== undefined;
            return {
                from: context.pos - partial.length,
                options: ["tcp", "udp"].map(value => ({
                    label: value,
                    apply: insideQuotes ? value : JSON.stringify(value),
                    type: "enum",
                })),
                validFor: insideQuotes ? /^[^"\n]*$/ : /^[A-Za-z0-9_-]*$/,
            };
        }

        const enumValue = /(?:^|\n)\s*(mode|strategy)\s*=\s*(?:"([^"\n]*)|([A-Za-z0-9_-]*))$/.exec(prefix);
        if (enumValue) {
            const values = enumValue[1] === "mode" ? ["virtual", "host"] : ["recreate", "rollover"];
            const partial = enumValue[2] ?? enumValue[3] ?? "";
            const insideQuotes = enumValue[2] !== undefined;
            return {
                from: context.pos - partial.length,
                options: values.map(value => ({
                    label: value,
                    apply: insideQuotes ? value : JSON.stringify(value),
                    type: "enum",
                })),
                validFor: insideQuotes ? /^[^"\n]*$/ : /^[A-Za-z0-9_-]*$/,
            };
        }

        const booleanValue = /(?:^|\n)\s*desired_running\s*=\s*([A-Za-z]*)$/.exec(prefix);
        if (booleanValue) {
            return {
                from: context.pos - booleanValue[1].length,
                options: ["true", "false"].map(label => ({label, type: "constant"})),
                validFor: /^[A-Za-z]*$/,
            };
        }

        const word = context.matchBefore(/[A-Za-z_]\w*/);
        if (!context.explicit && (!word || word.from === word.to)) return null;
        return {
            from: word ? word.from : context.pos,
            options: completionOptions,
            validFor: /^[A-Za-z_]\w*$/,
        };
    };
}

function referenceDecorations(view) {
    const decorations = [];
    for (const range of view.visibleRanges) {
        const text = view.state.sliceDoc(range.from, range.to);
        const functionPattern = /\b(?:secret|config|asset|address|deployment|space|node|mount|default_volume|host_path|port_forward|tls_passthrough)(?=\s*\()/g;
        const symbolPattern = /\b(?:secret|config|asset|address|deployment|space|node)\(\s*("(?:\\.|[^"\\])*")/g;
        let match;
        while ((match = functionPattern.exec(text))) {
            const from = range.from + match.index;
            decorations.push(Decoration.mark({class: "cm-reference-function"}).range(from, from + match[0].length));
        }
        while ((match = symbolPattern.exec(text))) {
            const callFrom = range.from + match.index;
            const symbol = match[1];
            const symbolOffset = match[0].indexOf(symbol);
            decorations.push(Decoration.mark({class: "cm-reference-symbol"})
                .range(callFrom + symbolOffset, callFrom + symbolOffset + symbol.length));
        }
    }
    decorations.sort((left, right) => left.from - right.from || left.to - right.to);
    return Decoration.set(decorations, true);
}

const referenceHighlighting = ViewPlugin.fromClass(class {
    constructor(view) {
        this.decorations = referenceDecorations(view);
    }

    update(update) {
        if (update.docChanged || update.viewportChanged) {
            this.decorations = referenceDecorations(update.view);
        }
    }
}, {decorations: plugin => plugin.decorations});

function nativeReferenceRanges(text) {
    const ranges = [];
    const patterns = [
        /\b(?:secret|config|asset)\(\s*"(?:\\.|[^"\\\n])+"\s*,\s*\{\s*version\s*=\s*\d+\s*\}\s*\)/g,
        /\b(?:address|deployment)\(\s*"(?:\\.|[^"\\\n])+"\s*,\s*"(?:\\.|[^"\\\n])+"\s*,?\s*\)/g,
    ];
    for (const pattern of patterns) {
        let match;
        while ((match = pattern.exec(text))) ranges.push({from: match.index, to: match.index + match[0].length});
    }
    return ranges;
}

export function syntaxDiagnostics(state) {
    const diagnostics = [];
    const seen = new Set();
    const nativeReferences = nativeReferenceRanges(state.doc.toString());
    const cursor = syntaxTree(state).cursor();
    do {
        if (!cursor.type.isError) continue;
        if (nativeReferences.some(range => cursor.from >= range.from && cursor.to <= range.to)) continue;
        const before = state.sliceDoc(Math.max(0, cursor.from - 120), cursor.from);
        const functionObjectBoundary = cursor.from === cursor.to
            && /(?:(?:secret|config|asset)\(\s*"[^"\n]+"(?:\s*,\s*\{\s*version\s*=\s*\d+\s*\})?\s*\)|(?:address|deployment)\(\s*"[^"\n]+"\s*,\s*"[^"\n]+"\s*\)|(?:space|node)\(\s*"[^"\n]+"\s*\)),?\s*$/.test(before);
        if (functionObjectBoundary) continue;
        const from = Math.max(0, Math.min(state.doc.length, cursor.from));
        const to = Math.max(from, Math.min(state.doc.length, cursor.to || from + 1));
        const key = `${from}:${to}`;
        if (seen.has(key)) continue;
        seen.add(key);
        diagnostics.push({from, to, severity: "error", message: "Invalid HCL syntax."});
    } while (cursor.next());
    return diagnostics;
}

export function deploymentConfigCodeWidget(args) {
    const documentModel = args?.document;
    if (typeof documentModel?.read !== "function" || typeof documentModel?.replace !== "function") {
        throw new Error("deploymentConfigCodeWidget requires document.read() and document.replace(next)");
    }

    const catalogs = args.catalogs || {};
    const constraints = args.constraints || {};
    const serializerOptions = {pinVersions: Boolean(constraints.updateMode)};
    const diagnostics = van.state([]);
    const hclValid = van.state(true);
    const staleDraft = van.state(false);
    let currentText = deploymentDocumentToHcl(documentModel.read(), catalogs, serializerOptions);
    const host = div({
        class: "flex-1 min-h-0 min-w-0 overflow-hidden",
        "data-testid": "deployment-hcl-editor",
    });
    const element = div(
        {
            class: "flex h-full flex-1 min-h-0 min-w-0 flex-col overflow-hidden bg-gray-950",
            "data-testid": "deployment-code-widget",
        },
        host,
        div(
            {class: "shrink-0 bg-gray-900/90 text-[11px]"},
            div(
                {class: "flex min-h-8 items-center justify-between gap-3 px-3 py-1.5"},
                div(
                    {class: "flex min-w-0 items-center gap-3"},
                    span(
                        {class: () => hclValid.val ? "text-emerald-400" : "text-red-400"},
                        () => hclValid.val ? "HCL valid" : "HCL invalid",
                    ),
                ),
                div(
                    {class: "flex shrink-0 items-center gap-3"},
                    span(
                        {class: () => diagnostics.val.length ? "text-amber-300" : "text-gray-500"},
                        () => `${diagnostics.val.length} diagnostic${diagnostics.val.length === 1 ? "" : "s"}`,
                    ),
                    button({
                        type: "button",
                        class: () => staleDraft.val ? "text-blue-300 hover:text-blue-200 cursor-pointer" : "hidden",
                        onclick: () => setCanonicalDocument(),
                    }, "Reload UI state"),
                ),
            ),
            () => diagnostics.val.length
                ? div(
                    {class: "max-h-28 overflow-y-auto px-3 pb-2"},
                    ...diagnostics.val.map(item => {
                        const line = currentText.slice(0, Math.max(0, item.from || 0)).split("\n").length;
                        const error = item.severity === "error";
                        return div(
                            {class: "grid grid-cols-[auto_auto_minmax(0,1fr)] items-start gap-2 rounded-md px-2 py-1.5 odd:bg-black/15"},
                            span({class: error ? "mt-1 h-1.5 w-1.5 rounded-full bg-red-400" : "mt-1 h-1.5 w-1.5 rounded-full bg-amber-300"}),
                            span({class: "font-mono text-gray-500"}, `L${line}`),
                            span({class: error ? "break-words text-red-200" : "break-words text-amber-100"}, item.message),
                        );
                    }),
                )
                : '',
        ),
    );

    let invalidDraft = false;
    let view = null;
    let suppressUpdates = false;
    let activationPending = false;
    let invalidBaseKey = '';

    const documentKey = () => JSON.stringify(documentModel.read());

    const initialParse = parseDeploymentHcl(currentText, catalogs, constraints);
    diagnostics.val = initialParse.diagnostics;

    const evaluate = (state, commit) => {
        const text = state.doc.toString();
        const syntax = syntaxDiagnostics(state);
        const parsed = parseDeploymentHcl(text, catalogs, constraints);
        const sharedDocumentChanged = Boolean(invalidBaseKey && invalidBaseKey !== documentKey());
        const conflict = commit && Boolean(parsed.document) && !syntax.some(item => item.severity === "error")
            && !parsed.diagnostics.some(item => item.severity === "error") && sharedDocumentChanged;
        const conflictDiagnostic = conflict
            ? [{from: 0, to: Math.min(1, state.doc.length), severity: "error", message: "The UI changed after this invalid draft was saved. Reload the UI state before continuing."}]
            : [];
        const nextDiagnostics = [...syntax, ...parsed.diagnostics, ...conflictDiagnostic];
        const hasSyntaxErrors = syntax.some(item => item.severity === "error");
        const hasSchemaErrors = parsed.diagnostics.some(item => item.severity === "error");
        const hasErrors = hasSyntaxErrors || hasSchemaErrors || !parsed.document || conflict;

        currentText = text;
        diagnostics.val = nextDiagnostics;
        hclValid.val = !hasSyntaxErrors;

        if (!commit) return nextDiagnostics;
        if (hasErrors) {
            if (!invalidDraft) invalidBaseKey = documentKey();
            invalidDraft = true;
            staleDraft.val = staleDraft.val || conflict;
        } else {
            documentModel.replace(parsed.document);
            invalidDraft = false;
            invalidBaseKey = '';
            staleDraft.val = false;
        }
        return nextDiagnostics;
    };

    const setCanonicalDocument = () => {
        const canonical = deploymentDocumentToHcl(documentModel.read(), catalogs, serializerOptions);
        currentText = canonical;
        if (canonical !== view.state.doc.toString()) {
            suppressUpdates = true;
            try {
                view.dispatch({
                    changes: {from: 0, to: view.state.doc.length, insert: canonical},
                    annotations: Transaction.addToHistory.of(false),
                });
            } finally {
                suppressUpdates = false;
            }
        }
        invalidDraft = false;
        invalidBaseKey = '';
        staleDraft.val = false;
        evaluate(view.state, false);
    };

    const createEditor = () => {
        const state = EditorState.create({
            doc: currentText,
            extensions: [
                lineNumbers(),
                highlightActiveLineGutter(),
                foldGutter(),
                history(),
                drawSelection(),
                indentOnInput(),
                bracketMatching(),
                closeBrackets(),
                syntaxHighlighting(defaultHighlightStyle, {fallback: true}),
                hcl(),
                referenceHighlighting,
                autocompletion({override: [schemaCompletion(catalogs)]}),
                linter(editorView => {
                    const syntax = syntaxDiagnostics(editorView.state);
                    const parsed = parseDeploymentHcl(editorView.state.doc.toString(), catalogs, constraints);
                    return [...syntax, ...parsed.diagnostics];
                }, {delay: 250}),
                lintGutter(),
                highlightActiveLine(),
                EditorView.lineWrapping,
                keymap.of([
                    ...closeBracketsKeymap,
                    ...defaultKeymap,
                    ...historyKeymap,
                    ...completionKeymap,
                    ...foldKeymap,
                    indentWithTab,
                ]),
                editorTheme,
                EditorView.updateListener.of(update => {
                    if (update.docChanged && !suppressUpdates) evaluate(update.state, true);
                }),
            ],
        });
        view = new EditorView({state, parent: host});
        evaluate(view.state, false);
    };

    const finishActivation = () => {
        if (!activationPending || !host.isConnected) return;
        activationPending = false;

        if (!view) {
            if (!invalidDraft) currentText = deploymentDocumentToHcl(documentModel.read(), catalogs, serializerOptions);
            createEditor();
        } else if (invalidDraft) {
            staleDraft.val = Boolean(invalidBaseKey && invalidBaseKey !== documentKey());
            evaluate(view.state, true);
        } else {
            setCanonicalDocument();
        }

        view.requestMeasure();
        requestAnimationFrame(() => view?.focus());
    };

    const activate = () => {
        activationPending = true;
        if (host.isConnected) {
            finishActivation();
            return;
        }
        requestAnimationFrame(finishActivation);
    };

    return {
        element,
        activate,
        invalidReason: () => diagnostics.val.find(item => item.severity === "error")?.message || "",
        diagnostics,
    };
}
