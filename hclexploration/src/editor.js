import {autocompletion} from "@codemirror/autocomplete";
import {syntaxTree} from "@codemirror/language";
import {lintGutter, linter} from "@codemirror/lint";
import {EditorState} from "@codemirror/state";
import {Decoration, EditorView, ViewPlugin, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection} from "@codemirror/view";
import {defaultKeymap, history, historyKeymap, indentWithTab} from "@codemirror/commands";
import {bracketMatching, defaultHighlightStyle, foldGutter, indentOnInput, syntaxHighlighting} from "@codemirror/language";
import {closeBrackets, closeBracketsKeymap, completionKeymap} from "@codemirror/autocomplete";
import {hcl} from "codemirror-lang-hcl";
import {completionOptions, validateDeploymentHcl} from "./schema.js";
import {referenceCatalogForDocument} from "./mockStateStream.js";

const editorTheme = EditorView.theme({
    "&": {height: "100%", color: "#e8edf5", backgroundColor: "#0c0f14"},
    ".cm-content": {fontFamily: '"IBM Plex Mono", "SFMono-Regular", Consolas, monospace', fontSize: "13px", lineHeight: "1.72", caretColor: "#78dba9", padding: "18px 0 80px"},
    ".cm-line": {padding: "0 10px"},
    ".cm-scroller": {overflow: "auto"},
    ".cm-gutters": {backgroundColor: "#0c0f14", color: "#46505f", border: "none", paddingLeft: "4px"},
    ".cm-activeLine": {backgroundColor: "#151a22"},
    ".cm-activeLineGutter": {backgroundColor: "#151a22", color: "#8995a5"},
    ".cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection": {backgroundColor: "#244438 !important"},
    ".cm-cursor, .cm-dropCursor": {borderLeftColor: "#78dba9"},
    ".cm-foldGutter span": {color: "#657184"},
    ".cm-tooltip": {backgroundColor: "#171c24", border: "1px solid #303844", color: "#dce4ef"},
    ".cm-tooltip-autocomplete > ul > li[aria-selected]": {backgroundColor: "#254235", color: "#ffffff"},
    ".cm-lintRange-error": {backgroundImage: "none", borderBottom: "2px solid #f07178"},
    ".cm-lintRange-warning": {backgroundImage: "none", borderBottom: "2px solid #e6b566"},
    ".cm-reference-function": {color: "#c792ea !important", fontWeight: "500"},
    ".cm-reference-symbol": {color: "#f2c879 !important"},
}, {dark: true});

function referenceDecorations(view) {
    const decorations = [];
    for (const range of view.visibleRanges) {
        const text = view.state.sliceDoc(range.from, range.to);
        const functionPattern = /\b(?:secret|config|asset|address|deployment|space|node|mount|default_volume|port_forward|tls_passthrough)(?=\s*\()/g;
        const symbolPattern = /\b(?:secret|config|asset|address|deployment|space|node)\(\s*("(?:\\.|[^"\\])*")/g;
        let match;
        while ((match = functionPattern.exec(text))) {
            const functionFrom = range.from + match.index;
            decorations.push(Decoration.mark({class: "cm-reference-function"}).range(functionFrom, functionFrom + match[0].length));
        }
        while ((match = symbolPattern.exec(text))) {
            const callFrom = range.from + match.index;
            const symbol = match[1];
            const symbolOffset = match[0].indexOf(symbol);
            decorations.push(Decoration.mark({class: "cm-reference-symbol"}).range(callFrom + symbolOffset, callFrom + symbolOffset + symbol.length));
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
        if (update.docChanged || update.viewportChanged) this.decorations = referenceDecorations(update.view);
    }
}, {decorations: plugin => plugin.decorations});

export function syntaxDiagnostics(state) {
    const diagnostics = [];
    const text = state.doc.toString();
    const nativeVersionReferences = [];
    const versionReferencePattern = /\b(?:secret|config|asset)\(\s*"(?:\\.|[^"\\\n])+"\s*,\s*\{\s*version\s*=\s*\d+\s*\}\s*\)/g;
    let match;
    while ((match = versionReferencePattern.exec(text))) {
        nativeVersionReferences.push({from: match.index, to: match.index + match[0].length});
    }
    const cursor = syntaxTree(state).cursor();
    do {
        if (cursor.type.isError) {
            if (nativeVersionReferences.some(range => cursor.from >= range.from && cursor.to <= range.to)) continue;
            const before = state.sliceDoc(Math.max(0, cursor.from - 120), cursor.from);
            // The bundled grammar reports a zero-width error after a valid
            // function call used as an object value; native HCL accepts it.
            const functionObjectBoundary = cursor.from === cursor.to
                && /(?:(?:secret|config|asset)\(\s*"[^"\n]+"(?:\s*,\s*\{\s*version\s*=\s*\d+\s*\})?\s*\)|(?:address|deployment|space|node)\(\s*"[^"\n]+"\s*\)),?\s*$/.test(before);
            if (functionObjectBoundary) continue;
            const from = Math.min(cursor.from, Math.max(0, state.doc.length - 1));
            diagnostics.push({
                from,
                to: Math.min(state.doc.length, Math.max(from + 1, cursor.to)),
                severity: "error",
                message: "Invalid HCL syntax.",
            });
        }
    } while (cursor.next());
    return diagnostics;
}

function diagnosticsFor(state) {
    return [...syntaxDiagnostics(state), ...validateDeploymentHcl(state.doc.toString())];
}

function catalogOptions(namespace, referenceCatalog, insideQuotes) {
    return referenceCatalog[namespace].map(item => ({
        label: item.key,
        apply: insideQuotes ? item.key : JSON.stringify(item.key),
        type: "variable",
        detail: `ID ${item.id}`,
        info: item.detail,
    }));
}

function versionOptions(namespace, referenceCatalog, key) {
    const versions = new Map();
    for (const item of referenceCatalog[namespace].filter(candidate => candidate.key === key)) {
        const version = Number(item.version || 0);
        if (version > 0) versions.set(version, {
            label: String(version),
            type: "constant",
            detail: `ID ${item.id}`,
        });
    }
    return [...versions.values()].sort((left, right) => Number(right.label) - Number(left.label));
}

export function schemaCompletion(context) {
    const referenceCatalog = referenceCatalogForDocument(context.state.doc.toString());
    const prefixStart = Math.max(0, context.pos - 100);
    const prefix = context.state.sliceDoc(prefixStart, context.pos);
    const versionReference = /(secret|config|asset)\(\s*"((?:\\.|[^"\\])*)"\s*,\s*\{\s*version\s*=\s*([0-9]*)$/.exec(prefix);
    if (versionReference) {
        let key;
        try {
            key = JSON.parse(`"${versionReference[2]}"`);
        } catch {
            return null;
        }
        return {
            from: context.pos - versionReference[3].length,
            options: versionOptions(versionReference[1], referenceCatalog, key),
            validFor: /^[0-9]*$/,
        };
    }
    const reference = /(secret|config|asset|address|deployment|space|node)\(\s*(?:"([^"]*)|([A-Za-z0-9_.-]*))$/.exec(prefix);
    if (reference) {
        const functionName = reference[1];
        const namespace = functionName === "address" ? "deployment" : functionName;
        const partial = reference[2] ?? reference[3] ?? "";
        const insideQuotes = reference[2] !== undefined;
        return {
            from: context.pos - partial.length,
            options: catalogOptions(namespace, referenceCatalog, insideQuotes),
            validFor: insideQuotes ? /^[^"]*$/ : /^[A-Za-z0-9_.-]*$/,
        };
    }
    const protocolReference = /port_forward\(\s*(?:"([^"]*)|([A-Za-z0-9_-]*))$/.exec(prefix);
    if (protocolReference) {
        const partial = protocolReference[1] ?? protocolReference[2] ?? "";
        const insideQuotes = protocolReference[1] !== undefined;
        return {
            from: context.pos - partial.length,
            options: ["tcp", "udp"].map(value => ({label: value, apply: insideQuotes ? value : JSON.stringify(value), type: "enum"})),
            validFor: insideQuotes ? /^[^"]*$/ : /^[A-Za-z0-9_-]*$/,
        };
    }
    const enumReference = /(?:^|\n)\s*(mode|strategy)\s*=\s*(?:"([^"]*)|([A-Za-z0-9_-]*))$/.exec(prefix);
    if (enumReference) {
        const values = {
            mode: ["virtual", "host"],
            strategy: ["recreate", "rollover"],
        }[enumReference[1]];
        const partial = enumReference[2] ?? enumReference[3] ?? "";
        const insideQuotes = enumReference[2] !== undefined;
        return {
            from: context.pos - partial.length,
            options: values.map(value => ({label: value, apply: insideQuotes ? value : JSON.stringify(value), type: "enum"})),
            validFor: insideQuotes ? /^[^"]*$/ : /^[A-Za-z0-9_-]*$/,
        };
    }
    const booleanReference = /(?:^|\n)\s*desired_running\s*=\s*([A-Za-z]*)$/.exec(prefix);
    if (booleanReference) {
        return {
            from: context.pos - booleanReference[1].length,
            options: ["true", "false"].map(value => ({label: value, type: "constant"})),
            validFor: /^[A-Za-z]*$/,
        };
    }
    const word = context.matchBefore(/[A-Za-z_]\w*/);
    if (!context.explicit && (!word || word.from === word.to)) return null;
    return {from: word ? word.from : context.pos, options: completionOptions, validFor: /^[A-Za-z_]\w*$/};
}

export function createEditor(parent, initialDocument, onUpdate) {
    let lastDiagnosticKey = "";
    const notify = state => {
        const diagnostics = diagnosticsFor(state);
        const key = diagnostics.map(item => `${item.from}:${item.to}:${item.severity}:${item.message}`).join("|");
        if (key !== lastDiagnosticKey) {
            lastDiagnosticKey = key;
            onUpdate(state.doc.toString(), diagnostics);
        } else {
            onUpdate(state.doc.toString(), null);
        }
    };

    const state = EditorState.create({
        doc: initialDocument,
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
            autocompletion({override: [schemaCompletion]}),
            linter(view => diagnosticsFor(view.state), {delay: 250}),
            lintGutter(),
            highlightActiveLine(),
            EditorView.lineWrapping,
            keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...historyKeymap, ...completionKeymap, indentWithTab]),
            editorTheme,
            EditorView.updateListener.of(update => {
                if (update.docChanged) notify(update.state);
            }),
        ],
    });
    const view = new EditorView({state, parent});
    notify(view.state);
    return {
        view,
        getDocument: () => view.state.doc.toString(),
        setDocument(document) {
            view.dispatch({changes: {from: 0, to: view.state.doc.length, insert: document}});
        },
        focus: () => view.focus(),
    };
}
