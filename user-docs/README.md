# User Documentation

Networking and logging documentation built with SvelteKit, Svelte 5, and
`adapter-static`. The output is a complete static site; no application server
is needed in production.

## Commands

Run these commands in `user-docs/` with Node.js 22.12 or later and pnpm:

```sh
pnpm install
pnpm dev
```

To check and build the site:

```sh
pnpm check
pnpm build
pnpm preview
```

Publish the contents of `build/` to a static host with directory-index support.
The site includes `/`, `/networking/`, `/logging/`, and the existing
`/data-model/index.html` reference. The old `/networking.html` and `/logging.html`
URLs redirect to the corresponding article. With JavaScript enabled, these
redirects also retain query strings and anchors.

Run checks and builds sequentially: both SvelteKit commands write generated
files under `.svelte-kit/`.

## Editing

- Articles: `src/routes/networking/+page.svelte` and `src/routes/logging/+page.svelte`.
- Shared navigation and layout: `src/routes/+layout.svelte`.
- Article styles: `src/app.css`.
- Code examples: `src/lib/networking-snippets.ts` and `src/lib/logging-snippets.ts`.
- Log-event definitions: `src/lib/log-event.ts`, with Protobuf, Smithy, and TypeScript tabs.
- Code rendering: `CodeBlock.svelte`, `CodeTabs.svelte`, and `languages.ts` in `src/lib/`.

CodeMirror is loaded in the browser and configured read-only. Prerendered HTML
contains each code example as plain text, so the articles remain readable without
JavaScript or if the editor fails to load. Tabs support keyboard navigation, and
copy buttons copy the selected representation. Clipboard access requires HTTPS
or localhost; on unsupported origins, the source can still be selected manually.

The schema tabs describe the same logical event model, not interchangeable wire
encodings. TypeScript uses `bigint` for 64-bit integers and nanosecond timestamps;
Smithy uses constrained integer shapes for unsigned values and native lists
for arrays; TypeScript uses array types directly. Protobuf retains the wrapper
messages needed for arrays inside maps. HCL and Smithy
highlighting uses display lexers, not validators.

The standalone data-model viewer remains sourced from `data-model/`. Dev and
build commands copy it into the ignored `static/data-model/` directory; edit the
original files rather than that generated copy.

## Browser Tests

Tests require Python 3 for a plain static HTTP server and Playwright's Chromium:

```sh
pnpm exec playwright install chromium
pnpm build
pnpm test
```

The tests serve `build/`, not the SvelteKit development server. They cover both
articles in desktop/mobile and light/dark modes, JavaScript-disabled content,
CodeMirror highlighting and read-only behavior, schema tabs and copying,
navigation, legacy redirects, and the data-model viewer.
