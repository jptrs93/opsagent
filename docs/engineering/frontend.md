# Frontend (VanJS)

## Overview

The UI is a single-page app using VanJS (`vanjs-core`) and Vite. Rendering is done by composing DOM nodes with VanJS tag helpers. State is managed through VanJS reactive state. Styling uses Tailwind CSS with a dark theme.

Key files:
- `frontend/src/app.js` — bootstraps the app and dispatches routes.
- `frontend/src/lib/router.js` — `currentPath` / `navigate` helpers over `popstate` + `history.pushState`.
- `frontend/src/state/login.js` — authentication state management.
- `frontend/src/state/deployments.js` — live transport, reconnect and inactivity handling.
- `frontend/src/state/tree.js` — the mutation fold over `EventStreamMsg`: per-entity maps, the seq gate, the observed clock merge, and deployment version pruning.
- `frontend/src/state/derive.js` — VanJS view states derived after each entire transaction. Value pickers derive pins from the events that changed the value facet.
- `frontend/src/state/deploymentMerge.js` — deployment row derive, selecting live placements or the latest finalized incarnation per ordinal.
- `frontend/src/pages/` — page-level components.
- `frontend/src/components/` — reusable UI pieces.
- `frontend/src/capi/` — generated API client (`capi.js`, `model.js`) plus the stream decoder helper.

## Routing

Routing is handled by a small in-house module (`frontend/src/lib/router.js`)
exposing `currentPath` (a `van.state`) and `navigate(path)`. The route
table lives in `app.js`:
- `/enrol` (and the old `/bootstrap`) — enrol or recover a user with the master password.
- `/enrol/<token>` — where an enrolment or recovery link lands.
- `/` — dashboard (renders the login page when unauthenticated, and the enrol page for a root session, which is not a user).
- anything else — falls back to the login page.

On app load, `initLoginState()` restores the session token from `localStorage` and
validates it via `GET /v1/auth/current/session`; an invalid session is
cleared, so the user lands on the login page on the next render.

## Layout

The dashboard uses a split-pane layout:
- `components/sidebar.js` — left sidebar: the header, the grouped nav, and a footer row with the deployments stream status and a sign-out icon button. The nav renders from the model in `components/nav.js`: four groups ordered by how often an operator visits them — Workloads (Deployments, Secrets / Configs, Assets, Jobs, Policies, Charts), Observability (Logs, Metrics, Dashboards, Alerts), IAM (Users & roles, Sessions, Approvals), Cluster (Spaces, Nodes, Settings). Items marked `future` in the model do not exist yet: the sidebar tags them "soon" and `pages/planned.js` renders each as a single centred "planned future feature" message. A `badge` key on an item names a pending count the dashboard can pass in (`sidebar(activePage, {badges})`); nothing supplies one yet.
- Groups show their title only when collapsed. An open group has a slim chevron handle (its uppercase title fades in on hover) and a hairline separates it from the next group; collapsing it replaces the handle with the title row, which is what you click to reopen. Collapsed groups are remembered in `localStorage` (`opsagent_sidebar_collapsed_groups`), navigating to a page inside a collapsed group opens it, and a collapsed group shows the sum of its items' pending badges so an inbox is never hidden. Test ids: `nav-<page key>` on items, `nav-group-<group key>` on headers (with `aria-expanded`), `nav-sign-out` on the footer button.
- `pages/policies.js` — the Policies page: one page for every cross-space exception, a tab per resource kind. Network is `pages/networkPolicies.js` unchanged (page key `policies`, tab test ids `policies-tab-<kind>`); Secrets is planned.
- `pages/spaces.js` — the Spaces page, with the same tab bar: Spaces (the space list) and Domain policies (`pages/domainPolicies.js`). Tab test ids `spaces-tab-spaces` and `spaces-tab-domain-policies`.
- The main pane is split horizontally with a draggable divider (width persisted to `localStorage`). The right-hand pane shows the deployment history sidebar when a card action opens one.
- The active page is tracked via a `van.state` value. Clicking a sidebar item swaps the main content.

## Pages

### Auth card (`components/authCard.js`)
- The shell shared by the login, enrol and link landing pages: one centred card on a dot-grid backdrop with a header line (mark, "OpenDeploy", the page title), a body, and a footer band. It also exports the one brand-tinted button style both pages use, the grouped field box with in-field captions and the password reveal toggle, the toned notices (error, warning, success), the "or" divider, and the transport footer row (plain HTTP, HTTPS, or HTTPS under the local CA, with the "Trust the CA" button that opens `components/caTrustHelp.js`).

### Login (`pages/login.js`)
- The auth card titled "Sign in": the sign-in controls in the body, the transport row and the enrol link in the footer band.
- Controls follow `authMethodsS`: the username/password form (a grouped field box with in-field captions) comes first, then an "or" divider and the passkey button; with `requirePasskey` the passkey button stands alone. Both buttons share one brand-tinted style. Discovery failure shows an amber notice with Retry and still offers the password form. One `busy` flag covers both methods so a slow failing password request can never land after a passkey login.
- The footer band shows the transport (plain HTTP, HTTPS, or HTTPS under the local CA) and the setup link. Under the local CA the transport row carries a "Trust the CA" button that opens `components/caTrustHelp.js`: a three-step overlay (get the certificate with download, copy and SHA-256 fingerprint; trust it per platform with the PEM inlined as a heredoc; restart).
- "Enrol or recover a user" navigates to `/enrol`.

### Enrol (`pages/enrol.js`)
- The auth card titled "Enrol or recover" with a three-step strip (master password, enrol or recover, done). Step one is the master password box; `enrolActions.verifyMasterPassword` (`state/enrol.js`) exchanges it for a root session through `setLoginFromResponse`, which also opens the event stream (`hasStateStreamAccess` admits any login; the server sends a root session only users, spaces, templates and grants). The widget `components/userEnrol.js` is then built, not before: VanJS drops the bindings of DOM that sits disconnected for about a second. A reload with a live root session skips straight to the widget.
- `state/enrol.js` is the widget's server seam, shared with the Users page: `createUser` posts to `/v1/users/create` and reads the new user out of the `CoreWriteUpdate` receipt, `createLink` turns the `/v1/users/link` token into `<origin>/enrol/<token>`, and `signIn` (root only) opens a session for the user. `enrolData` reads the lists straight from the stream stores.
- "Back to login" and Done clear a root login before navigating, so a root token never lingers in `localStorage`.

### Link landing (`pages/enrolLanding.js`)
- Redeems the token from the URL once (`POST /v1/auth/link/redeem`), stores the session, then asks for whatever the response says is missing: a username, then a password; offers a passkey (skippable) when the browser supports them; then navigates home. A spent or expired link shows the reason and a "Go to login" link. If a full login already exists in the browser the page reads `/v1/account` instead of redeeming, so a reload mid-setup continues rather than burning the link.

### Account (`state/account.js`, `components/accountPanel.js`)
- The You tab on Users & roles. `loadAccount` fetches `/v1/account` into `accountS`; `accountSelf` joins it with the user entity from the stream for the joined date and pending state. Passkey ids arrive as bytes and are kept both raw (for delete) and base64url (for keys and test ids). `registerPasskey` runs the WebAuthn ceremony and is shared with the landing page.

### Global state

`state/tree.js` owns normalized maps and one pure fold, `applyMessage(tree,
msg)`, over the `EventStreamMsg`s of `POST /v1/global/event-stream`;
`state/derive.js` projects the maps into the page view models after each whole
message. The opening message carries a `snapshot`, which `applySnapshot`
folds into an empty tree one `MaterialisedEntity` at a time as a create
under the envelope its `meta` carries (`updatedSeq`, `updatedTime`,
`updatedActor`), adding a tombstone after the last entry of a deployment
whose entries carry `meta.deleted`. Each live `CoreWriteUpdate` folds by
mutation with the commit's envelope and the mutation's `meta`; every
derived fact the tree holds (`version`, `specVersion`, `valueVersion`,
`createdTime`, `author`) comes from that meta, never from the entity.
Deployments keep a version map per id keyed by `meta.version` (a delete
mutation becomes a tombstone entry one version above the latest, so pinned
versions survive until their instances finalize); secrets, configs, and
assets keep their history oldest first, and `valueVersions` picks the
entries whose `valueVersion` changed; nodes, instances, network policies,
and grants keep the latest entity wrapped with the commit's `seq`,
`eventTime`, and `author`; spaces, users, directories, sessions, rule
templates, and global rules are plain latest-entity maps; the system config
is the latest entity plus its `seq`, and space domain policies are a
latest-entity map that keeps the `seq` too, derived in id order as
`spaceDomainPoliciesS`. Entity types the browser does not fold (nix store
resets, secret keyslots) are skipped. Observed statuses merge by their nanosecond
`updatedAt` (the generated Date's `epochNanoseconds`), not by seq. A status may
precede its parent inside one message, so orphans are swept once the message
has folded rather than refused on arrival. Tombstones clear the visible entry
while retaining its clock until the parent goes away. An event at or below
`tree.seq` is a replay and is dropped; every message's `seq`, heartbeats
included, advances `tree.seq`. A snapshot clears every map before it folds,
on the first message and whenever the server resends one after a visibility
change; the sidecars (backup, secrets status, ingress diagnostics) survive it
and replace only when present. Deployment retention uses the same placement selector as the view:
live pins and the newest final run retain their referenced versions, with
obsolete versions pruned when those pins disappear.

The stream client in `state/deployments.js` sends an empty request (there
is no resume token: every connection opens with a snapshot), reconnects
after overflow or a lost heartbeat, resets the tree silently when the
session token changes so the new session opens afresh, and clears
everything on logout. The runner's `runningVersion` is not on the wire:
`deploymentMerge.js` fills it from the instance's pinned config when the
runner reports that config's spec version.

Agent sessions and user sessions are latest-only maps like spaces and users,
already owner-filtered by the server, and derive to `agentSessionsS` and
`userSessionsS` newest first; the sessions page marks the row whose id matches
`loginS.sessionId`. Grant state follows the fold: `AUTHZ_GRANT` mutations
populate a map keyed by entity id and delete mutations remove the entry; a
visibility change replaces the map with the fresh snapshot's live grants.

Concurrency tokens: every write that guards against a concurrent change
(`/v1/deployments/update`, delete, evict, enrollment accept, network policy
update, space domain policy update, and the referencing-deployment lists of
secret and config sets) sends
`expectedSeq`, the `seq` of the entity's last mutation as the page held it.
Reveal is by `{secretId, version}` and asset content by `asset_id` plus
`version`.

### Deployments (`pages/deployments.js` and `pages/status.js`)
- `pages/deployments.js` owns a top tab bar: the status table is pinned left as "Overview", and every editor (Update on a row, Add deployment, Fork, restore from recently deleted) opens as a full-page tab. One update tab per deployment (re-opening focuses it), one tab per create; Cancel and a successful save close the tab and activate its neighbour; the × button, middle click, and Delete/Backspace close it too, asking first when the tab carries unsaved edits (an amber dot marks dirty tabs). Panels stay mounted while hidden so drafts survive switching, and `pages/dashboard.js` keeps the whole page mounted behind other pages so tabs survive a detour through Logs. The page takes an `actions` override for the editor's API calls; production uses `capi`.
- `pages/status.js` consumes live deployment state from `POST /v1/global/event-stream` (binary protobuf stream via `AsyncIterable<EventStreamMsg>`) and renders one table row per deployment, sorted by OPENDEPLOY-last, then space, name, node, and id (deterministic across stream reconnects). It owns no deployment editor: `statusPage(onOpenLogs, {onUpdate, onCreate})` hands every editor open to the page hooks. Creates POST a typed `DeploymentCreateRequest` via `POST /v1/deployments/create`; updates submit a typed `DeploymentUpdateRequest` (one `update` alternative) via `POST /v1/deployments/update`.
- Each row (`components/statusCard.js`) shows deployment, node, prepare and runtime details, audit metadata, and deployment actions. Status and Version are vertically split into one oldest-first subcell per non-final scheduled instance during rollovers; a candidate uses its pinned target version until its runner reports a version.
- The per-node internal `opendeploy` and `opendeploy-net` deployments are merged client-side into one group row each (`makeSystemGroups` in `pages/status.js`, rendered by `systemGroupStatusRow`), with Node, Status, Version, Prepare, Restarts, and audit cells split into one subline per node — secondaries first, primary last. Their Update action opens `components/openDeployGroupUpdateOverlay.js`: a per-node table of current and target release dropdowns with an "Align versions" toggle (on by default, the primary's dropdown drives all rows). On confirm the browser orchestrates the rollout itself via sequential `POST /v1/deployments/update` calls — secondaries one at a time, each waiting for the node's runner to report the new version through the state stream, primary last, halting on the first failure. With the toggle off, only nodes whose dropdown was explicitly changed are acted on; untouched nodes are skipped entirely, so successive single-node canary upgrades never revert each other. The `opendeploy` and `opendeploy-net` groups are deliberately uncoupled: rolling one never touches the other.
- The deployment editor has independent UI and HCL editor surfaces over one API-shaped authoring document. Valid HCL updates the shared document; invalid HCL is retained privately while the UI continues to show the last valid state. Code is the default surface; the last UI/Code choice persists per browser under the `localStorage` key `opsagent_deployment_editor_mode`. CodeMirror is loaded only when Code mode is first opened.
- Prepare output opens `components/prepareOutputOverlay.js`; run output navigates to the Logs page (`pages/logs.js`); the history view is the `components/deploymentHistory.js` sidebar. History and prepare output show "Connection error" on network failure.
- Deployment history (`components/deploymentHistory.js`) color-codes entries: green for stable running, grey for other status transitions, orange for config changes.

### Metrics (`pages/metrics.js`)
- With no deployment selected it is a live overview from `POST /v1/metrics/latest`: one row per running container (deployment, node, run, CPU cores, memory, network and disk rates, PIDs, TCP connections, sample age); clicking a row selects that deployment. Every column header sorts the table (text columns start ascending, numeric ones descending; missing values sort last); the choice persists in `localStorage`.
- With a deployment selected it queries `POST /v1/metrics/query` for the current time range and renders a grid of `components/lineChart.js` panels: CPU (total/user/system/throttled cores), memory (current/anon/file), network and disk rates, processes and file descriptors, TCP connection states, pressure stall averages, and per-bucket events (OOM kills, throttle periods, dropped packets). Series from several instance runs are summed per bucket by default; "Split by run" plots them separately. The page fetches once on load and when the deployment or time range changes; there is no automatic refresh, the Refresh button re-runs the query.
- `components/timeRangePicker.js` is the quick-preset plus custom-range dropdown shared with the Logs page; `components/lineChart.js` is a dependency-free SVG chart with unit-aware axes, a crosshair tooltip, and a legend that toggles series.
- `window.__metricsResult` and `window.__metricsLatest` mirror the last responses for e2e assertions, like the Logs page's `__logsResult`.

### Logs (`pages/logs.js`)
- The search bar is, left to right: the spaces filter, the deployment select, `components/logScopePicker.js` (config version, instance, run), `components/logLevelPicker.js`, the query input, `components/timeRangePicker.js` and Search. Every control except the query input re-runs the search on change.
- Levels are six fixed buckets from `lib/logLevels.js`: DEBUG, INFO, WARN, ERROR, OTHER (any other parsed level, e.g. TRACE or FATAL) and NONE (no parsed level). They match the histogram series the backend returns. The level picker is a multi-select over them; `logLevelFilters` turns the selection into wire filters (an `in` over the selected levels, or `neq` exclusions plus `exists` when OTHER is on, since filters only AND). The legend under the status line is read-only: swatch, label and total per bucket, dimmed when the picker has that level off; OTHER and NONE appear only when the result holds such lines.
- `window.__logsResult` mirrors the last response for e2e assertions.

### Nodes (`pages/cluster.js`)
- Three pinned tabs in the Users & roles strip. **Nodes** is a dense table of the enrolled nodes from `machinesS` (the stream's `Node` and `NodeStatus` entities merged in `state/derive.js`): name with a `primary` tag, a status dot and word (`draining` replaces the word in amber; the dot keeps the connection state), cluster address, host addresses, allowed spaces, agent version, runtime versions, and connected since. Lists collapse to a count (`3 addresses`, `2 spaces`, `all`), every cell truncates with an ellipsis, and the full value rides on the cell's title; host addresses read `unknown` when the node reported `host_addresses_unknown` with an empty list. A toolbar search filters by name, identifier, address or state; evicted nodes sit behind an `Evicted (n)` toggle and render muted at the bottom.
- Clicking a row opens the **inspector** on the right (resizable with the `.vgrip`, closed with × or by clicking the row again): the name as an `inlineEditableInput`, identifier, connected and enrolled times, every address with a copy button, the full transport key, agent and runtime versions, the allowed-spaces editor (checkboxes; `_system` always ticked and locked), and Drain / Evict for secondaries (the evict dialog is `components/evictNodeOverlay.js`). The inspector rebuilds on every store update; the spaces draft lives at page level so an edit in progress survives one.
- **Enrollments** lists the pending requests (request id and machine id, remote IP, agent version, requested, and an accept-as name with the Accept button; Enter accepts) and, under its own band, the secondary install command built from the cluster listen settings, the enrollment fingerprint from `/v1/nodes/enrollments/info`, and the primary's self-deployment version, with a Copy button.
- **Replication** shows `backupStatusS`: the status badge and a sentence, tiles for local and remote TXID, lag, and last successful sync, then the pending-assets notice and any database or asset error. The tab's dot mirrors the badge tone. The fixture in `fixtures/nodes-redesign/` mounts the page over an in-memory node API.

Deployment node selectors render `Node.operator.name` and submit the node's entity id in `scheduling.dedicated_nodes`.

### Spaces (`pages/spaces.js` and `pages/domainPolicies.js`)
- Two tabs. **Spaces** is the space list with its add and delete dialogs.
- **Domain policies** lists `spaceDomainPoliciesS` in id order: an allow or deny badge, the name, the spaces and the domains the rule covers, and an enable switch. A disabled row is muted and tagged "off". The row grid stacks under a container query, so the same markup serves the narrow layout. Above the list sit a "Check a hostname" helper (a space and a hostname, answered from the visible policies), one line on evaluation order and wildcards, and an amber notice while an enabled allow covers every space and every domain and there is more than one user space.
- The editor dialog takes a name, the effect, a spaces position (these spaces, or all except; `_system` is offered only when the policy already names it), a domains position (these domains, or all except; one per line or comma separated) and Enabled. It shows the rule as a sentence and keeps Save disabled while the draft has a problem. An exact list must not be empty; an empty "all except" list means all.
- The switch is not optimistic: it sends the whole policy with `disabled` flipped and `expectedSeq`, and moves when the stream delivers the update. A refused toggle, save or delete shows the server's `displayErr` unchanged next to the control, which is where the "would leave published hostnames without permission" refusal appears.
- The model is `lib/domainPolicies.js`, pure and unit tested: the effect and selector shapes, the summaries, domain validation (`canonicalDomain`, `parseDomains`), `decide` (the server's order: enabled denies, then enabled allows), the draft and the three request payloads.
- Test ids: `domain-policy-row-<id>`, `domain-policy-toggle-<id>`, `domain-policy-edit-<id>`, `domain-policy-delete-<id>`, `domain-policy-add`, `domain-policy-editor`, `domain-policy-save`, `domain-policy-delete-confirm`, `domain-policy-error`. The fixture in `fixtures/domain-policies/` (`pnpm run dev:domain-policies`) mounts the page over an in-memory API that refuses what the server refuses.

### Settings (`pages/settings.js`)
- One flush surface in the explorer pages' style: a toolbar on the 30px baseline (a search box that filters rows by label or section title, and once anything is dirty an amber "Unsaved changes" count with Reset and Save changes), a status line (the one line of help, or an amber warning when the cluster settings changed elsewhere while the draft is dirty), then one dense table per group — Web UI, Authentication, Cluster, Repository credentials, Backup, Secrets recovery — with the group name in a merged cell down the left, centred beside its rows, and a single rule centred in the gap between one group and the next; nothing collapses and there are no lines between rows. Columns are label, source, value. Rows are `tr`s with `data-testid="setting-row-<KEY>"`. Rows a bool hides (the HTTP listen without HTTP, the backup S3 fields without backups, the separate large-asset S3 fields) stay mounted under `hidden` so a typed draft survives; a group whose rows a search leaves empty hides with them, and the first group still showing carries no rule above it.
- Source is a Value / Config select (a secret setting is always a reference and shows a secret badge instead); the value cell holds the typed text or a toggle under Value and the config picker under Config, capped at 26rem. A dirty row gets an amber dot before its label, an amber tint, and a "was …" hint after its control; a clean row following a config shows what it resolves to ("→ …") instead, since that is what decides the dependent rows' visibility. Secret rows carry icon buttons to open the secret editor, clear the reference, and create a secret (the `valueOverlay` dialogs).
- Master password is a row of the Authentication section: Verify… and Change… open dialogs (Change… can generate a password and copy it). Secrets recovery has the recovery code row — status from the stream's `secretsStatusS`, which the rotate and unlock handlers publish, so nothing is fetched — and the restore command as a collapsible `codeBlock` filled from the current settings. A freshly generated recovery code opens a dialog that is dismissed with "I've saved it"; the code is shown once and stored nowhere.

### Users & roles (`pages/users.js`)
- Two pinned tabs in the Deployments page's strip: **Users** (the roster with each user's grants as chips, a revoke × or a padlock where `grantRevokeBlock` refuses, and a dashed + that opens the grant dialog) and **Roles & rules** (the roles table with a Granted to column, then the global rules, under collapsible bands; one search filters both). The editor dialogs are `components/accessEditors.js`; rule pills are `components/ruleDisplay.js`, built with `titles: false` here because the explainer replaces the native tooltips.
- Every rule pill, role name and grant chip opens the **rule explainer** (`components/ruleExplainer.js`): hover shows it after 220ms and moving off closes it; a click pins it until the × in its corner, Esc, a click elsewhere or a second click; one card at a time; a pinned card follows its row while the table scrolls and rings its trigger (`.explain-open` / `.explain-pinned` in `style.css`). The card sits below the modal overlays and closes whenever the page opens one. The layer is appended to `document.body` once by `mountExplainerLayer()`, which the page calls on mount.
- The card is split under a one-line header naming the subject ("Rule 2 of space_admin", "Global rule no_host_network", "Role space_admin", or the grant with its bound arguments). **Definition** (left) shows the rule as written: Effect, then Actions, Resources, Instances, Spaces as tokens over each position's universe (covered in blue, everything else dimmed) or the open argument, and Sessions as Users and Agents badges. A role or role grant with several rules gets a Rule 1, 2, 3 strip over the pane. **Access table** (right; "Access table (All rules)" when it is the union of several) has one tab per space, spaces with identical tables sharing one ("Any space" when that is every real space), a System tab first for anything checked in `_system`, and for a role whose spaces are an open argument a `${spaces}` tab plus a System tab led by "Only applies when `_system` is one of the `${spaces}` arguments." Each tab lists only the action × resource pairs that can apply there; a cell shows a person icon (allowed in person sessions), an agent icon (allowed in agent sessions), both, a dot (not affected), grey (never applies there) or `?` (depends on an argument), red for a deny rule. Instance-scoped rules carry a numbered marker into a legend; a readout line under the table names the hovered pair, what the permission grants and the cell's state; a key lists the glyphs; System tabs describe the space under the key.
- The model is `lib/authzExplain.js`, pure and unit tested: `resolveSelector` and `ruleDefinition` for the definition pane; `explainMatrix` and `mergeGrid` for the tables; `APPLICABLE`, `SYSTEM_APPLICABLE` and `SPACE_APPLICABLE` name the pairs the API checks and where (see [auth.md](auth.md)); `PAIR_GLOSS` says what each pair grants. The fixture in `fixtures/users-redesign/` mounts the page over an in-memory access API.
- **Enrol or recover** (`components/userEnrol.js`) is how users are created: one widget reached either behind the master password on the auth card (`pages/enrol.js`) or from the page's toolbar as a dialog (`usersPage({enrol})`, built by the dashboard over `state/enrol.js`), with two pathways. *Enrol a new user* takes a username (optional when the invitee picks it from the link), a password (chosen from the link by default, or set now with a generator) and a list of grants built with the exported `grantEditor`, then produces a single-use sign-in link or, with a password set, signs in directly (login host) or creates the pending user (Users page). *Recover an existing user* picks the account from a searchable list, shows its grants read-only, and produces a recovery link or signs in as them (login host only). The fixture in `fixtures/user-enrol/` runs both hosts over the users fixture's in-memory API. The link lands on `pages/enrolLanding.js`; a user is pending (amber tag) until `activatedAt` is set by its first session.
- **You** (`components/accountPanel.js`, over `state/account.js`) is a third tab the page shows when given `account`: the signed-in user's own account in three blocks. *Identity* renames with the `inlineEditableInput` (Enter saves, Escape discards, a collision with another user's name warns and blocks). *Sign in* has the password row (Set or Not set, with a Change or Set dialog that asks for the current password only when one exists and can generate the new one), the passkey list with add and remove (the remove control locks on the only passkey of an account with no password, so nobody strips their last way in), and a Manage sessions hand-off to the Sessions page. *Your access* lists the user's grants as explainable chips, read-only. The same fixture drives it, with a "You" switch for the signed-in user.

## Deployment editor

- `components/deploymentEditorWidget.js` owns the editor shell (`layout: 'page'` fills its host; the default frames a fixed-width card), API actions, submission, the footer, and an optional `dirty` state the page reads for its tab marker. A blank create seeds `deployment-<6 chars>` and the first visible space/node pair the allow list permits (`lib/nodeSpaces.js`), global space first.
- `components/deploymentUiWidget.js` and `components/deploymentCodeWidget.js` are independent middle surfaces. The UI surface renders the version as a read-only summary (branch · short id · message · date once the list entry is loaded, else the bare version); selection lives in the footer. The code surface's colour theme, font size, and keyword weight come from `components/deploymentHclTheme.js` (overridable through `theme`); layout stays in the widget. Syntax errors are ordinary diagnostics reading `Syntax invalid near "…"`.
- `DeploymentCreationUpdate.document` is their shared API-shaped authoring boundary: `read()` returns identity, placement, spec, version, and desired state; `replace()` atomically applies a valid document.
- Source validation is layered and human-triggered (`components/deploymentCreationUpdate.js`). The source tuple (type, repository, flake path, flake target, image) has one layer per field: repository, flake path, and target for Nix; image for images. Each layer is `trusted` (tuple unchanged from the saved deployment, whatever its running state), `unvalidated`, `checking`, `ok`, or `error`, and the overall state is their union. Typing never issues a request: every tuple change resets the layers and drops the version lists, and a repository, image, or source-type change also clears the selected version. The footer (`components/deploymentSourceFooter.js`) shows the overall state, a per-layer panel, and Validate; a version button opens a filterable dropdown (branch select plus commit rows for Nix, tag rows for images; the deployed version carries a "current" badge) beside Refresh. Validate checks image access and lists tags, or checks repository access, lists branches, and lists the commits of the selected branch (main, or the first branch, when none is selected; two requests the first time, one combined request once a branch is known), then checks the flake file at the selected commit. Picking a commit checks the flake file at it; the target layer applies local rules only (`.#` selector or empty), since the validate endpoint has no target check. A trusted source lists lazily on first dropdown open through `POST /v1/deployments/versions`, which also names the branch carrying the deployed commit. Validate and list requests are sequenced per kind and keyed on the tuple they were issued for; stale responses are dropped. `components/deploymentSource.js` defines the request builders, response attestation, and local flake-path rules.
- Save gating: a rejected layer blocks any save; running additionally requires the overall state to be valid or trusted and a selected version (a full 40-character commit for Nix); stopped saves need only structurally valid fields, and a stopped deployment may retarget its version (sent as a spec update with `running = false`, since the version-only update always starts the workload). The backend still verifies running Nix transitions before persistence.
- The Code-mode completion for the Nix `version = "…"` string, and for the tag typed after `image = "repository:`, lists the loaded versions; a footer pick in Code mode rewrites only that string or tag in the text. The source tuple keys the image on its repository with any tag stripped (the form stores the bare repository and a typed tag becomes the selected version), and the backend's same-source check compares repositories the same way, so a tag change is a version change and never invalidates a trusted source.
- `components/deploymentHcl.js` implements the bounded HCL serializer/parser and resolves symbolic catalog references to API IDs. It does not evaluate general HCL expressions or interpolation.
- `dsl/` is the formal two-layer model the editor is meant to converge on. The type definition language is the e2edesign domain model language minus tags, laws, and field metadata (`typeParser.js`, `schema.js`); `deployment.js` holds the Deployment type ported from `e2edesign/examples/opendeploy/deployment.dm` and must be kept in step with it. The code language is a projection of the type (nested types are blocks, unions hold one alternative, scalars are attributes); `evaluate.js` runs the functions declared with `defineFunctions` on submission to produce a dense instance, `print.js` renders it back with zero-valued fields left out, and the laws and constraints the type language drops live in `deploymentRules`. The user-facing description is the docs site page Design › Configuration code, and `src/dsl/README.md` maps the code. Not yet wired into the editor.
- HCL places `name = "…"` and `space = space("name")` directly in the root `deployment` block, with no identity wrapper. Placement and desired state form a `scheduling { running = true  dedicated_nodes { nodes = [node("name")] } }` block that mirrors the API's `Scheduling` message. The workload version sits with its source: `nix_docker_build { … version = "<commit>" }`, while a container image is versioned by its reference alone, `container_image { image = "repository:tag" }` or `"repository@sha256:…"`, with no separate attribute. The parser splits the reference into the stored bare repository and the version, and the serializer joins them back, so a stored reference that still carries a tag renders the version once. A tagless image is valid only while `running` is false. The API-shaped authoring document exposes the name and space as `identity`, the resolved placement and running state as `scheduling`, and the version on the container spec. A `node` or `desired_running` attribute from the earlier dialect, a container-level `version`, or a `version` inside `container_image`, yields a diagnostic naming the new location.
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
time the user navigated away. The settings page is mounted the same way, for
two reasons: it reads its draft while building its rows, so inside the switch
every edit would rebuild the page and reset the draft; and caching a detached
node instead (as it once did) lost its derives to the collector, leaving the
page stuck on "Loading..." after a sign-out.

## API usage

- API calls are centralized in `frontend/src/capi/capi.js` (generated).
- Pages import `capi` from `frontend/src/capi/index.js` and call methods with plain JS objects.
- The auth header is injected automatically from `loginS` state.

## Styling

- Tailwind CSS 4 via `@tailwindcss/vite`.
- Dark theme with custom colors defined in `frontend/src/style.css`: `--color-brand`, `--color-sidebar`, `--color-surface`, `--color-surface-hover`.
- Utility classes: `.text-input`, `.btn-primary`, `.btn-secondary`, `.card`.
