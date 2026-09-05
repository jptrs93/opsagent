# Login page redesign fixture

> Design D (Card) is what `src/pages/login.js` now ships, with the overlay in
> `src/components/caTrustHelp.js`. The "Current" option here is a replica of
> the page before any of this work; A, B and C are the unadopted alternatives.

Run from `frontend/`:

```sh
pnpm run dev:login-redesign
```

Three proposals for `src/pages/login.js`, rendered next to a replica of the
current page, against a mock auth backend. The floating panel (bottom left)
switches design and scenario; both persist in the URL hash, so a link like
`#design=console&passwordLogin=0&localCa=1` opens a specific state.

Every design is purely presentational over the same `loginController` in
`shared.js`, which is the real page's behaviour (one attempt at a time across
both methods, passkey `NotAllowedError` wording, error cleared on retry) with
the API calls swapped for `mock.js`. Sign-in takes ~1.2s and either succeeds
(a toast stands in for the navigation to `/`) or fails when "Every sign-in
fails" is on.

## Scenarios

- **Password login enabled** — `auth.password_login_enabled`. When on, the
  password form is the primary path and the passkey button is secondary,
  because password login is enabled precisely where browsers refuse WebAuthn.
  When off, the passkey button is the single primary action.
- **Passkey login enabled** / **Browser supports passkeys** — the two reasons
  the passkey path can be missing; each design has an explicit message for both.
- **Local CA in use** — shows the certificate-trust help entry point.
- **Methods discovery** — `ready`, `loading` (skeleton), or `error` (banner with
  Retry, passkey button still offered, as today).

## The designs

### A · Centered

One column, no card and no side panel: a single header line (mark, product,
"Sign in"), labelled fields with a filled primary and an outlined secondary,
then a compact connection panel (transport with a status dot, enabled
methods). The "Trust the
CA" action sits on the transport row it belongs to and opens the certificate
overlay. A faint dot grid and glow at the top of the page give it some
atmosphere without a second surface to maintain.

Best when the page should feel like a product front door but still hold the
operational facts. It is the most vertical of the three, so the connection
panel is what gives on short viewports.

### B · Minimal

No card and no panel: an outline mark, one heading, the host in mono,
underline inputs with small-caps labels, and a single high-contrast button
(light on dark, deliberately not the brand blue used inside the app). All
secondary content is one quiet footer row; certificate help opens the same
overlay as A.

Best if the aim is "less". It has the least chrome of the three and reads well
at every width, but the light button needs a dark spinner, and underline
inputs are a departure from the app's bordered inputs.

### C · Console

One compact card that borrows the app's own language: a header band with the
wordmark and a host chip (green dot on TLS, amber on plain HTTP), a grouped
field box with in-field captions and a single focus ring, a hairline "or", and
a footer band with the secondary links. Certificate help expands inline under
the footer so the card stays the only surface. Errors are a banner strip at
the top of the body.

Closest to the rest of the UI and the smallest change to ship; it stays a card
on the gradient body, so if the card itself is what you dislike this is not
the one.

### D · Card

Console's shell around Centered's content. The card (header band on the
card surface, grouped field box with in-field captions, footer band) sits on the
Centered backdrop. The header band is the single "OpenDeploy Sign in" line,
the passkey button comes first with the password form after it, both
buttons in one brand-tinted style between filled and outlined (no hint
sentence), and the footer band holds the transport row with the "Trust the CA" action
and the setup link. Certificate help is always the
overlay; nothing expands inline.

Best if the card is wanted for its framing but the page should keep the
integrated design's content and behaviour.

## Adopting one

- `busyButton` in `shared.js` is `spinnerButton` with `spinnerClass` exposed.
  A and C can use `spinnerButton` as is; B needs an `options.spinnerClass` on
  `src/components/spinnerbutton.js`.
- `caHelpBody`/`caHelpModal` are the current `caTrustHelp` reworked as three
  steps (get, trust, restart) with a platform picker, a header bar per command
  block, and the CA's SHA-256 fingerprint computed from the fetched PEM so it
  can be checked against the installer output. They would move to
  `src/components/` since the bootstrap page could use the same overlay.
- `fingerprintIcon`, `keyIcon`, `shieldIcon`, `alertIcon`, `downloadIcon` and `logoMark` would
  move to `src/lib/icons.js`. The mark is a placeholder glyph; the sidebar
  header could carry it too.
- `data-testid` attributes from the current page are not on the proposals;
  restore `login-passkey-button`, `login-username-input`,
  `login-password-input`, `login-password-button`, `login-methods-error`,
  `login-ca-help`, `login-ca-download`, `login-ca-copy`, `login-ca-cmd-copy`
  and `login-ca-warning` when porting so the e2e suite keeps working.
