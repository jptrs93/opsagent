# Frontend (VanJS)

## Overview

The UI is a single-page app using VanJS (`vanjs-core`) and Vite. Rendering is done by composing DOM nodes with VanJS tag helpers. State is managed through VanJS reactive state. Styling uses Tailwind CSS with a dark theme.

Key files:
- `frontend/src/app.js` — bootstraps the app and registers routes.
- `frontend/src/state/login.js` — authentication state management.
- `frontend/src/pages/` — page-level components.
- `frontend/src/components/` — reusable UI pieces.

## Routing

Routes are defined in `app.js` using `vanjs-routing`:
- `/login` — passkey login page.
- `/bootstrap` — first-time setup (master password then passkey registration).
- `/` — dashboard (requires authentication, redirects to `/login` if not logged in).

On app load, `initLoginState()` restores the JWT from `localStorage`. If no valid token exists and the user is at `/`, they are redirected to `/login`.

## Layout

The dashboard uses a sidebar + main content layout:
- `components/sidebar.js` — left sidebar with navigation items under "Deployments": Config and Status.
- The active page is tracked via a `van.state` value. Clicking a sidebar item swaps the main content.

## Pages

### Login (`pages/login.js`)
- Single "Sign in with passkey" button.
- "First time setup" link navigates to `/bootstrap`.

### Bootstrap (`pages/bootstrap.js`)
- Three-step flow: master password entry, passkey registration, completion.
- Steps are tracked via a `van.state('password' | 'register' | 'done')`.

### Config (`pages/config.js`)
- CodeMirror YAML editor with `oneDark` theme.
- Save button calls `PUT /v1/config` with raw YAML content.
- Version history sidebar (`components/configHistory.js`) shows all saved versions. Clicking a version restores its YAML into the editor.

### Status (`pages/status.js`)
- Consumes live deployment state from `POST /v1/state/stream`.
- Groups deployments by environment and renders one card per deployment.
- Loads scopes via `POST /v1/list/scopes` and versions via `POST /v1/list/versions`. Scopes are preparer-specific (git branches for nix, empty for github release); versions are commit hashes or release tags.
- Each card (`components/statusCard.js`) shows status badge, deployment info (deployed by/at/version), runtime info (restarts/last restart), prepare status, scope selector, version dropdown, and deploy button.
- Sidebar panels for prepare output (`components/prepareOutput.js`), run output (`components/runOutput.js`), and deployment history (`components/deploymentHistory.js`). All show "Connection error" in the header on network failure.
- Deployment history (`components/deploymentHistory.js`) color-codes entries: green for stable running, red for crashes, orange for user actions.

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
