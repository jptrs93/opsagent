// HCL editor palette: the code widget's default colour theme and syntax
// highlighting, in place of CodeMirror's light-theme `defaultHighlightStyle`.
// Every colour is a CSS variable defined per theme in styles/editor.css, so
// switching the app theme recolours an open editor without rebuilding it.
//
// The code widget always applies its own layout theme; this only carries
// colour, font size, and weight, so a caller-supplied `theme` can replace it.

import {EditorView} from "@codemirror/view";
import {HighlightStyle, syntaxHighlighting} from "@codemirror/language";
import {tags} from "@lezer/highlight";

export const palette = {
    background: "var(--editor-bg)",
    gutter: "var(--editor-gutter-bg)",
    text: "var(--editor-text)",
    block: "var(--editor-block)",             // deployment / container / identity ... block names
    property: "var(--editor-property)",       // attribute names
    string: "var(--editor-string)",
    number: "var(--editor-number)",
    bool: "var(--editor-bool)",
    comment: "var(--editor-comment)",
    reference: "var(--editor-reference)",     // secret() / asset() / address() ... calls
    symbol: "var(--editor-symbol)",           // the quoted name inside a reference call
    punctuation: "var(--editor-punctuation)",
};

const colorTheme = EditorView.theme({
    "&": {
        color: palette.text,
        backgroundColor: palette.background,
    },
    // 12px where the form face is 13px: the mono face reads larger at equal
    // size.
    ".cm-content": {
        caretColor: "var(--editor-caret)",
        fontSize: "12px",
        lineHeight: "1.7",
    },
    ".cm-scroller": {scrollbarColor: `var(--editor-scroll-thumb) ${palette.background}`},
    ".cm-scroller::-webkit-scrollbar-track": {background: palette.background},
    ".cm-scroller::-webkit-scrollbar-thumb": {
        background: "var(--editor-scroll-thumb)",
        border: `2px solid ${palette.background}`,
        borderRadius: "999px",
    },
    ".cm-scroller::-webkit-scrollbar-thumb:hover": {background: "var(--editor-scroll-thumb-hover)"},
    ".cm-gutters": {
        fontSize: "12px",
        backgroundColor: palette.gutter,
        color: "var(--editor-gutter-text)",
        border: "none",
        borderRight: "1px solid var(--editor-gutter-line)",
        paddingLeft: "3px",
    },
    ".cm-activeLine": {backgroundColor: "var(--editor-active-line)"},
    ".cm-activeLineGutter": {backgroundColor: "var(--editor-active-line)", color: "var(--editor-gutter-active)"},
    ".cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection": {
        backgroundColor: "var(--editor-selection) !important",
    },
    ".cm-cursor, .cm-dropCursor": {borderLeftColor: "var(--editor-caret)"},
    "&.cm-focused .cm-matchingBracket": {
        backgroundColor: "var(--editor-bracket-bg)",
        outline: "1px solid var(--editor-bracket-line)",
    },
    ".cm-foldGutter span": {color: "var(--editor-fold)"},
    ".cm-foldPlaceholder": {
        backgroundColor: "var(--editor-fold-bg)",
        border: "1px solid var(--editor-fold-border)",
        color: "var(--editor-fold-text)",
    },
    ".cm-tooltip": {
        backgroundColor: "var(--editor-tooltip-bg)",
        border: "1px solid var(--editor-tooltip-border)",
        borderRadius: "var(--editor-tooltip-radius)",
        color: "var(--editor-tooltip-text)",
    },
    ".cm-tooltip-autocomplete > ul > li[aria-selected]": {
        backgroundColor: "var(--editor-completion-bg)",
        color: "var(--editor-completion-text)",
    },
    ".cm-diagnostic-error": {borderLeftColor: "var(--editor-diagnostic-error)"},
    ".cm-diagnostic-warning": {borderLeftColor: "var(--editor-diagnostic-warning)"},
    ".cm-lintRange-error": {backgroundImage: "none", borderBottom: "2px solid var(--editor-error)"},
    ".cm-lintRange-warning": {backgroundImage: "none", borderBottom: "2px solid var(--editor-warning)"},
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
    {tag: tags.invalid, color: "var(--editor-invalid)"},
]);

export const deploymentHclTheme = [syntaxHighlighting(highlightStyle), colorTheme];
