# Logs page redesign fixture

Run from `frontend/`:

```sh
pnpm run dev:logs-redesign
```

Proposal for re-doing `src/pages/logs.js` around structured events instead of a
single `<pre>` of raw text: a virtualised row list, promotable field columns, a
time histogram, row expansion into the full record, and a ±context pivot.

## Layout

- **One-line search bar.** The old field farm (space/deployment/version
  selects, level select, from/to inputs, quick-range buttons, search string)
  collapses into: scope select · one query input · time-range dropdown ·
  Search. The query input is the single source of filter truth — bare words
  and `"quoted phrases"` match the message, `key:value` matches a field,
  `key:*` means the field exists, a leading `-` negates. Everything else
  (sidebar ⊕/⊖ actions, row-detail filters) just edits this string, and the
  parsed tokens render as removable chips under the bar.
- **Time histogram with brush-zoom.** Counts per bucket for the current query,
  stacked by level (error on top), with a hover tooltip per bucket. Dragging
  across it sets a custom time range and re-runs; `clear zoom` returns to the
  preset. The legend doubles as level toggles and shows per-level match
  counts. Fill colors were run through the dataviz palette validator against
  this surface (`#c42121` / `#c67b04` / `#3b82f6` / `#0e9488`).
- **Fields sidebar (Kibana-style).** *Columns* lists the current column set in
  order (hover to reorder/remove); *Available fields* expands any field into
  top-5 values over a sample of the current matches, each with coverage bars
  and filter for/out actions, plus an add-as-column toggle.
- **Virtualised results table.** Default columns `time | message | level`,
  newest first, fixed-height rows absolutely positioned over a spacer — only
  the visible window (~60 rows) exists in the DOM. Error/warn rows carry a 2px
  left tint strip. `wrap` switches to taller rows with the message clamped to
  three lines. Result lists are capped at the newest 200k rows (the histogram
  still covers everything) to stay under browser element-height limits —
  which matches how real log UIs cap result sets anyway.
- **Row expansion.** Clicking a row opens an inline detail pane: a key/value
  *Fields* tab (per-field filter for/out and column toggles) and a raw *JSON*
  tab, plus copy-JSON and *View in context*.
- **Context view.** A terminal-tail overlay of ±50 events around the anchor by
  storage order, ignoring the search filters, with load-50-more at both ends
  and the anchor row highlighted. Esc / backdrop click closes.

## Mock data

`mockData.js` is a virtual store: ~1.3M events over 48h derived per-index from
an integer hash, so `event(i)` is deterministic and nothing is materialized up
front. The rate curve is segmented to give the histogram something to find: a
worker disk-pressure warn spike (~T−30h), a 3-minute deploy gap (~T−26h, also
flips the `version` field 506→507), and a 20-minute postgres-outage error
storm (~T−5h) concentrated in api/ingress/postgres. Queries scan the index
range in chunks (yielding to the UI, cancellable); cheap fields (level,
service, host) filter before any message string is built, so a full-range
unfiltered scan is ~150ms.

## Backend (now implemented)

This design shipped as the real `src/pages/logs.js`, backed by the node-local
log storage backend from `docs/future-work/logmanager-implementation-plan.md`
— see the "Search API sketch" section there for the endpoint design. In short:

- **`logquery`**: one round trip returning parsed records (newest 10k,
  one-shot, no cursor), the per-level histogram over the full range, the
  total match count (drives "showing 10,000 of N — refine"), and the
  available field names.
- **Field stats**: per-field top values + coverage + other bucket ride in the
  query response, sampled over the newest 5k matched records during the same
  scan — the sidebar needs no extra request.
- **Context view**: no row addressing — estimate local record frequency and
  run a widened time-range query centred on the anchor row's timestamp,
  trimming client-side.
