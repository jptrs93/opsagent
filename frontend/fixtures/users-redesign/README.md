# Users & roles fixture

> The tabbed page with the rule explainer is what `src/pages/users.js` now
> ships, with the model in `src/lib/authzExplain.js` and the popover in
> `src/components/ruleExplainer.js`. This fixture mounts that page over an
> in-memory access API so it can be exercised without a cluster. The design
> is described in `docs/engineering/frontend.md` (Users & roles).

Run from `frontend/`:

```sh
pnpm run dev:users-redesign
```

The page renders inside a static replica of the dashboard shell (`shell.js`)
so it is judged in situ. The floating panel (bottom left, inside the sidebar
column) switches the dataset and the signed-in user; both persist in the URL
hash, so `#data=busy&you=3` opens a specific state.

Nothing of the page is forked. It reads the real state stores (`usersMapS`,
`spacesS`, `authzTemplatesS`, `authzGrantsS`, `authzGlobalRulesS`), renders
rules with the real `ruleDisplay`, and opens the real editor dialogs from
`src/components/accessEditors.js`. `mock.js` writes the datasets into those
stores and replaces the seven `/v1/access/*` methods on the generated `capi`
object with in-memory ones (with the backend's refusal cases: built-in roles,
roles still granted, the last access-managing grant, a deny targeting access
control), so creating, editing, granting, revoking and deleting all work end
to end and the tables update live.

## Scenarios

- **Set** — `typical` (six users, the two built-in roles plus four custom
  ones including a cluster-level `node_operator`, a direct rule for
  `deploy-bot`, the seeded `default_user_visibility` allow rule and two
  denies, one agent-only), `busy` (sixteen users with several grants each, nine
  roles including a two-argument `release_manager`, seven spaces, five global
  rules: wrapping and the pill phrasing ladder under load), `empty` (a fresh
  install: one user, built-ins only, one seeded rule; the empty states).
- **You** — which user is signed in: the "you" badge and the padlock on your
  own `cluster_admin` grant.

## Open questions

- The Rules column clips the long built-in rules below about 1150px of
  viewport, as the previous page did; the Granted to column costs it roughly
  a tenth of the width. Dropping that column, or moving the holders into the
  role's explainer, would give it back.
- Hover opens after 220ms, so aiming for a chip's revoke × shows the
  explanation on the way. It sits under the chip and never covers the ×.
- The card covers the rows beneath the rule. An inline expansion under the
  row (push, not overlay) is the alternative if that proves annoying while
  scanning a long role.
- Selecting a rule tab in the definition pane does not filter the access
  table; if that turns out to be wanted, the table can highlight the cells
  the selected rule contributes.
