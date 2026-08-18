# Frontend (VanJS)

## Overview

The UI is a single-page app using VanJS (`vanjs-core`) and Vite. Rendering is done by composing DOM nodes with VanJS tag helpers. State is managed through VanJS reactive state. Styling uses Tailwind CSS with a dark theme.

Key files:
- `frontend/src/app.js` — bootstraps the app and dispatches routes.
- `frontend/src/lib/router.js` — `currentPath` / `navigate` helpers over `popstate` + `history.pushState`.
- `frontend/src/state/login.js` — authentication state management.
- `frontend/src/state/deployments.js` — live state stream consumer (see `POST /v1/global/state-stream`).
- `frontend/src/pages/` — page-level components.
- `frontend/src/components/` — reusable UI pieces.
- `frontend/src/capi/` — generated API client (`capi.js`, `model.js`) plus the stream decoder helper.

## Routing

Routing is handled by a small in-house module (`frontend/src/lib/router.js`)
exposing `currentPath` (a `van.state`) and `navigate(path)`. The route
table lives in `app.js`:
- `/bootstrap` — first-time setup (master password then passkey registration).
- `/` — dashboard (renders the login page when unauthenticated).
- anything else — falls back to the login page.

On app load, `initLoginState()` restores the JWT from `localStorage` and
validates it via `GET /v1/auth/current/session`; an invalid session is
cleared, so the user lands on the login page on the next render.

## Layout

The dashboard uses a split-pane layout:
- `components/sidebar.js` — left sidebar with top-level navigation (Status, Cluster).
- The main pane is split horizontally with a draggable divider (width persisted to `localStorage`). The right-hand pane shows the deployment logs / history sidebar when a card action opens one.
- The active page is tracked via a `van.state` value. Clicking a sidebar item swaps the main content.

## Pages

### Login (`pages/login.js`)
- Single "Sign in with passkey" button.
- "First time setup" link navigates to `/bootstrap`.

### Bootstrap (`pages/bootstrap.js`)
- Three-step flow: master password entry, passkey registration, completion.
- Steps are tracked via a `van.state('password' | 'register' | 'done')`.

### Status (`pages/status.js`)
- Consumes live deployment state from `POST /v1/global/state-stream` (binary protobuf stream via `AsyncIterable<State>`).
- Renders one table row per deployment, sorted by OPENDEPLOY-last, then space, name, node, and id (deterministic across stream reconnects).
- "Add deployment" button opens `components/createOverlay.js` to POST a typed `DeploymentCreateRequest` via `POST /v1/deployments/create`.
- Each row (`components/statusCard.js`) shows deployment, node, prepare and runtime details, audit metadata, and deployment actions. Status and Version are vertically split into one oldest-first subcell per non-final scheduled instance during rollovers; a candidate uses its pinned target version until its runner reports a version.
- The deploy overlay fetches available versions on demand via `POST /v1/deployments/versions`, lets the user edit the deployment spec form, and submits a typed `DeploymentUpdateRequest` via `POST /v1/deployments/update`.
- The per-node internal `opendeploy` and `opendeploy-net` deployments are merged client-side into one group row each (`makeSystemGroups` in `pages/status.js`, rendered by `systemGroupStatusRow`), with Node, Status, Version, Prepare, Restarts, and audit cells split into one subline per node — secondaries first, primary last. Their Update action opens `components/openDeployGroupUpdateOverlay.js`: a per-node table of current and target release dropdowns with an "Align versions" toggle (on by default, the primary's dropdown drives all rows). On confirm the browser orchestrates the rollout itself via sequential `POST /v1/deployments/update` calls — secondaries one at a time, each waiting for the node's runner to report the new version through the state stream, primary last, halting on the first failure. With the toggle off, only nodes whose dropdown was explicitly changed are acted on; untouched nodes are skipped entirely, so successive single-node canary upgrades never revert each other. The `opendeploy` and `opendeploy-net` groups are deliberately uncoupled: rolling one never touches the other.
- The deployment editor card has independent UI and HCL editor surfaces over one API-shaped authoring document. Valid HCL updates the shared document; invalid HCL is retained privately while the UI continues to show the last valid state. CodeMirror is loaded only when Code mode is first opened.
- Sidebar content is reused by the same `components/deploymentLogs.js` for prepare output and run output (switched by a mode flag), and `components/deploymentHistory.js` for the history view. All three show "Connection error" in the header on network failure.
- Deployment history (`components/deploymentHistory.js`) color-codes entries: green for stable running, grey for other status transitions, orange for config changes.

### Cluster (`pages/cluster.js`)
- Shows primary + worker machines and connection state, derived client-side from the state stream's `ClusterNode` and `ClusterNodeStatus` snapshots.
- Allows editing a machine's display name without changing its certificate or deployment identity.

Deployment node selectors render `ClusterNode.name` and submit `ClusterNode.id` as `DeploymentCreateRequest.nodeId`.

## Deployment editor

- `components/deploymentEditorWidget.js` owns the card shell, API actions, submission, and the persistent header/footer.
- `components/deploymentConfigUiWidget.js` and `components/deploymentConfigCodeWidget.js` are independent middle surfaces.
- `DeploymentCreationUpdate.document` is their shared API-shaped authoring boundary: `read()` returns identity, placement, spec, version, and desired state; `replace()` atomically applies a valid document.
- `components/deploymentSource.js` defines pure source keys, validation requests, response attestation, and local flake-path rules. Existing update overlays hydrate persisted-source choices with one `POST /v1/deployments/versions` request. Their persisted Nix repository and flake path remain frontend-trusted while unchanged, including across commit selection, branch discovery, refresh, and stopped-to-running transitions; full commit and local flake-path checks still apply, and the backend authoritatively verifies running updates before persistence. Creates and updates with an edited repository or flake path require exact frontend preflight validation. `DeploymentCreationUpdate` owns independently sequenced repository, branch-commit, exact Nix, image discovery, and persisted-version state.
- Nix branches are discovery filters rather than persisted deployment state. Changing branches refreshes the commit list without changing or revalidating the selected commit; when that commit is absent from the returned branch it remains available as an injected option until the user explicitly selects another commit.
- `components/deploymentHcl.js` implements the bounded HCL serializer/parser and resolves symbolic catalog references to API IDs. It does not evaluate general HCL expressions or interpolation.
- HCL places `node = node("name")` directly in the root `deployment` block; the API-shaped authoring document continues to expose the resolved placement as `nodeId`.
- `asset("name"[, { version = number, space = "name" }])`, `secret("name"[, { version = number, space = "name" }])`, and `config("name"[, { version = number, space = "name" }])` resolve names within the deployment's own space or the global space — names are only unique per space — with the deployment's own space shadowing a same-named global item and omitted versions selecting the latest version. The `space` option resolves in exactly that space (still restricted to own-or-global), and the serializer emits it whenever the referenced item lives outside the deployment's space so a later same-named local item cannot capture the reference. Code mode always emits pinned versions for existing deployments; create mode may omit the options object for a latest-version reference. The UI reference pickers apply the same own-or-global scoping and label every option `space / name`.
- HCL deployment references are globally addressable and always explicitly space-qualified: `address("space", "deployment")` resolves the target's stable inbound virtual-network address `I` and `deployment("space", "deployment")` resolves another deployment's default volume. The run-scoped preferred outbound address `O` is runtime-only and is never an authoring reference.
- Port forwarding uses `port_forward("tcp"|"udp", container_port[, { host_port = number }])`; the host port defaults to the container port, matching the options-object style used by `tls_passthrough`.
- Code mode commits only valid documents. Invalid source remains private to Code mode, allowing UI mode to continue from the last valid shared document.
- CodeMirror is dynamically imported on first use so the normal form path does not include it in the initial bundle.
- Use `pnpm run dev:deployment-editor` for the mock fixture and `pnpm run check:deployment-editor` for source-policy, codec, and payload round-trip checks.

## Rendering pattern

Components are plain functions returning DOM nodes created with `van.tags`. Reactive values are created with `van.state(...)` and read in closures.

```js
const msg = van.state("")
return div(
  { class: () => (msg.val ? "visible" : "hidden") },
  () => msg.val
)
```

## API usage

- API calls are centralized in `frontend/src/capi/capi.js` (generated).
- Pages import `capi` from `frontend/src/capi/index.js` and call methods with plain JS objects.
- The auth header is injected automatically from `loginS` state.

## Styling

- Tailwind CSS 4 via `@tailwindcss/vite`.
- Dark theme with custom colors defined in `frontend/src/style.css`: `--color-brand`, `--color-sidebar`, `--color-surface`, `--color-surface-hover`.
- Utility classes: `.text-input`, `.btn-primary`, `.btn-secondary`, `.card`.
