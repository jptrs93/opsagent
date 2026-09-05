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
- `components/sidebar.js` — left sidebar: the header, the grouped nav, and a footer row with the deployments stream status and a sign-out icon button. The nav renders from the model in `components/nav.js`: four groups ordered by how often an operator visits them — Workloads (Deployments, Secrets / Configs, Assets, Jobs, Policies, Charts), Observability (Logs, Metrics, Dashboards, Alerts), IAM (Users & roles, Sessions, Approvals), Cluster (Spaces, Nodes, Settings). Items marked `future` in the model do not exist yet: the sidebar tags them "soon" and `pages/planned.js` renders each as a single centred "planned future feature" message. A `badge` key on an item names a pending count the dashboard can pass in (`sidebar(activePage, {badges})`); nothing supplies one yet.
- Groups show their title only when collapsed. An open group has a slim chevron handle (its uppercase title fades in on hover) and a hairline separates it from the next group; collapsing it replaces the handle with the title row, which is what you click to reopen. Collapsed groups are remembered in `localStorage` (`opsagent_sidebar_collapsed_groups`), navigating to a page inside a collapsed group opens it, and a collapsed group shows the sum of its items' pending badges so an inbox is never hidden. Test ids: `nav-<page key>` on items, `nav-group-<group key>` on headers (with `aria-expanded`), `nav-sign-out` on the footer button.
- `pages/policies.js` — the Policies page: one page for every cross-space exception, a tab per resource kind. Network is `pages/networkPolicies.js` unchanged (page key `policies`, tab test ids `policies-tab-<kind>`); Secrets is planned.
- The main pane is split horizontally with a draggable divider (width persisted to `localStorage`). The right-hand pane shows the deployment history sidebar when a card action opens one.
- The active page is tracked via a `van.state` value. Clicking a sidebar item swaps the main content.

## Pages

### Login (`pages/login.js`)
- One centred card on a dot-grid backdrop: a header band (mark, "OpenDeploy", "Sign in"), the sign-in controls, and a footer band.
- Controls follow `authMethodsS`: the passkey button comes first; with password login enabled an "or" divider and the username/master-password form (a grouped field box with in-field captions) follow. Both buttons share one brand-tinted style. Discovery failure shows an amber notice with Retry and still offers the passkey button.
- The footer band shows the transport (plain HTTP, HTTPS, or HTTPS under the local CA) and the setup link. Under the local CA the transport row carries a "Trust the CA" button that opens `components/caTrustHelp.js`: a three-step overlay (get the certificate with download, copy and SHA-256 fingerprint; trust it per platform with the PEM inlined as a heredoc; restart).
- "Set up your passkey" navigates to `/bootstrap`.

### Bootstrap (`pages/bootstrap.js`)
- Two-step flow: master password entry, then passkey registration.
- Steps are tracked via a `van.state('password' | 'register')`.

### Deployments (`pages/deployments.js` and `pages/status.js`)
- `pages/deployments.js` owns a top tab bar: the status table is pinned left as "Overview", and every editor (Update on a row, Add deployment, Fork, restore from recently deleted) opens as a full-page tab. One update tab per deployment (re-opening focuses it), one tab per create; Cancel and a successful save close the tab and activate its neighbour; the × button, middle click, and Delete/Backspace close it too, asking first when the tab carries unsaved edits (an amber dot marks dirty tabs). Panels stay mounted while hidden so drafts survive switching, and `pages/dashboard.js` keeps the whole page mounted behind other pages so tabs survive a detour through Logs. The page takes an `actions` override for the editor's API calls; production uses `capi`.
- `pages/status.js` consumes live deployment state from `POST /v1/global/state-stream` (binary protobuf stream via `AsyncIterable<State>`) and renders one table row per deployment, sorted by OPENDEPLOY-last, then space, name, node, and id (deterministic across stream reconnects). It owns no deployment editor: `statusPage(onOpenLogs, {onUpdate, onCreate})` hands every editor open to the page hooks. Creates POST a typed `DeploymentCreateRequest` via `POST /v1/deployments/create`; updates submit a typed `DeploymentUpdateRequestV2` via `POST /v2/deployments/update`.
- Each row (`components/statusCard.js`) shows deployment, node, prepare and runtime details, audit metadata, and deployment actions. Status and Version are vertically split into one oldest-first subcell per non-final scheduled instance during rollovers; a candidate uses its pinned target version until its runner reports a version.
- The per-node internal `opendeploy` and `opendeploy-net` deployments are merged client-side into one group row each (`makeSystemGroups` in `pages/status.js`, rendered by `systemGroupStatusRow`), with Node, Status, Version, Prepare, Restarts, and audit cells split into one subline per node — secondaries first, primary last. Their Update action opens `components/openDeployGroupUpdateOverlay.js`: a per-node table of current and target release dropdowns with an "Align versions" toggle (on by default, the primary's dropdown drives all rows). On confirm the browser orchestrates the rollout itself via sequential `POST /v2/deployments/update` calls — secondaries one at a time, each waiting for the node's runner to report the new version through the state stream, primary last, halting on the first failure. With the toggle off, only nodes whose dropdown was explicitly changed are acted on; untouched nodes are skipped entirely, so successive single-node canary upgrades never revert each other. The `opendeploy` and `opendeploy-net` groups are deliberately uncoupled: rolling one never touches the other.
- The deployment editor has independent UI and HCL editor surfaces over one API-shaped authoring document. Valid HCL updates the shared document; invalid HCL is retained privately while the UI continues to show the last valid state. Code is the default surface; the last UI/Code choice persists per browser under the `localStorage` key `opsagent_deployment_editor_mode`. CodeMirror is loaded only when Code mode is first opened.
- Prepare output opens `components/prepareOutputOverlay.js`; run output navigates to the Logs page (`pages/logs.js`); the history view is the `components/deploymentHistory.js` sidebar. History and prepare output show "Connection error" on network failure.
- Deployment history (`components/deploymentHistory.js`) color-codes entries: green for stable running, grey for other status transitions, orange for config changes.

### Metrics (`pages/metrics.js`)
- With no deployment selected it is a live overview from `POST /v1/metrics/latest`: one row per running container (deployment, node, run, CPU cores, memory, network and disk rates, PIDs, TCP connections, sample age); clicking a row selects that deployment. Every column header sorts the table (text columns start ascending, numeric ones descending; missing values sort last); the choice persists in `localStorage`.
- With a deployment selected it queries `POST /v1/metrics/query` for the current time range and renders a grid of `components/lineChart.js` panels: CPU (total/user/system/throttled cores), memory (current/anon/file), network and disk rates, processes and file descriptors, TCP connection states, pressure stall averages, and per-bucket events (OOM kills, throttle periods, dropped packets). Series from several instance runs are summed per bucket by default; "Split by run" plots them separately. The page fetches once on load and when the deployment or time range changes; there is no automatic refresh, the Refresh button re-runs the query.
- `components/timeRangePicker.js` is the quick-preset plus custom-range dropdown shared with the Logs page; `components/lineChart.js` is a dependency-free SVG chart with unit-aware axes, a crosshair tooltip, and a legend that toggles series.
- `window.__metricsResult` and `window.__metricsLatest` mirror the last responses for e2e assertions, like the Logs page's `__logsResult`.

### Cluster (`pages/cluster.js`)
- Shows primary + worker machines and connection state, derived client-side from the state stream's `ClusterNode` and `ClusterNodeStatus` snapshots.
- Allows editing a machine's display name without changing its certificate or deployment identity.

Deployment node selectors render `ClusterNode.name` and submit `ClusterNode.id` as `DeploymentCreateRequest.nodeId`.

## Deployment editor

- `components/deploymentEditorWidget.js` owns the editor shell (`layout: 'page'` fills its host; the default frames a fixed-width card), API actions, submission, the footer, and an optional `dirty` state the page reads for its tab marker. A blank create seeds `deployment-<6 chars>` and the first visible space/node pair the allow list permits (`lib/nodeSpaces.js`), global space first.
- `components/deploymentUiWidget.js` and `components/deploymentCodeWidget.js` are independent middle surfaces. The UI surface renders the version as a read-only summary (branch · short id · message · date once the list entry is loaded, else the bare version); selection lives in the footer. The code surface's colour theme, font size, and keyword weight come from `components/deploymentHclTheme.js` (overridable through `theme`); layout stays in the widget. Syntax errors are ordinary diagnostics reading `Syntax invalid near "…"`.
- `DeploymentCreationUpdate.document` is their shared API-shaped authoring boundary: `read()` returns identity, placement, spec, version, and desired state; `replace()` atomically applies a valid document.
- Source validation is layered and human-triggered (`components/deploymentCreationUpdate.js`). The source tuple (type, repository, flake path, flake target, image) has one layer per field: repository, flake path, and target for Nix; image for images. Each layer is `trusted` (tuple unchanged from the saved deployment, whatever its running state), `unvalidated`, `checking`, `ok`, or `error`, and the overall state is their union. Typing never issues a request: every tuple change resets the layers and drops the version lists, and a repository, image, or source-type change also clears the selected version. The footer (`components/deploymentSourceFooter.js`) shows the overall state, a per-layer panel, and Validate; a version button opens a filterable dropdown (branch select plus commit rows for Nix, tag rows for images; the deployed version carries a "current" badge) beside Refresh. Validate checks image access and lists tags, or checks repository access, lists branches, and lists the commits of the selected branch (main, or the first branch, when none is selected; two requests the first time, one combined request once a branch is known), then checks the flake file at the selected commit. Picking a commit checks the flake file at it; the target layer applies local rules only (`.#` selector or empty), since the validate endpoint has no target check. A trusted source lists lazily on first dropdown open through `POST /v1/deployments/versions`, which also names the branch carrying the deployed commit. Validate and list requests are sequenced per kind and keyed on the tuple they were issued for; stale responses are dropped. `components/deploymentSource.js` defines the request builders, response attestation, and local flake-path rules.
- Save gating: a rejected layer blocks any save; running additionally requires the overall state to be valid or trusted and a selected version (a full 40-character commit for Nix); stopped saves need only structurally valid fields, and a stopped deployment may retarget its version (sent as a spec update with `running = false`, since the version-only update always starts the workload). The backend still verifies running Nix transitions before persistence.
- The Code-mode completion for the Nix `version = "…"` string, and for the tag typed after `image = "repository:`, lists the loaded versions; a footer pick in Code mode rewrites only that string or tag in the text. The source tuple keys the image on its repository with any tag stripped (the form stores the bare repository and a typed tag becomes the selected version), and the backend's same-source check compares repositories the same way, so a tag change is a version change and never invalidates a trusted source.
- `components/deploymentHcl.js` implements the bounded HCL serializer/parser and resolves symbolic catalog references to API IDs. It does not evaluate general HCL expressions or interpolation.
- HCL places `name = "…"` and `space = space("name")` directly in the root `deployment` block, with no identity wrapper. Placement and desired state form a `scheduling { node = node("name")  desired_running = true }` block. The workload version sits with its source: `nix_docker_build { … version = "<commit>" }`, while a container image is versioned by its reference alone, `container_image { image = "repository:tag" }` or `"repository@sha256:…"`, with no separate attribute. The parser splits the reference into the stored bare repository and the version, and the serializer joins them back, so a stored reference that still carries a tag renders the version once. A tagless image is valid only while `desired_running` is false. The API-shaped authoring document continues to expose the resolved placement as `nodeId`, the name and space as `identity`, and version and desired state on the container spec. A root `node` or `desired_running`, a container-level `version`, or a `version` inside `container_image`, yields a diagnostic naming the new location.
- `secret("space", "folder/name"[, version])`, `config("space", "folder/name"[, version])`, and `asset("space", "folder/key"[, version])` are fully qualified: the space is positional and required (the deployment's own space or the global space; anything else is a diagnostic), the second argument is the item's folder path within that space (names are unique per folder, so the path is the identity; a root-level item is its bare name), and the trailing integer pins a version, with an omitted version selecting the latest. Keyword arguments are not HCL, so the version stays positional. Code mode always emits pinned versions for existing deployments; create mode may omit the version for a latest reference. The previous `{ version, space }` options object yields a diagnostic naming the new shape. Completions offer spaces for the first argument, folder paths in that space for the second, and that item's versions for the third. The UI reference pickers apply the same own-or-global scoping and label every option `space / name`.
- HCL deployment references are globally addressable and always explicitly space-qualified: `address("space", "deployment")` resolves the target's stable inbound virtual-network address `I` and `deployment("space", "deployment")` resolves another deployment's default volume. The run-scoped preferred outbound address `O` is runtime-only and is never an authoring reference.
- The `network` section is block-form: `ingress { https { ... } tls_passthrough { ... } port_forward { ... } }`, every block naming ports as `container_port` / `host_port` (`port_forward` defaults `host_port` to `container_port`; `https` rejects it). Routes take repeated `listen { node = scheduled_node() | node("name") | any_node()  address = "ip" | ["ip", "cidr"] | ipv4() | ipv6() | any_address() }` blocks whose attributes default to the scheduled node and any address (`node("name")` must be the deployment's own node; another node is a diagnostic). The serializer omits defaults and emits `listen` only when set; the old call form yields one diagnostic naming the block form.
- Code mode commits only valid documents. Invalid source remains private to Code mode, allowing UI mode to continue from the last valid shared document.
- CodeMirror is dynamically imported on first use so the normal form path does not include it in the initial bundle.

## Rendering pattern

Components are plain functions returning DOM nodes created with `van.tags`. Reactive values are created with `van.state(...)` and read in closures.

```js
const msg = van.state("")
return div(
  { class: () => (msg.val ? "visible" : "hidden") },
  () => msg.val
)
```

VanJS ties every `van.derive` created while a reactive child or property
binding is being evaluated to the DOM that binding produces, and its garbage
collector drops those derives once that DOM leaves the document. A page that
must stay live while hidden (the deployments page behind the dashboard's page
switch) is therefore built outside the switching binding and only hidden with
a class; building it inside the binding would freeze its tables the first
time the user navigated away.

## API usage

- API calls are centralized in `frontend/src/capi/capi.js` (generated).
- Pages import `capi` from `frontend/src/capi/index.js` and call methods with plain JS objects.
- The auth header is injected automatically from `loginS` state.

## Styling

- Tailwind CSS 4 via `@tailwindcss/vite`.
- Dark theme with custom colors defined in `frontend/src/style.css`: `--color-brand`, `--color-sidebar`, `--color-surface`, `--color-surface-hover`.
- Utility classes: `.text-input`, `.btn-primary`, `.btn-secondary`, `.card`.
