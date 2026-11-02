# Enrol or recover fixture

The enrol-or-recover widget (`src/components/userEnrol.js`) in both places
it is reached from, over an in-memory backend, so the flow can be judged
before the endpoints, the link token, and the pending user state exist.

Run from `frontend/`:

```sh
pnpm run dev:user-enrol
```

The floating panel (bottom left) switches the host, the dataset, and whether
every server action fails; all persist in the URL hash, so
`#context=users&data=empty` opens a specific state.

## The two hosts

- **login** — the auth card. The master password (anything non-empty in the
  fixture) unlocks the widget, which then fills the card. Step strip above:
  Master password, Enrol or recover, Done. This is what a `src/pages/enrol.js`
  would be, replacing today's First time setup.
- **users page** — the real `src/pages/users.js` in the dashboard shell. Its
  toolbar button opens the widget as a dialog (`userEnrolOverlay`). New users
  land in the roster with a `pending` tag until they sign in. The page also
  shows the **You** tab (`src/components/accountPanel.js`): rename inline,
  set or change the password, add and remove passkeys, a hand-off to
  Sessions, and the signed-in user's grants read-only. The panel's **You**
  switch picks who is signed in: `joss` has a password and two passkeys,
  `alice` is passkey-only (her one passkey cannot be removed), `bob` has a
  password and no passkey. In the fixture any current password is accepted
  except `wrong`.

## The flow

1. **Choose**: Enrol a new user, or Recover an existing user.
2. **Enrol**: username (optional when the invitee will pick it from the
   link), password (they choose it from the link, the default, or set it now
   with a generator), and privileges as a list of grants added with the
   grant editor the Users page already uses. Finish with *Create sign-in link*
   (primary) or, with a username and password set, *Create and sign in* (login
   host) / *Create user* (users host).
3. **Recover**: pick the user from a searchable list; the summary shows their
   privileges read-only. Finish with *Create recovery link* or, on the login
   host only, *Sign in as …*.
4. **Done**: the link with a copy button, its lifetime, and a warning that it
   signs in as that user. Direct sign-in shows a toast where the app would
   navigate to `/`.

Nothing of the Users page is forked: the widget reads the real stores and
`mock.js` writes to them. The grant editor is the one from
`src/components/accessEditors.js`, now exported as `grantEditor` and shared
with the Grant access dialog.

## Open questions

- Whether the login host should keep offering direct sign-in for a new
  user, or only for recovery. Direct enrolment sign-in is the master password
  acting as the new user's first login, which is today's behaviour.
- The link lands on a page that is not designed yet: pick a username if none
  was set, set a password if none was set, optionally register a passkey.
- Lifetime and single use are shown as fixed (7 days, once); whether either
  is a setting.
