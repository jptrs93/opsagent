# Worker enrollment (future)

Design for simplifying worker node onboarding so a new secondary can join the
cluster with only two pieces of information: the primary's address and the
primary's TLS cert fingerprint.

## Motivation

Current worker setup requires:

1. Running `deploy/tls/generate_certs.sh` on the primary to produce `ca.crt`
   and a dedicated `<machine>.crt` / `<machine>.key` pair.
2. Copying all three files to the worker's `/etc/opsagent/tls/` out-of-band
   (scp, config management, etc.).
3. Editing `/etc/opsagent/env` on the worker to set the cert paths and
   `OPSAGENT_PRIMARY_ADDR`.

That's three files and one env edit per worker, all manual. For a small
cluster this is tolerable; for anything more it's friction and a source of
operator error (wrong cert on wrong host, stale CA after rotation, etc.).

## Goal

Install a new worker with a single command:

```bash
sudo ./ubuntu_worker_install.sh \
    --primary-addr primary.example.com:9443 \
    --primary-fingerprint sha256:AA:BB:CC:...
```

No pre-generated certs. No manual file copies. Everything the worker needs is
fetched from the primary during a first-contact enrollment handshake, and then
a human approves the machine in the primary's web UI.

## Primary becomes the CA

- The primary holds the cluster CA cert **and** the intermediate signing key.
- On first startup the primary generates its own server cert, self-signed by
  the intermediate key. No more `generate_certs.sh` — the operator never
  touches OpenSSL.
- The primary's web UI exposes the primary's cert SHA-256 fingerprint so the
  operator can copy it into the worker installer.

The signing key lives in `/var/lib/opsagent/ca/` on the primary, owned by the
`opsagent` user, mode 600. At this scale we don't encrypt-at-rest behind the
master password; that's a future tightening if needed.

## Enrollment protocol

The cluster mTLS listener (currently `OPSAGENT_CLUSTER_LISTEN`, default
`:9443`) gains an enrollment branch. We do **not** run a second port — the
same TLS listener accepts both authenticated (mTLS) cluster traffic and
unauthenticated enrollment requests. The handler branches on whether the
client presented a valid client cert:

- No client cert → only `/enroll/*` endpoints are reachable.
- Valid client cert → full cluster protocol.

This keeps the firewall surface to a single port and matches how kubeadm's
kube-apiserver handles bootstrap tokens on the same port as authenticated
API traffic.

### Worker-side first contact

1. Worker reads `--primary-addr` and `--primary-fingerprint` from its env.
2. If `/var/lib/opsagent/tls/node.crt` already exists, skip enrollment and
   connect directly on the mTLS channel. (Idempotent — reruns are safe.)
3. Otherwise, dial `--primary-addr` with TLS `InsecureSkipVerify: true` plus
   a custom `VerifyConnection` callback that computes the SHA-256 of the
   presented leaf cert and compares it to `--primary-fingerprint`. On
   mismatch, **abort with a clear error** — do not proceed.
4. Send `POST /enroll/request` with:
   - a freshly-generated ed25519 keypair's public key (the worker keeps the
     private key locally)
   - the worker's self-asserted hostname (for operator convenience only —
     the primary will not trust this value)
   - the worker's detected IP(s)
5. The primary stores the pending enrollment and returns a 202 with a
   `pending_id`. The worker then long-polls `GET /enroll/status/<pending_id>`
   with a 30-second timeout and retries on 204, backing off.
6. When the operator approves, the response carries:
   - a freshly signed `node.crt` for the worker's pubkey, with `CN=<operator-chosen-name>` and an appropriate SAN
   - the cluster CA cert (`ca.crt`)
   - any bootstrap secrets the worker needs (GitHub token, etc.)
7. Worker writes `ca.crt`, `node.crt`, and its private key to
   `/var/lib/opsagent/tls/`, mode 600, then connects on the mTLS channel.
   From that point on, step 2 short-circuits future reconnects.

### Primary-side approval

- Pending enrollments appear in a new **Machines** tab in the primary UI.
- Each row shows: source IP, self-asserted hostname, enrollment timestamp,
  ed25519 public key fingerprint.
- The operator sets the canonical machine name (not trusting the
  self-asserted one) and clicks **Approve**.
- The primary signs the cert with the operator-chosen CN, bundles it with
  the CA cert and secrets, and delivers it via the polling worker's next
  status request.
- **Reject** discards the pending enrollment. The worker sees a terminal
  error and exits.

## Why fingerprint pinning (not bootstrap tokens)

Two reasonable options exist for authenticating first contact:

| | Bootstrap token | Cert fingerprint pin |
|---|---|---|
| Extra info operator copies | one short-lived token | one SHA-256 fingerprint |
| Expiry | needs expiry (otherwise leaked tokens live forever) | stateless, never expires |
| Primary-side state | needs a token store | none |
| UX | operator generates token in UI, copies, token may expire before use | operator reads fingerprint once, reuses forever |
| Rotates when primary cert rotates | unaffected | yes — must re-copy on rotation |

Fingerprint pinning wins on simplicity: the primary has no token store, no
expiry plumbing, no "regenerate token" UI. The one downside — operator has
to re-copy the fingerprint when the primary's cert rotates — is acceptable
because primary cert rotation is a rare operator-initiated event anyway,
and at that moment the operator is already touching the primary.

kubeadm uses tokens because kubeadm targets Kubernetes at scale, where
rotating tens of thousands of tokens is cheaper than rotating one cluster
root. We're not in that regime.

## Cert lifecycle

- Issued certs have a 90-day TTL.
- Workers auto-renew over the mTLS channel when <25% of lifetime remains.
  The renewal endpoint is `POST /enroll/renew`, authenticated by the
  existing cert, returns a new cert signed by the same pubkey.
- **Revocation is by non-renewal.** An operator can deny a machine in the
  UI; denial flips a flag in the primary's DB, and the next renewal request
  fails. Within the TTL window (worst case 90 days, typical <30) the
  machine falls out of the cluster. No CRL, no OCSP.
- If an immediate boot is needed, the operator can also close the mTLS
  connection server-side at deny time — instant effect for online nodes,
  still no CRL.

## Secret distribution

Secrets (GitHub token, anything else cluster-wide) live on the primary and
are pushed to workers on two triggers:

1. At enrollment approval, inline with the cert bundle.
2. On reconnect, via a `GET /cluster/secrets?since=<etag>` endpoint that
   returns 304 if unchanged.

Workers hold secrets in memory only; no on-disk copy. Restart → reconnect
→ refetch.

## Installer changes

- `ubuntu_server_install.sh` stays the primary installer, unchanged.
- New `ubuntu_worker_install.sh` (thin variant):
  - required flags: `--primary-addr`, `--primary-fingerprint`
  - creates the `opsagent` user, data dir, sudoers, unit file (same as primary)
  - writes `/etc/opsagent/env` with `OPSAGENT_PRIMARY_ADDR` and
    `OPSAGENT_PRIMARY_FINGERPRINT` set, everything else blank
  - starts the service immediately — no manual config step, since first-contact
    enrollment blocks in-process until the operator approves

## Backend work

Rough inventory of what this implies in `backend/`:

- `cluster/`: add enrollment handler, fingerprint-pinning TLS dialer on the
  worker side, pending-enrollment store, renewal handler.
- `primary/`: CA bootstrap on first start, signing helpers, secret
  distribution endpoint.
- `slave/`: enrollment state machine, cert persistence, auto-renew loop.
- `handler/`: machines API (list pending / list active / approve / deny /
  rename).
- `frontend/`: Machines tab with pending + active sections and approve flow.
- `ainit/`: new env vars `OPSAGENT_PRIMARY_FINGERPRINT`, signing-key path.

## Open questions

- **Signing key at rest.** Plaintext file vs. encrypted behind the master
  password. Starting plaintext (mode 600 under the `opsagent` user) is fine
  for v1; revisit if we add a threat model where disk compromise matters.
- **Multi-primary / HA.** Current design assumes a single primary holds the
  CA. If we ever need HA, the CA becomes a coordination problem. Out of
  scope for v1.
- **Operator identity for approval audit.** Who approved which machine,
  when. Needs to tie into the existing passkey auth so there's a real user
  on each approval event.
- **Fingerprint discovery UX.** Operator still has to get the fingerprint
  from the primary to the worker somehow. Printing it at the top of the
  primary's Machines tab is enough — they copy it once, use it on every
  worker install.
