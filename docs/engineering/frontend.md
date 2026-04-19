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
- Renders one card per deployment, sorted by OPSAGENT_SYSTEM-last, then environment, name, machine, and id (deterministic across stream reconnects).
- "Add deployment" button opens `components/createOverlay.js` to POST a per-deployment YAML via `POST /v1/deployment/create`.
- Each card (`components/statusCard.js`) shows status badge, deployment info (deployed by/at/version), runtime info (restarts/last restart), prepare status, and an Update button that opens `components/deployOverlay.js`. Running cards expose a stop icon; stopped cards with a known version expose a start icon.
- The deploy overlay fetches available versions on demand via `POST /v1/deployment/versions`, lets the user edit the per-deployment YAML spec, and submits via `POST /v1/deployment/update`.
- Sidebar content is reused by the same `components/deploymentLogs.js` for prepare output and run output (switched by a mode flag), and `components/deploymentHistory.js` for the history view. All three show "Connection error" in the header on network failure.
- Deployment history (`components/deploymentHistory.js`) color-codes entries: green for stable running, grey for other status transitions, orange for config changes.

### Cluster (`pages/cluster.js`)
- Shows primary + worker machines and connection state via `GET /v1/cluster/status`.

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
