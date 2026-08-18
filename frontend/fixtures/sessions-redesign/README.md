# Sessions page redesign fixture

Run from `frontend/`:

```sh
pnpm run dev:sessions-redesign
```

Proposal for re-doing `src/pages/sessions.js` in the direction of the other
flush pages (IAM, nodes): one surface running to the window edges, no cards and
no padding gap, with the sessions list as a dense gridline table instead of a
row-of-divs list.

## Layout

- **Two tabs.** `Agent sessions` holds today's content (agent prompt + session
  list); `Personal sessions` is new and lists the user's own browser login
  sessions. The tab bar sits on the band background with an underline
  indicator and a per-tab row count.
- **Agent prompt as a `codeBlock`.** The prompt card becomes the reusable
  `src/components/codeBlock.js` component: a header bar (collapse toggle +
  copy button top right) over a lazily loaded CodeMirror view, here with
  `wrap: true` and `lineNumbers: false` since the prompt is prose. Collapsing
  it leaves the table full-height.
- **Excel-ish tables.** Tight `px-2 py-[3px]` cells, hairline row rules with
  slightly fainter column rules, uppercase micro headers on the band
  background, `tabular-nums` timestamps. Pending agent rows keep the amber
  tint and inline Approve; the empty state is a row so the header never sits
  on nothing.

## Columns

- Agent: `Started | Status | Approval code | Origin | actions`. The approval
  code gets its own column (it was crammed into Status before); finished rows
  show a `—`.
- Personal: `Signed in | Status | Device | IP address | Last active | Expires |
  actions`. The current session is badged `This browser` and its revoke action
  reads `Sign out this browser`.

## Mock behaviour

- Approving a pending agent row moves it to `Approved, not collected`; ~2.5s
  later the mock agent collects its token and the row flips to `Active until…`.
- Revoke/reject and personal-session revoke flip rows to their dead state in
  place (records stay listed, matching production semantics).
- Scenarios: `Typical` (every state visible), `Many` (density check), `Empty`.

## Backend work this design needs

Personal sessions do not exist server-side today: browser logins are stateless
JWTs with no `jti`, so they cannot be listed or revoked. Supporting the tab
means:

- Minting browser tokens with a `jti` and recording a session row at login
  (created, expiry, user agent, requesting address, last-seen updated on use).
- Checking revocation for browser tokens on request (today only agent tokens
  pay the per-request session read; see `verifyAgentSession` in
  `backend/app/primary/webuihandler/auth.go`).
- New `ApiServer` endpoints: list own personal sessions, revoke one (self
  restricted), plus marking which row is the calling session.
