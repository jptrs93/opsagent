# Deployments page tabs and editor footer: implementation plan

Status of the deployments page redesign: a top tab bar with the status table
pinned as Overview and every editor as a full-page tab, Code as the default
editor surface with a readable palette, and human-triggered layered source
validation with one shared version picker. The work started in a local design
fixture and is now integrated into the main path; this note records what
landed, how it is verified, and what is still open.

## Goals

- Replace the update and create overlays on the Deployments page with a top
  tab bar: Overview pinned left, one full-page tab per editor.
- Make Code mode the default editor surface, remember the UI/Code choice,
  and give the HCL editor a readable dark palette.
- Make source validation human-triggered, layered, and visible in the
  editor footer, with a single version picker that both modes share.

## Integrated (all uncommitted on `main`)

Page (`frontend/src/pages/`):

- `deployments.js` owns the tab strip (34px, fixed height, no overflow
  scrolling; crowded tabs shrink and truncate) and the panels. Overview is
  the pinned `statusPage`; Update, Add deployment, Fork, and restore from
  recently deleted open `deploymentEditorWidget` in `layout: 'page'`. One
  update tab per deployment (re-open focuses), one tab per create. Cancel and
  a successful save close the tab and activate the neighbour; ×, middle
  click, and Delete/Backspace close it after a confirm when the tab is dirty
  (amber dot). Panels stay mounted while hidden. `deploymentsPage(onOpenLogs,
  {actions})` takes an API override so a harness can run it in memory.
- `dashboard.js` keeps the deployments page mounted behind the other pages so
  open tabs survive a detour.
- `status.js` requires the `onUpdate` and `onCreate` hooks and owns no
  editor; `deployOverlay.js` and `createOverlay.js` are deleted. The
  `_system` group rows keep `openDeployGroupUpdateOverlay`.

Model (`components/deploymentCreationUpdate.js`):

- Layered validation over the source tuple (type, repository, flake, target,
  image): statuses `trusted`, `unvalidated`, `checking`, `ok`, `error`;
  overall as the union; trust when the tuple equals the persisted one,
  whatever the running state. Every tuple change resets the layers and drops
  the lists; a repository, image, or type change also clears the selection.
- Actions: `validate()` (image check plus tags; or repository plus branches,
  then the selected branch's commits (main or first when none), then the
  flake check at the selected commit; one combined listing request once a
  branch is known), `refreshVersions()`, `selectBranch()`,
  `selectVersion()` (runs the flake check for Nix), `ensureVersionsLoaded()`
  (lazy; a trusted update lists through `POST /v1/deployments/versions`).
  Requests are sequenced per kind and keyed on the tuple; stale responses
  are dropped. Actions sync the tuple first, because form edits reach the
  derive asynchronously.
- Gone: the blur-driven chain (`onRepositoryBlur`, `onImageBlur`, the flake
  debounce, the running-toggle trigger, the constructor's versions request),
  the Code-mode gate on in-flight requests and the request cancellation on
  mode switch, and with them the UI-form flash before Code mode.
- Save gating: a rejected layer blocks any save; running needs a valid or
  trusted source and a selected version; stopped saves need well-formed
  fields only, and a stopped deployment may retarget its version (spec
  update with `running = false`).
- Unit-tested as a pure module in `deploymentCreationUpdate.test.js`
  (statuses, tuple invalidation, trust, request shapes and counts,
  selection rules, stale responses, document replacement, images).

Editor (`components/`):

- `deploymentEditorWidget.js`: footer holds the mode toggle, the source
  status pill and layer panel with Validate, the version dropdown and
  refresh (`deploymentSourceFooter.js`), and a tinted submit button at the
  toolbar height. `dirty` state for the page; blank creates seed a
  placeholder name and placement. Code is the default mode; the choice
  persists per browser.
- `deploymentUiWidget.js`: the Version section is a read-only summary only;
  the selects and their Refresh are gone. `deploymentForm.js` source fields
  echo their layer's result and no longer trigger discovery.
- `deploymentCodeWidget.js`: `deploymentHclTheme.js` is the default colour
  theme (12px, keyword weight 500); `versionCompletions` come from the
  model's lists; a footer pick rewrites only the `version = "…"` string;
  syntax errors are ordinary diagnostics; `draftInvalid` feeds the dirty
  flag.
- `deploymentHcl.js`: flat identity (`node`, `name`, `space` at the root),
  no stopped-version constraint, "Version must be a full 40-character
  commit sha."

Tests and docs:

- e2e helpers (`testing-vms/e2e/helpers/ui.js`) look editors up as the
  visible tab panel, switch to UI mode explicitly, validate through the
  footer (asserting no request before Validate, two on Validate, one on the
  commit pick, one on refresh), and pick versions from the dropdown; image
  creates validate too. `access-enforcement.js` follows.
- `docs/engineering/frontend.md` and `docs/product/deployments.md` describe
  the page, the validation model, and the workflow.

## Verification

- `pnpm test` (115 cases) and `vite build` pass.
- A Playwright smoke run over the page with in-memory actions covered:
  create tab in Code by default with placeholder identity; UI mode fill,
  Validate (two requests), version pick with branch context, dirty marker,
  save closes the tab and the row appears; trusted image update with lazy
  listing and a version-only update; Code-mode footer pick rewriting the
  text; dirty × close asking and honouring dismiss/accept; Fork tab and
  Cancel.
- The full Lima e2e suite (`bash testing-vms/run.sh`, wipe and rebuild) is
  the acceptance run for the real backend; see the session report for its
  result.

## Still open

- Backend flake target check: the target layer applies local rules only
  (`.#` selector or empty). A flake evaluation at the commit behind a new
  validate flag would let it report existence; cost per Validate is the
  question.
- Stopped version retarget has unit coverage but no e2e flow: the mirror
  snapshot has a single commit, so there is no second version to pick.
- The page-layout form fills the full width (the overlay capped itself at
  1120px). Revisit if wide forms read badly on large screens.
- The UI/Code preference stays in `localStorage` per browser; revisit only
  if a server-side user preference store appears.
- The tab strip does not scroll; beyond roughly a dozen open tabs the titles
  truncate hard.

## Commit slicing

Groups that stand on their own:

1. Code widget: theme module and default palette, syntax diagnostics,
   activation wait, version completion source, `setWorkloadVersion`,
   `draftInvalid`.
2. HCL dialect: flat identity, message wording, placeholder node, test
   sample, frontend doc.
3. Model: layered validation, footer widgets, form field messages, UI
   version summary, editor footer and submit style, unit tests.
4. Create defaults: `nodeSpaces.js` helpers and tests, editor seeding.
5. Tabbed page: `pages/deployments.js`, dashboard mounting, status hooks,
   overlay deletion, dirty tracking.
6. e2e helpers and case update; product and engineering docs; this plan.

`frontend/src/pages/metrics.js` and the metrics paragraph in
`docs/engineering/frontend.md` carry unrelated uncommitted work (sortable
overview columns) and stay out of these commits. `frontend/fixtures/sidebar-tree/`
is an unrelated untracked fixture.
