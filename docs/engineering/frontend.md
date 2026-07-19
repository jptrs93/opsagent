# Frontend (VanJS)

## Overview

The UI is a single-page app using VanJS (`vanjs-core`) and Vite. Rendering is done by composing DOM nodes with VanJS tag helpers. State is managed through VanJS reactive state. Styling uses Tailwind CSS with a dark theme.

Key files:
- `frontend/src/app.js` — bootstraps the app and dispatches routes.
- `frontend/src/lib/router.js` — `currentPath` / `navigate` helpers over `popstate` + `history.pushState`.
- `frontend/src/state/login.js` — authentication state management.
- `frontend/src/state/deployments.js` — live state stream consumer (see `POST /v1/state/stream`).
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
- Consumes live deployment state from `POST /v1/state/stream` (binary protobuf stream via `AsyncIterable<State>`).
- Renders one card per deployment, sorted by OPENDEPLOY-last, then environment, name, machine, and id (deterministic across stream reconnects).
- "Add deployment" button opens `components/createOverlay.js` to POST a typed `DeploymentCreateRequest` via `POST /v1/deployment/create`.
- Each card (`components/statusCard.js`) shows status badge, deployment info (deployed by/at/version), runtime info (restarts/last restart), prepare status, and an Update button that opens `components/deployOverlay.js`. Running cards expose a stop icon; stopped cards with a known version expose a start icon.
- The deploy overlay fetches available versions on demand via `POST /v1/deployment/versions`, lets the user edit the deployment spec form, and submits a typed `DeploymentUpdateRequest` via `POST /v1/deployment/update`.
- The deployment editor card has independent UI and HCL editor surfaces over one API-shaped authoring document. Valid HCL updates the shared document; invalid HCL is retained privately while the UI continues to show the last valid state. CodeMirror is loaded only when Code mode is first opened.
- Sidebar content is reused by the same `components/deploymentLogs.js` for prepare output and run output (switched by a mode flag), and `components/deploymentHistory.js` for the history view. All three show "Connection error" in the header on network failure.
- Deployment history (`components/deploymentHistory.js`) color-codes entries: green for stable running, grey for other status transitions, orange for config changes.

### Cluster (`pages/cluster.js`)
- Shows primary + worker machines and connection state via `GET /v1/cluster/status`.
- Allows editing a machine's display name without changing its certificate or deployment identity.

Deployment machine selectors render `ClusterNode.name` but submit the immutable `ClusterNode.identifier` as `DeploymentIdentifier.machine`.

## Deployment editor

- `components/deploymentEditorWidget.js` owns the card shell, API actions, submission, and the persistent header/footer.
- `components/deploymentConfigUiWidget.js` and `components/deploymentConfigCodeWidget.js` are independent middle surfaces.
- `DeploymentCreationUpdate.document` is their shared API-shaped authoring boundary: `read()` returns identity, placement, spec, version, and desired state; `replace()` atomically applies a valid document.
- `components/deploymentSource.js` defines pure source keys, validation requests, response attestation, and local flake-path rules. Existing update overlays hydrate persisted-source choices with one `POST /v1/deployment/versions` request; source validation is deferred until a relevant source, selected version, flake path, or running-state change requires it. `DeploymentCreationUpdate` owns independently sequenced repository, branch-commit, exact Nix, image discovery, and persisted-version state; only a trusted persisted source or exact repository-wide commit and flake validation permits Running Nix submissions.
- Nix branches are discovery filters rather than persisted deployment state. Changing branches refreshes the commit list without changing or revalidating the selected commit; when that commit is absent from the returned branch it remains available as an injected option until the user explicitly selects another commit.
- `components/deploymentHcl.js` implements the bounded HCL serializer/parser and resolves symbolic catalog references to API IDs. It does not evaluate general HCL expressions or interpolation.
- HCL places `node = node("name")` directly in the root `deployment` block; the API-shaped authoring document continues to expose the resolved placement as `nodeId`.
- `asset("name"[, { version = number }])`, `secret("name"[, { version = number }])`, and `config("name"[, { version = number }])` resolve globally unique names, with omitted versions selecting the latest version. Code mode always emits pinned versions for existing deployments; create mode may omit the options object for a latest-version reference.
- HCL deployment references are globally addressable and always explicitly space-qualified: `address("space", "deployment")` resolves a virtual-network address and `deployment("space", "deployment")` resolves another deployment's default volume.
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
