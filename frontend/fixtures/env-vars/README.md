# Environment variables pane fixture

Run from `frontend/`:

```sh
pnpm run dev:env-vars
```

Renders the production env-vars editor pane (`envVarsPane` in
`src/components/deploymentForm.js`) against in-memory secret/config catalogs.
The rest of the deployment form is omitted; only the summary row that opens
the pane is wired up. The pane's grouping/toggle logic lives in
`src/lib/envVarGrouping.js` and is covered by `pnpm test`
(`src/lib/envVarGrouping.test.js`).

## What the pane does

Two header-bar toggles, both off by default:

### Group by prefix

- A row's prefix is the key up to the first `_`. A key without an underscore
  uses the whole key as its prefix, so `DEBUG` still groups with `DEBUG_MODE`
  when both exist.
- Only prefixes with two or more rows form a named group. Singletons (and
  empty-key rows) collect into a shared trailing **No group** bucket.
- Named groups sort alphabetically; rows keep their original order within a
  group. Each group header is collapsible; collapse state is remembered per
  prefix while the pane is mounted.
- Regrouping is live: renaming a key so its prefix joins an existing group
  moves the row on that keystroke (focus moves with the rebuilt row — the
  pane's signature-rebuild mechanism).
- `+ Add environment variable` stays global at the bottom; a new (empty-key)
  row lands in No group, which auto-expands so the row is visible.

### Boolean toggles

- Rows whose name ends in `ENABLED` (case-insensitive) render a toggle switch
  in the Value cell in place of the free-text input.
- The Type dropdown stays, so a `*_ENABLED` name can still point at a shared
  config or secret — selecting a reference type swaps the toggle for the
  normal reference picker, and switching back to Value restores the toggle.
- Existing values are read leniently (`true`, `1`, `yes`, `on` = on, anything
  else = off); flipping the switch writes canonical `true`/`false`.

### Spreadsheet-style grid

- Table cells carry the 1px rules (`border-collapse` merges them into single
  lines); the controls inside are borderless, transparent, and fill their
  zero-padding cell, so adjacent rows never draw a double line.
- All row controls are pinned to `h-6` so every cell in a row is exactly the
  same height — without it, selects get intrinsic browser chrome and the mono
  font a different line box, leaving the controls in a row 20–23px tall and
  visibly misaligned.
- Focus highlights the cell from the inside (inset ring + darker fill), like
  a selected spreadsheet cell.
- The header row is a bordered `bg-gray-900` band; the Remove column sits
  outside the grid so the table reads as a three-column sheet with actions in
  the margin.
- Reference values are tinted with the app's per-kind colors — secrets
  `text-purple-300`, configs `text-blue-300`, assets `text-asset` — matching
  the explorer pages, so the Value column shows at a glance which rows are
  references and of what kind. Plain and address values stay `text-gray-100`.

## Scenarios

- `Typical` — four groups (CACHE, DATABASE, FEATURE, OTEL) plus singletons in
  No group, including `METRICS_ENABLED` so a toggle appears inside No group.
- `Many` — eight groups for density, with lenient boolean values (`yes`, `on`,
  `1`) and secret/config refs mixed into groups.
- `Empty` — header and add-button only.

`Saved env vars` under the form column mirrors production `formEnvVars` so you
can see exactly what would be written to the spec. The fixture has no asset or
deployment catalogs, so asset/address pickers render with empty option lists.
