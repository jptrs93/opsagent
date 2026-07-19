import van from "vanjs-core";
import {createEditor} from "./editor.js";
import {containerExample, nixExample} from "./examples.js";
import {formatHcl, schemaSections} from "./schema.js";
import {referenceCatalogForDocument, selectedSpaceName} from "./mockStateStream.js";

const {button, div, header, main, section, aside, span, strong, p, code, details, summary} = van.tags;
const STORAGE_KEYS = {
    container: "opendeploy-hcl-exploration-container-v17",
    nix: "opendeploy-hcl-exploration-nix-v17",
    blank: "opendeploy-hcl-exploration-blank-v13",
};
const TAB_META = {
    container: {label: "Container", filename: "payments-api.hcl", initial: containerExample},
    nix: {label: "Nix", filename: "report-worker.hcl", initial: nixExample},
    blank: {label: "Blank", filename: "scratch.hcl", initial: ""},
};

function icon(name) {
    const paths = {
        code: ["M16 18l6-6-6-6", "M8 6l-6 6 6 6"],
        check: ["M20 6L9 17l-5-5"],
        alert: ["M10.3 2.9L1.8 17a2 2 0 001.7 3h17a2 2 0 001.7-3L14.7 2.9a2 2 0 00-4.4 0z", "M12 9v4", "M12 17h.01"],
        copy: ["M8 8h11a2 2 0 012 2v9a2 2 0 01-2 2H10a2 2 0 01-2-2V8z", "M16 8V5a2 2 0 00-2-2H5a2 2 0 00-2 2v9a2 2 0 002 2h3"],
        format: ["M4 6h16", "M7 12h10", "M9 18h6"],
    };
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("aria-hidden", "true");
    svg.setAttribute("class", "button-icon");
    for (const d of paths[name]) {
        const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
        path.setAttribute("d", d);
        path.setAttribute("fill", "none");
        path.setAttribute("stroke", "currentColor");
        path.setAttribute("stroke-width", "1.8");
        path.setAttribute("stroke-linecap", "round");
        path.setAttribute("stroke-linejoin", "round");
        svg.append(path);
    }
    return svg;
}

function savedDocument(tab) {
    try {
        return localStorage.getItem(STORAGE_KEYS[tab])
            ?? TAB_META[tab].initial;
    } catch {
        return TAB_META[tab].initial;
    }
}

function App() {
    const diagnostics = van.state([]);
    const saved = van.state(true);
    const activeTab = van.state("container");
    const copied = van.state(false);
    const documents = Object.fromEntries(Object.keys(TAB_META).map(tab => [tab, savedDocument(tab)]));
    const activeCatalog = van.state(referenceCatalogForDocument(documents.container));
    const activeSpace = van.state(selectedSpaceName(documents.container));
    const editorHost = div({class: "editor-host"});
    let editor;
    const saveTimers = {};

    const handleUpdate = (document, nextDiagnostics) => {
        const tab = activeTab.val;
        documents[tab] = document;
        activeCatalog.val = referenceCatalogForDocument(document);
        activeSpace.val = selectedSpaceName(document);
        if (nextDiagnostics) diagnostics.val = nextDiagnostics;
        saved.val = false;
        clearTimeout(saveTimers[tab]);
        saveTimers[tab] = setTimeout(() => {
            try {
                localStorage.setItem(STORAGE_KEYS[tab], documents[tab]);
                if (activeTab.val === tab) saved.val = true;
            } catch {
                if (activeTab.val === tab) saved.val = false;
            }
        }, 350);
    };

    const selectTab = tab => {
        if (tab === activeTab.val) return;
        activeTab.val = tab;
        saved.val = true;
        editor.setDocument(documents[tab]);
        editor.focus();
    };

    const copyDocument = async () => {
        await navigator.clipboard.writeText(editor.getDocument());
        copied.val = true;
        setTimeout(() => { copied.val = false; }, 1400);
    };

    const shell = div(
        {class: "app-shell"},
        header(
            {class: "topbar"},
            div(
                {class: "brand"},
                div({class: "brand-mark"}, icon("code")),
                div(
                    strong("OpenDeploy"),
                    span("HCL configuration lab"),
                ),
            ),
            div(
                {class: "topbar-meta"},
                span({class: "stream-pill"}, span(), "MOCK STREAM CONNECTED"),
                span({class: "prototype-pill"}, "EXPLORATION"),
                span({class: () => `save-state ${saved.val ? "is-saved" : ""}`}, () => saved.val ? "Draft saved locally" : "Saving draft..."),
            ),
        ),
        main(
            {class: "workspace"},
            section(
                {class: "editor-panel"},
                div(
                    {class: "panel-toolbar"},
                    div(
                        div({class: "eyebrow"}, "DEPLOYMENT CONFIG"),
                        div({class: "filename"}, () => TAB_META[activeTab.val].filename),
                    ),
                    div(
                        {class: "toolbar-actions"},
                        div(
                            {class: "example-tabs", role: "tablist", "aria-label": "Editor documents"},
                            ...Object.entries(TAB_META).map(([tab, meta]) => button({
                                class: () => `example-tab ${activeTab.val === tab ? "is-active" : ""}`,
                                role: "tab",
                                "aria-selected": () => String(activeTab.val === tab),
                                onclick: () => selectTab(tab),
                            }, meta.label)),
                        ),
                        button({class: "tool-button", title: "Format document", onclick: () => editor.setDocument(formatHcl(editor.getDocument()))}, icon("format"), span("Format")),
                        button({class: "tool-button", title: "Copy document", onclick: copyDocument}, icon("copy"), span(() => copied.val ? "Copied" : "Copy")),
                    ),
                ),
                editorHost,
                div(
                    {class: "statusbar"},
                    div(
                        {class: () => `validation-summary ${diagnostics.val.some(item => item.severity === "error") ? "has-errors" : "is-valid"}`},
                        () => diagnostics.val.some(item => item.severity === "error") ? icon("alert") : icon("check"),
                        () => diagnostics.val.length === 0 ? "Schema valid" : `${diagnostics.val.length} ${diagnostics.val.length === 1 ? "issue" : "issues"}`,
                    ),
                    span("HCL 2"),
                    span("Spaces: 2"),
                ),
            ),
            aside(
                {class: "reference-panel"},
                    div(
                        {class: "reference-heading"},
                        div({class: "eyebrow"}, "SCHEMA REFERENCE"),
                        strong("Deployment"),
                        p("Authorable fields from the current deployment API. Completions are derived from a mock global state-stream snapshot."),
                ),
                div(
                    {class: "reference-scroll"},
                    div(
                        {class: "root-signature"},
                        span("ROOT BLOCK"),
                        code("deployment { identity { ... } }"),
                    ),
                    ...schemaSections.map((item, index) => details(
                        {class: "schema-section", open: index < 2},
                        summary(
                            div(strong(item.name), code(item.signature)),
                            span({class: "disclosure"}, "+"),
                        ),
                        div(
                            {class: "schema-detail"},
                            p(item.description),
                            div({class: "field-list"}, ...item.fields.map(field => code(field))),
                        ),
                    )),
                    div(
                        {class: "reference-catalog"},
                        div({class: "catalog-title"}, span("MOCK STREAM REFERENCES"), code(() => activeSpace.val)),
                        () => div(...Object.entries(activeCatalog.val).map(([namespace, items]) => div(
                                {class: "catalog-group"},
                                strong(namespace),
                                div(...items.map(item => div(
                                    {class: "catalog-item"},
                                    code(item.key),
                                    span(`ID ${item.id}`),
                                ))),
                            )),
                        ),
                    ),
                    div(
                        {class: "mapping-note"},
                        strong("Resolved on save"),
                        p("The HCL retains symbolic names. Saving resolves space, node, and resource names and sends their immutable IDs to the current protobuf API."),
                    ),
                ),
                div(
                    {class: "diagnostics"},
                    div({class: "diagnostics-title"}, strong("Diagnostics"), span(() => String(diagnostics.val.length))),
                    () => diagnostics.val.length === 0
                        ? div({class: "empty-diagnostics"}, icon("check"), p("No schema issues found."))
                        : div({class: "diagnostic-list"}, ...diagnostics.val.slice(0, 6).map(item => div(
                            {class: `diagnostic-item ${item.severity}`},
                            span(),
                            p(item.message),
                        ))),
                ),
            ),
        ),
    );

    requestAnimationFrame(() => {
        editor = createEditor(editorHost, documents.container, handleUpdate);
        editor.focus();
    });
    return shell;
}

van.add(document.body, App());
