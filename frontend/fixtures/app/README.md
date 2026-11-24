# App fixture

The whole web UI running in the browser with no backend. It mounts the real
`dashboard()` and every page, plus the login, enrol and enrol-landing pages,
over an in-memory cluster with realistic data. Use it to look at any page under
any theme, and to take screenshots.

## Run it

```bash
cd frontend
pnpm install
pnpm run dev:app
```

This opens `/fixtures/app/`. The URL takes two parameters:

- `view`: `app` (default, signed in as `joss`), `login`, `enrol` or `landing`.
- `chrome=0`: hides the small view switcher in the top-right corner.

The theme comes from the cog in the sidebar footer. It is stored in
localStorage under `opsagent_theme` (`slate`, `graphite` or `classic`) and set
as `data-theme` on `<html>`.

`window.__fixture` holds the in-memory store and the list of API methods
called that the fixture does not mock.

## Take screenshots

```bash
cd frontend
node fixtures/app/shots.mjs --theme classic
node fixtures/app/shots.mjs --theme all --shots status,secrets,cluster-inspector
```

Options:

- `--theme`: `slate`, `graphite`, `classic`, a comma list, or `all`. Default `all`.
- `--shots`: comma list of shot names. Default every shot.
- `--out`: output folder. Default `<os tmpdir>/shots`. Files go to `<out>/<theme>/<shot>.png`.
- `--port`: dev server port. Default 5199.
- `--width`, `--height`, `--scale`: viewport size and device scale. Default 1440x900 at 1.

The script uses Playwright from `testing-vms/e2e/node_modules`, so run
`pnpm install` there once. It starts Vite on the port only if nothing listens
there yet, and stops only the server it started. A failing shot is reported
and the others still run. The script prints a summary with any browser console
errors, and exits non-zero if a shot failed.

Shots:

- `status`, `status-inspector`, `status-history`, `status-editor`, `status-create`,
  `dialog-recently-deleted` and `dialog-run-report` are the Deployments page.
- `secrets`, `secrets-inspector` and `dialog-secret-value` are Secrets / Configs.
- `assets` and `assets-inspector` are Assets.
- `logs`, `metrics` and `metrics-deployment` are Logs and Metrics.
- `users`, `users-rules`, `users-you`, `sessions` and `sessions-user` are Users & roles and Sessions.
- `spaces`, `spaces-domain`, `dialog-add-space` and `policies` are Spaces and network policies.
- `cluster`, `cluster-inspector`, `cluster-access`, `dialog-node-access-policy`, `cluster-enrollments` and
  `cluster-replication` are Nodes.
- `settings` is Settings.
- `shell-stream-card` (bottom-left crop) and `shell-theme-menu` are the sidebar footer.
- `space-filter`, `space-filter-presets` and `space-filter-search` are the space filter open: plain, with
  three presets saved through the menu and the name field showing, and on Secrets / Configs with a query typed.
- `login`, `enrol` and `landing` are the signed-out pages.

## Add a shot

Add an entry to `SHOTS` in `shots.mjs`. `steps` gets the page after the app
has loaded and must wait for something that shows the page is filled in, such
as a row's `data-testid`. `view` picks a signed-out view, and `clip` returns a
crop rectangle from the viewport width and height.

## What is mocked

`server.js` replaces the generated API client's methods. The event stream
sends an opening snapshot, a heartbeat every 3 seconds, and a small real change
every 20 seconds: billing-cron crashes and restarts, worker-a's node status
changes, and the backup status moves on. Writes such as creating or updating a
deployment, editing secrets, assets, policies, spaces, nodes, users and
settings are applied to the store and sent back through the stream. A new or
updated deployment goes through building, starting and running.

The data is built in `data.js`: spaces `_system`, global, prod, staging and
dev; five nodes (worker-c draining, edge-1 disconnected), a pending enrolment
and an evicted node; about 15 deployments in different states plus the system
deployments; secrets, configs and assets in folders with versions; six users
(sam pending), roles, grants and global rules; agent and user sessions;
network, domain and node access policies; and backup, secrets and ingress status. Logs,
metrics, run reports, prepare output and version lists are generated.

Asset content and uploads go through a patched `fetch` and `XMLHttpRequest`.

## What is not mocked

Any API method without a mock rejects with `not mocked in fixture: <method>`
and logs that to the console. Passkey sign-in and registration need a real
WebAuthn ceremony and fail. Sign-out goes to the login view. The stream card's
byte count stays at 0, because it measures the real network response.
