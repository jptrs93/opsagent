# Deployment HCL language

A self-contained CodeMirror language package for the deployment config dialect
of HCL, intended to replace the third-party `codemirror-lang-hcl` dependency.
That grammar cannot parse function calls in object-value position
(`env_vars = { "KEY" = secret("x") }`, `cert = acme()`), which forced the
editor widget to suppress its error nodes with regexes. This grammar is
hardcoded to the dialect defined by `src/components/deploymentHcl.js` and
parses all generated configs without error nodes.

Not yet wired into the editor widget.

## Files

- `deploymentHcl.grammar` — Lezer grammar source (the file to edit).
- `parser.js`, `parser.terms.js` — generated output, checked in; do not edit.
- `generate.js` — regenerates the parser: `pnpm run generate:hcl-parser`.
- `index.js` — exports `deploymentHclLanguage` (LRLanguage with highlighting,
  indentation, and folding metadata) and `deploymentHcl()` (LanguageSupport),
  mirroring the `hcl()` entry point the widget currently imports.

## Dialect summary

Statements are attributes (`name = expression`) or unlabeled blocks
(`name { ... }`). Expressions are single-line JSON-escaped strings, integers,
booleans, lists, objects (identifier or quoted-string keys, commas optional),
and function calls of any arity, nestable (`mount(default_volume(), "/data")`).
Comments: `#`, `//`, `/* */`. Identifiers allow hyphens after the first
character.
