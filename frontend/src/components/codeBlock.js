import van from "vanjs-core";
import {checkIcon, chevronDownIcon, copyIcon} from "../lib/icons.js";

const {button, div, pre, span} = van.tags;

// CodeMirror arrives lazily (same pattern as assetEditor) so pages that show a
// code block do not pull the editor bundle into their initial load; a styled
// <pre> stands in until the chunk lands.
let editorLoader;
const loadEditor = () => {
    editorLoader ||= import("./assetCodeEditor.js").then(module => module.assetCodeEditor);
    return editorLoader;
};

const stateValue = value => typeof value === "function" ? value() : (value?.val ?? value);
const valueText = value => String(stateValue(value) ?? "");

// A code block with a header bar: title on the left (a collapse toggle when an
// `open` state is passed), copy on the right, CodeMirror underneath.
//
//   codeBlock({title: "Install command", value: () => command(), wrap: true})
//
// - `value`: string, function, or van state. With `editable: true` it must be
//   a van state; edits write back to it.
// - `open`: optional van.state(bool); its presence makes the header a
//   collapse toggle. The editor stays mounted (hidden) while collapsed.
// - `syntax`: optional async loader returning extra CodeMirror extensions
//   (language support, highlighting, linting). The component knows nothing
//   about dialects; the caller supplies them, behind a dynamic import so the
//   language module stays out of the caller's chunk:
//     syntax: () => import("@codemirror/lang-yaml").then(m => [m.yaml()])
// - `lineNumbers: false` drops the gutter for prose-like content.
// - `maxHeight`: CSS length; content beyond it scrolls inside the block.
// - `actions`: extra header nodes, placed left of the copy button.
export function codeBlock({
    title,
    value,
    open = null,
    syntax = null,
    editable = false,
    wrap = false,
    lineNumbers = true,
    fontSize = "11px",
    maxHeight = "",
    actions = [],
    class: extraClass = "",
    testId = "code-block",
    ariaLabel = "",
}) {
    const copied = van.state(false);

    const copyButton = button(
        {
            type: "button",
            class: "inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded border border-gray-600 text-gray-300 hover:bg-surface-hover cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed",
            disabled: () => !valueText(value),
            title: () => copied.val ? "Copied" : `Copy ${title || "code"}`,
            "aria-label": () => copied.val ? "Copied" : `Copy ${title || "code"}`,
            onclick: async () => {
                try {
                    await navigator.clipboard.writeText(valueText(value));
                    copied.val = true;
                    setTimeout(() => copied.val = false, 2000);
                } catch {
                    copied.val = false;
                }
            },
        },
        () => copied.val ? checkIcon({class: "w-3 h-3 text-green-400"}) : copyIcon({class: "w-3 h-3"}),
        () => copied.val ? "Copied" : "Copy",
    );

    const titleClass = "text-[11px] font-semibold uppercase tracking-wider text-gray-400";
    const heading = open
        ? button(
            {
                type: "button",
                "aria-expanded": () => String(open.val),
                class: `inline-flex items-center gap-1.5 rounded px-1 py-0.5 ${titleClass} hover:text-gray-200 cursor-pointer`,
                onclick: () => { open.val = !open.val; },
            },
            chevronDownIcon({class: () => `w-3 h-3 transition-transform ${open.val ? "" : "-rotate-90"}`}),
            title)
        : span({class: `px-1 ${titleClass}`}, title);

    const editor = van.state(null);
    const loadError = van.state("");
    Promise.all([loadEditor(), syntax ? syntax() : []])
        .then(([codeEditor, extensions]) => {
            editor.val = codeEditor({
                value,
                disabled: !editable,
                ariaLabel: ariaLabel || title,
                bare: true,
                wrap,
                lineNumbers,
                fontSize,
                // Swapped relative to the CM default: the body takes the
                // lighter surface tone and the header the darker one.
                background: "#1f2937",
                extensions,
            });
        })
        .catch(error => { loadError.val = error.message || "Unable to load editor"; });

    const body = div(
        {class: "flex min-h-0 flex-col", style: maxHeight ? `max-height:${maxHeight}` : ""},
        div(
            {class: "flex-1 min-h-0 overflow-hidden"},
            () => editor.val || pre(
                {class: `m-0 h-full overflow-auto bg-gray-800 p-3 font-mono text-[11px] leading-relaxed text-gray-200 ${wrap ? "whitespace-pre-wrap break-words" : "whitespace-pre"}`},
                () => loadError.val || valueText(value)),
        ),
    );

    return div(
        {class: `flex min-h-0 flex-col overflow-hidden rounded-md border border-gray-700/60 bg-gray-800 ${extraClass}`, "data-testid": testId},
        div(
            // Darker chrome over a surface-toned body.
            {class: "flex flex-none flex-wrap items-center gap-2 bg-gray-900 px-1.5 py-1 min-h-[30px]"},
            heading,
            div({class: "flex-1"}),
            ...actions,
            copyButton,
        ),
        // hidden rather than unmounted, so the lazily created CodeMirror view
        // survives collapse/expand and keeps its scroll position.
        open ? div({class: () => open.val ? "contents" : "hidden"}, body) : body,
    );
}
