# Nodes fixture

> The tabbed page with the inspector is what `src/pages/cluster.js` now
> ships. This fixture mounts that page over an in-memory node API so it can
> be exercised without a cluster. The design is described in
> `docs/engineering/frontend.md` (Nodes).

Nothing of the page is forked. It reads the real stores (`machinesS`,
`enrollmentsS`, `backupStatusS`, `spacesS`, `systemConfigS`, `deploymentsS`)
and calls the real `capi` methods.

Run from `frontend/`:

```sh
pnpm run dev:nodes-redesign
```

The page renders inside a static replica of the dashboard shell (`shell.js`)
with Nodes active. The floating panel (bottom left) switches the dataset; it
persists in the URL hash, so `#data=busy` opens that state directly.

## Layout

A tab strip in the Users & roles style with three pinned tabs:

- **Nodes** — one dense row per enrolled node: name (with a `primary` tag),
  status dot and word (`draining` replaces the word, the dot keeps the
  connection state), cluster address, host addresses (`unknown` when the
  node could not enumerate them), allowed spaces, agent version, runtime
  versions, connected since. Lists collapse to a count (`3
  addresses`, `2 spaces`, `all`) and every cell truncates with an ellipsis;
  the full value rides on the cell's title. Clicking a row opens the
  inspector on the right (resizable, close with × or by clicking the row
  again): editable name, identifier, timestamps, every address with a copy
  button, the full transport key, agent and runtime versions, the
  allowed-spaces editor, and
  Drain / Evict for secondaries. Evicted nodes are hidden behind the
  `Evicted (n)` toggle in the toolbar and render muted at the bottom.
- **Enrollments** — the pending requests table (request id and machine id,
  remote IP, agent version, requested, accept-as name and button; Enter in
  the name field accepts) and, under its own band, the secondary install
  command with a Copy button.
- **Replication** — the backup status badge and sentence, tiles for local
  and remote TXID, lag, and last successful sync, then the asset-pending
  notice and any database or asset error. The tab's dot mirrors the badge
  tone. Not-configured shows a short pointer to Settings.

`mock.js` writes the datasets into the stores and replaces
`getV1NodesEnrollmentsInfo`, `postV1NodesRename`, `postV1NodesAllowedSpaces`,
`postV1NodesDrain`, `postV1NodesExposure`, `postV1NodesEvict` and
`postV1NodesEnrollmentsAccept` with in-memory versions (with the backend's
refusals: duplicate names, the primary, stale `expected_seq`, pinned
deployments without force), so rename, spaces, drain, the real evict dialog
and accepting an enrollment all work end to end and the tables update live.

## Scenarios

- `typical` — primary plus four secondaries (one draining, one disconnected,
  one with a narrowed space list), one evicted node, one pending enrollment,
  backups in sync.
- `busy` — twelve live nodes with several host addresses each, two evicted,
  three pending enrollments, backups catching up with assets moving to S3.
- `empty` — a fresh install: the primary alone, nothing pending, backups not
  configured.
- `trouble` — two disconnected secondaries, one draining on an old agent
  with no runtime and unknown host addresses, a stale enrollment, replication not running with
  a database error and an asset error.

## Open questions

- With the inspector open the runtime column truncates to
  `containerd 2.0.4 · runc …`; shortening the label to the versions alone
  (`2.0.4 / 1.2.6`) would fit everywhere.
- The inspector rebuilds on every store update, so an allowed-spaces edit in
  progress survives (its draft lives at page level) but a rename error
  message does not.
