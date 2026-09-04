// HCL editor palette: the code widget's default colour theme and syntax
// highlighting. A dark-surface palette drawn from the app's Tailwind
// 300-level accents on a background one step lighter than the page, in place
// of CodeMirror's light-theme `defaultHighlightStyle`, whose purple keywords,
// dark red strings, and navy properties were made for a white page.
//
// The code widget always applies its own layout theme; this only carries
// colour, font size, and weight, so a caller-supplied `theme` can replace it.

import {EditorView} from "@codemirror/view";
import {HighlightStyle, syntaxHighlighting} from "@codemirror/language";
import {tags} from "@lezer/highlight";

export const palette = {
    background: "#161e2d",
    gutter: "#121927",
    text: "#e2e8f0",
    block: "#c4b5fd",       // deployment / container / identity ... block names
    property: "#7dd3fc",    // attribute names
    string: "#86efac",
    number: "#fdba74",
    bool: "#f0abfc",
    comment: "#6b7280",
    reference: "#f9a8d4",   // secret() / asset() / address() ... calls
    symbol: "#fde68a",      // the quoted name inside a reference call
    punctuation: "#8b95a7",
};

const colorTheme = EditorView.theme({
    "&": {
        color: palette.text,
        backgroundColor: palette.background,
    },
    // 12px where the form face is 13px: the mono face reads larger at equal
    // size.
    ".cm-content": {
        caretColor: "#93c5fd",
        fontSize: "12px",
        lineHeight: "1.7",
    },
    ".cm-scroller": {scrollbarColor: `#3f4a5c ${palette.background}`},
    ".cm-scroller::-webkit-scrollbar-track": {background: palette.background},
    ".cm-scroller::-webkit-scrollbar-thumb": {
        background: "#3f4a5c",
        border: `2px solid ${palette.background}`,
        borderRadius: "999px",
    },
    ".cm-scroller::-webkit-scrollbar-thumb:hover": {background: "#566275"},
    ".cm-gutters": {
        fontSize: "12px",
        backgroundColor: palette.gutter,
        color: "#4f5a6c",
        border: "none",
        borderRight: "1px solid #1f2937",
        paddingLeft: "3px",
    },
    ".cm-activeLine": {backgroundColor: "rgba(148, 163, 184, 0.07)"},
    ".cm-activeLineGutter": {backgroundColor: "rgba(148, 163, 184, 0.07)", color: "#9ca3af"},
    ".cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection": {
        backgroundColor: "#27446f !important",
    },
    ".cm-cursor, .cm-dropCursor": {borderLeftColor: "#93c5fd"},
    "&.cm-focused .cm-matchingBracket": {
        backgroundColor: "rgba(96, 165, 250, 0.18)",
        outline: "1px solid rgba(96, 165, 250, 0.45)",
    },
    ".cm-foldGutter span": {color: "#6b7280"},
    ".cm-foldPlaceholder": {backgroundColor: "#1f2937", border: "1px solid #374151", color: "#9ca3af"},
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
    ".cm-reference-function": {color: `${palette.reference} !important`, fontWeight: "500"},
    ".cm-reference-symbol": {color: `${palette.symbol} !important`},
}, {dark: true});

const highlightStyle = HighlightStyle.define([
    {tag: tags.keyword, color: palette.block, fontWeight: "500"},
    {tag: tags.propertyName, color: palette.property},
    {tag: tags.string, color: palette.string},
    {tag: tags.number, color: palette.number},
    {tag: tags.bool, color: palette.bool},
    {tag: [tags.comment, tags.lineComment, tags.blockComment], color: palette.comment, fontStyle: "italic"},
    {tag: tags.function(tags.variableName), color: palette.reference},
    {tag: [tags.paren, tags.squareBracket, tags.brace], color: palette.punctuation},
    {tag: [tags.definitionOperator, tags.separator, tags.punctuation], color: palette.punctuation},
    {tag: tags.invalid, color: "#f87171"},
]);

export const deploymentHclTheme = [syntaxHighlighting(highlightStyle), colorTheme];
