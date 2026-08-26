# Deployment status page redesign fixture

Run from `frontend/`:

```sh
pnpm run dev:status-redesign
```

Proposal for re-doing `src/pages/status.js` in the direction of the flush
explorer pages (assets, secrets, configs): no bordered card, one `bg-surface`
page running to the window edges, a dense gridline table, and a right-hand
inspector instead of per-row History/Update buttons.

## Aggregated cells: adaptive one-line / stacked

Status, Version, and Prepare no longer split one sub-line per scheduled
instance; instances group by value with counts. Each cell tries the one-line
rendering first (`2 running · 1 crashed`) and falls back to one sub-line per
group when the estimate says the line would truncate in its fixed column —
so a row can be one-line in Status and stacked in Prepare at the same time.
The estimate is glyph-width arithmetic against the colgroup widths
(`USABLE_PX` in `app.js`), deterministic and cheap; the real page could do the
same or measure once per font.

## Layout

- **Excel-ish table.** Tight `px-2 py-[3px]` cells, hairline row rules with
  fainter column rules, sticky uppercase micro headers on the surface, space
  band rows (caret + space dot + name + count) instead of gradient dividers.
- **Collapsible spaces.** Clicking a band row (or its caret) collapses that
  space's rows.
- **Resizable columns.** Every column carries the explorer pages' resize
  grip (drag, or arrow keys when focused, Shift for bigger steps). Free
  container width is distributed evenly across all columns (except the edit
  gutter) and re-fitted on every container size change — window resizes and
  the inspector opening, closing, or being dragged — clamping at per-column
  minimums, below which the table scrolls horizontally. A dragged column
  keeps its width; the others absorb the delta. The adaptive
  one-line/stacked estimate follows the live widths — narrowing Status
  flips its rows to stacked, widening flips them back.
- **Spaces multi-select** in the toolbar, same control as the configs/secrets
  explorers (dots + `N spaces` + chevron, checkable menu, reset row when
  dirty). Default: every space selected except `_system`.
- **Nodes column** reads the node name for a single-node deployment and
  `N nodes` (full list in the tooltip) otherwise.
- **Actions column shrinks to an edit icon.** Update becomes a pencil icon;
  the History button is gone (history lives in the inspector); Fork / View
  config / Delete move to inspector footer buttons.
- **Row click opens the inspector** (resizable via the drag grip on its left
  edge, like the explorer pages) with two tabs:
  - *Details* — a dense facts table (name, space, nodes, created, deployed,
    target version, inbound IP and DNS name each with a copy button), then a
    per-instance table: id, node, status, version, prepare, restarts. Footer:
    Edit / Fork / View config / Prepare output / Delete. For `_system`
    deployments — really an aggregate of one deployment per node — the
    single-deployment facts (created, deployed, target, inbound IP, DNS
    name) are omitted; the per-instance table carries the per-node detail.
  - *History* — the free-text log becomes a CSV-like table
    (`Time | Type | V | By | Change`) with a status-rows toggle (off by
    default — config rows only); non-latest config rows carry an inline
    revert link in the version cell (`v12 revert`).

## Mock data

`mockData.js` covers the states the aggregation has to survive: uniform
multi-node running (api ×3), a mid-rollover with two versions on one node
(web), mixed `2 running · 1 crashed` (ingest), prepare-failed (mailer),
starting, stopped, a long container image tag (playground), and the
`_system` space (opendeploy / opendeploy-net across three nodes, one node a
version behind) for the spaces filter default. History is generated
deterministically per deployment: created → prepare → run, version bumps,
then whatever the current instances imply (crash storm, stop, failed image).

## Not modelled here

- Column show-hide and sort (would come along from the assets-page
  machinery in the real build), and persisting resized widths across
  visits.
- The `_system` group-merge rows (opendeploy / opendeploy-net collapse to one
  row per name today) and their group update overlay — here they are plain
  multi-instance rows in the `_system` space.
- Live streams — everything is static mock state.
