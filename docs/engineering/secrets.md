# Secrets management

## Overview

A secret is a **stable identity** — `secrets.id`, with a name, space, and
directory — whose encrypted content lives in immutable numbered
**version rows** (`secret_versions.id`). Setting a secret appends the next
version (`v1`, `v2`, ...) with a new version row id; the identity id survives
renames, moves, and rotations and is what the write API targets. Deployment
environment variables and settings pin exact versions by `secretVersionId` /
`SecretRef.version_id`; plain user configs use the same identity + versions
model with `configVersionId` / `ConfigRef.version_id`.

Secrets and configs share **one file system per space**: a name must be unique
among sibling secrets, configs, and `value_directories` under the same parent
directory (0 = the implicit root). That law spans three tables, so it is
enforced in Go behind the storage mutex (`valueSiblingNameTakenLocked`), never
by a SQL constraint. Assets have their own independent per-space file system.

Values are decrypted during deployment preparation, cached on the node that
runs the deployment — in memory, and additionally encrypted at rest on a
secondary — and expanded at process spawn time. They never appear in stored
deployment config, the UI state stream, the cluster replication feed, or logs.
The state stream and list APIs carry `SecretMeta` / `ConfigMeta`: the identity
at the root plus `version_refs`, NEWEST FIRST (`version_refs[0]` is the
latest). Config version refs include the plaintext value; secret version refs
never do.

A signed-in operator can also decrypt a single value on demand via the explicit
`PostV1SecretsReveal` endpoint (surfaced as the per-row "Reveal" button in the
UI). This is the **only** API path that returns a plaintext value — `List`
returns metadata only, and `Set` is write-only. Reveal requests use the immutable
secret row ID for exact-version reads. A value is decrypted into a response
solely on this explicit request; it is still never logged, replicated, or
persisted outside the encrypted store.

### Server-side generation

`PostV1SecretsGenerate` creates a secret from a specification instead of a
supplied value: the caller names it, says what kind of value it wants, and gets
back metadata. The plaintext is produced inside the process, sealed, and never
returned. That inversion is what lets it sit on the `default` scope while every
other value-writing route requires `secrets_access` — a caller who cannot read
secrets can still mint one and reference it from deployment env, which is how an
agent wires a fresh credential into a deployment end to end without the value
ever reaching anywhere it can observe.

The request nests its specification (`SecretGenerateRequest.password`) rather
than hanging fields off the request, so further generators — SSH keypairs, API
tokens, certificates — become sibling fields on the same route.

Two properties make the weaker scope safe:

- **Create-only.** An existing name is rejected. `Set` appends an immutable
  version rather than replacing one, so without this guard a caller could bury
  an operator's credential under a value that neither of them can read back.
  Rotation stays an operator action in the browser.
- **Nothing is echoed.** The response is `SecretMeta`; the generated buffer is
  zeroed once sealed.

Passwords are drawn from the same alphabet as the browser generator
(`frontend/src/components/secretGenerator.js`) by rejection sampling, 16–4096
characters, defaulting to 32 with symbols off. Lengths outside the range are
rejected rather than clamped — the caller cannot read the value back, so a
silently widened length would go unnoticed. `/v1/secrets/generate` carries its
own rate limit in `run.go`, because a retry loop here writes rows nothing ever
collects and produces no visible error.

The **store** is primary-only: the `secrets` table, the SMK and its keyslots live
on the primary and are never replicated. A secondary receives only the plaintext
values its own deployments reference, and keeps them under a machine key of its
own — see "Local runtime input persistence" below.

Key files:
- `backend/lib/secrets/secrets.go` — `Manager`, the key hierarchy, AEAD, and the
  machine-key boundary.
- `backend/lib/secrets/generate.go` — the server-side value generators used by
  `PostV1SecretsGenerate`.
- `backend/lib/machinekey/machinekey.go` — the shared `Provider` boundary (KEK
  supply + AEAD helpers) used by both the primary's store and a secondary's
  local cache.
- `backend/lib/localinputs/localinputs.go` — a secondary's encrypted at-rest
  copy of the runtime inputs it needs.
- `backend/storage/primarydb/secrets_store.go` — `secrets.Store` on the primary
  `StorageAdapter` (`secret_keyslots`, `secrets`, `secret_versions`, and
  `system_secrets` tables, plus the `SecretMeta` builders). Sealing happens
  through a `secrets.SealFunc` callback inside the write transaction, because
  the id-and-version AAD needs the identity id before the ciphertext can exist.
- `backend/storage/primarydb/values.go` — the shared secrets/configs namespace
  law: `ValidValueName` and the three-table sibling-uniqueness check.
- `backend/lib/engine/prepare/runtimeinputs/secrets.go` — finds typed `secretVersionId`
  / `configVersionId` refs, fetches each needed batch, validates it, and owns the
  prepared in-memory caches.
- `backend/lib/engine/secretdist/secretdist.go` — primary-side encrypted-secret
  fetch adapter.
- `backend/app/secondary/secrets.go` — secondary-side mTLS batch fetcher.
- `backend/lib/engine/runner/secrets.go` — typed secret/config env ref expansion
  through the injected runtime-input service at spawn time.
- `backend/app/primary/webuihandler/secrets.go` — the CRUD / status / recovery endpoints.
- `frontend/src/pages/secrets.js` — the Secrets page.

The secret create and value editors share a browser-side generator for random
passwords and multi-word passphrases. It uses Web Crypto for every generation;
passphrases select independently from the 2,048-word BIP39 English list. A
generated value remains only in the editor draft and is not sent to the server
until the operator saves it.

`frontend/src/components/valueOverlay.js` provides both create and edit modes
for configs and secrets. Create mode uses the same full-size value editor but
shows only the editable name and create actions; version metadata, copy/discard
actions remain edit-only. The editor keeps the persisted original value separate
from its staged value; generating or typing changes only the staged value, and
discard restores it from the original. Secret-reference controls on the Settings
page use these same modes; a created or newly edited version is selected in the
settings draft by its returned immutable ID. Revealed secret plaintext is scoped
to the open editor or copy operation rather than cached in the Secrets page rows.
When an edited value is referenced by deployments, the editor can update those
deployment environment references to the newly created immutable ID as part of
the save flow. The set request includes the caller-observed deployment IDs and
current config versions. The backend validates the complete reference set and
commits the resource version plus all deployment config/history updates in one
SQLite transaction, rejecting stale or incomplete requests without partial
writes.

OpenDeploy also stores internal key material in a separate encrypted
`system_secrets` table. System secrets use the reserved `opendeploy.` name
prefix, are not listed in the Secrets UI, cannot be revealed through
user-facing APIs, and cannot be referenced from deployment env vars. Current
system secrets hold the primary cluster CA/server mTLS material generated by
the installer before the first service start.

## Key hierarchy (envelope encryption)

```
recovery code ──Argon2id(salt)──► recovery KEK ─┐
                                                 ├─ wrap ─► SMK ──AEAD(AAD=class+name)──► secret values
machine KEK (provider-supplied) ────────────────┘
```

- A single 32-byte **Secrets Master Key (SMK)** encrypts every value with
  XChaCha20-Poly1305. For user secrets the owning identity id and version
  number are bound as **associated data**
  (`opendeploy-secret:user:s<secret_id>:v<version>`), so a ciphertext cannot be
  moved to another secret or another version of the same secret — and renames
  and directory moves never re-encrypt, because neither appears in the AAD.
  System secrets stay name-bound (`opendeploy-secret:system:<name>`); they are
  name-keyed, unversioned, and outside the file system.

  Rows written before the identity split are sealed under the legacy name AAD
  (`opendeploy-secret:user:<name>`). A **re-seal sweep** runs inside every
  successful unlock, before the store serves reads: each row is tried under the
  id AAD, and one that only opens under the legacy name AAD is re-sealed and
  rewritten. Detection is by derived trial decryption — nothing in the DB
  describes its own binding, so the swap-protection property holds throughout.
  The sweep is idempotent and crash-safe (a half-finished sweep resumes at the
  next unlock), and a store that stays locked simply keeps legacy rows until
  the recovery unlock. Rename requires an unlocked store for exactly this
  reason: unlocked implies swept, and only swept rows are rename-proof.
- The SMK is never stored in the clear. It is stored wrapped, once per
  **keyslot** (`secret_keyslots` table):
  - **machine slot** — `AEAD(SMK, machineKEK)`, for unattended boot.
  - **recovery slot** — `AEAD(SMK, Argon2id(recoveryCode, salt))`, the
    break-glass path. The recovery code is shown once and never stored.

Adding or rotating a keyslot never re-encrypts the secrets themselves.

### Why the machine key sits outside the DB

The machine KEK is *not* in the database and *not* in backups (litestream
replicates only `primary.db`; see `backend/backup/litestream.go`). So a leaked
DB or backup holds only `AEAD(SMK, KEK)` and `AEAD(SMK, recoveryKEK)` — useless
without either the on-box machine KEK or the recovery code.

| Attacker has… | Outcome |
|---|---|
| DB backup / replicated copy | Safe — no keyslot is decryptable |
| A secondary node's `secondary.db` alone | Safe — rows are sealed under that node's machine key, which is not in the DB, and decrypt on no other machine. Only the values that node's own deployments reference are ever present |
| A secondary node's disk (DB **and** machine key) | Exposes the values that node's deployments reference — the same values already readable from its running containers' environments. The primary's SMK and every unreferenced secret stay out of reach |
| UI stream / logs | Safe — only names, metadata, and numeric refs appear; plaintext is returned only by an explicit, authenticated `Reveal` request |
| Root on the primary | Game over (true of any host-side secrets manager; Phase 3 narrows the *stolen-disk / offline* case) |

## Lifecycle

- **Installation** — the primary bootstrap service generates the SMK,
  establishes a machine KEK, and writes the machine slot before systemd starts.
  A warning is logged until a recovery code is generated (a required setup step).
- **Normal restart** — load the machine KEK via the provider, unwrap the SMK.
  Unattended.
- **Locked** — if the machine KEK cannot be recovered on this node (e.g. a
  backup restored onto a fresh machine, where the machine-key file is absent),
  the store stays locked: the server still runs, but `Resolve` returns
  not-found (deployments referencing secrets **fail closed** at spawn) and
  writes are rejected until an unlock.
- **Recovery** — `Unlock(code)` derives the recovery KEK, unwraps the SMK, then
  re-establishes a fresh machine slot via the provider so subsequent boots are
  unattended again.

## Prepare-time distribution and spawn-time expansion

Typed `secretVersionId` and `configVersionId` env refs are discovered during deployment
preparation (`backend/lib/engine/prepare/runtimeinputs/secrets.go`). The
runtime-input service requests all referenced secret IDs as one batch through
`SecretProvider.FetchSecrets` and all referenced config IDs as one batch through
`ConfigProvider.FetchConfigs`; this is the same prepare-time readiness boundary
used for asset materialization.

On the primary, the provider decrypts from `secrets.Manager`. On a secondary,
the provider calls the primary over the mTLS cluster endpoint
`GET /v1/cluster/secrets` with a `ClusterSecretsRequest{ids}` payload, then
returns the plaintext batch. In both cases the single `RuntimeInputs` instance
validates the complete response before storing any values in its process-memory
cache, and a secondary additionally writes them through to encrypted local
storage.

Because rows are immutable, `EnsureSecretsReady` and `EnsureConfigsReady` request
only the ids not already held: an id always denotes the same value, and rotation
mints a new id that arrives as a new deployment config version. A node that
already holds everything a config references therefore makes no request at all.

The operator injects that same `RuntimeInputs` instance into every container
runner. At process spawn time (`backend/lib/engine/runner/secrets.go`),
`EnvVarValue` entries with `secretVersionId` or `configVersionId` are expanded from its
prepared in-memory caches. Plain `configs` values are not encrypted at rest in
the primary's own `configs` table (a secondary's local copies are, because it
seals every runtime input the same way). Unknown references, locked secrets,
missing primary connectivity during prepare with no local copy, or no prepared
value on the node are **fail-closed** errors.

## The `machinekey.Provider` boundary

How the machine KEK is protected at rest is isolated behind one interface, in
`backend/lib/machinekey`:

```go
type Provider interface {
    Establish() ([]byte, error) // create + persist a fresh KEK
    Load() ([]byte, error)      // recover the KEK on this machine (else error)
}
```

Critically, **what the KEK wraps is provider-agnostic**: the primary's machine
slot always holds `AEAD(SMK, KEK)`, and a secondary's rows always hold
`AEAD(value, KEK)`. Only how the KEK itself is stored differs, and that is the
provider's private business. This is the seam Phase 3 plugs into, and because
both node types share it, Phase 3 covers both in one change.

The two callers differ in what a failed `Load` means. On the primary it leaves
the store locked, because the SMK is unrecoverable without the recovery code. On
a secondary it establishes a fresh key, because everything the old key protected
can be refetched from the primary.

Phase 1 ships one implementation, `machinekey.File` (KEK in a 0600 file in the
data dir).

---

## Secondary secret distribution

Deployments running on a secondary can reference secrets by `secretVersionId`. The
secondary does not receive the encrypted secrets table or SMK; it fetches only
the plaintext IDs needed by the deployments assigned to it, over the cluster mTLS
listener.

## Local runtime input persistence

A secondary stores the secret and config values it has fetched in its own
`local_runtime_inputs` table, sealed under a machine key it generates for itself.
Together with the asset cache and the durable assignment cache, this means that
once an instance has started on a node, that node holds everything needed to keep
it running and can cold-start it with the primary unreachable.

**No key hierarchy, deliberately.** The primary needs an SMK, keyslots and a
recovery code because losing its machine key must not lose the secrets. On a
secondary none of that applies — the primary is authoritative, so a lost or
unreadable key just means refetching. The design is therefore one machine KEK
sealing each row directly, with `kind + ref_id` as associated data. Rows that
will not open are dropped and refetched rather than treated as an error, and a
missing key file is established rather than reported. The secondary's key is
independent of the primary's: nothing in `secondary.db` decrypts anywhere else,
including on the primary.

**What the encryption is for.** Against an attacker who already has local access
it is weak by construction: with the file provider the machine key sits 0600
beside `secondary.db`, same uid. Its value is the offline case — disk images, VM
snapshots, volume clones, support bundles, a `sqlite3 .dump` pasted into a ticket
— where ciphertext is a categorically different object to circulate than
plaintext. It is also what makes Phase 3 a provider swap on the secondary rather
than a migration of every row on every worker.

**Retention.** Persisting values is only acceptable if a node also stops holding
what it no longer needs, so a periodic sweep
(`backend/app/secondary/retention.go`) drops every stored value and cached asset
file that no instance assigned to the node still references. The reference set is
the union over the durable assignment cache, which is already the authoritative
answer to "what does this node run".

The sweep runs only when every instance on the node is settled — desired config
version equals both the preparer's and the runner's reported version, or the
instance is not meant to be running. A mid-rollout instance is still running the
previous config version, whose referenced ids the node can no longer enumerate
(it holds only the current assignment blob), so sweeping then could delete an
input its live container needs to respawn. It is all-or-nothing because ids are
shared between deployments, leaving no sound way to attribute one to the instance
that has settled.

Two consequences worth knowing:

- A node that holds a value keeps running on it even if the secret is later
  deleted on the primary; revocation propagates as a config change, not
  immediately. This is intended — it is the same property that makes cold start
  work — and is safe on the assumption that a referenced secret cannot be
  deleted.
- One instance stuck mid-rollout blocks reclamation for the whole node until it
  settles. This fails in the safe direction (files are kept, never wrongly
  deleted) and self-corrects.

---

## Phase 3 (planned): TPM-sealed machine key

### Goal

Replace the on-disk machine KEK with a TPM-sealed one **where a TPM is
available**, so a stolen disk (or an offline DB+keyfile copy) cannot decrypt the
SMK on a primary, or the locally stored runtime inputs on a secondary. It must be
**opt-in via config** and **fall back transparently** to the file provider on
nodes without a usable TPM. The keyslot format, the secondary's row format, the
recovery slot, and all higher layers are unchanged — only a new
`machinekey.Provider` implementation is added.

### Mechanism

Talk to the TPM directly via `github.com/google/go-tpm` (not `systemd-creds`):
the machine KEK is generated by opendeploy at runtime, which fits a direct
seal/unseal model better than systemd's provisioned-credential model, and avoids
a hard dependency on systemd ≥ 250 and an external CLI.

```go
// in backend/lib/machinekey, alongside File
type TPM2 struct{ BlobPath string } // e.g. {dataDir}/machine.key.tpm

func (t *TPM2) Establish() ([]byte, error) {
    // 1. generate a random 32-byte KEK
    // 2. TPM2 seal it under the SRK (no PCR policy by default — see below)
    // 3. write the sealed blob to BlobPath (0600)
    // 4. return the KEK
}

func (t *TPM2) Load() ([]byte, error) {
    // read the sealed blob, TPM2 unseal -> KEK
}
```

The sealed blob is useless off the originating machine, so a stolen disk that
carries both `primary.db` and `machine.key.tpm` still cannot recover the SMK.

### Configuration and selection

Add a config knob (default preserves Phase 1 behaviour):

```
OPENDEPLOY_SECRETS_MACHINE_KEY = file | tpm2 | auto    (default: file)
```

- `file` — always the on-disk provider (today's behaviour).
- `tpm2` — require the TPM; fail startup if unavailable (for operators who want
  a hard guarantee).
- `auto` — probe for a usable TPM (`/dev/tpmrm0` present and accessible) and use
  `tpm2` if so, else `file`. This is the "transparent" mode and is the likely
  long-term default once the path is proven.

Selection happens where the provider is constructed — `secrets.Open` on the
primary and the `localinputs.Open` call site on a secondary — so the only change
in each is choosing the implementation from config.

### PCR binding

Seal to the TPM's storage root **without a PCR policy** by default. PCR binding
(measured boot state) additionally defends against an attacker booting a
different OS on the same hardware, but it is brittle: a kernel/firmware/bootloader
update changes the PCRs and breaks unseal until re-sealed. opendeploy's threat
model (leaked backup, stolen disk) is met without PCR binding, so it is not the
default. A signed-PCR policy could be a later opt-in.

### Availability — why fallback is mandatory

A TPM is not universally present, so `tpm2` cannot be the unconditional default:

- **Bare metal** — TPM 2.0 is common on post-~2016 hardware but often disabled
  in firmware.
- **Cloud VMs** — only on specific configurations: AWS NitroTPM (newer instance
  types + UEFI AMIs), GCP Shielded VMs, Azure Gen2 / Trusted Launch. Older or
  blunt instance types have none.
- **Self-hosted virt** — QEMU/KVM needs `swtpm` configured; VMware needs a vTPM;
  VirtualBox 7+ optional.
- **Containers** (Docker/LXC/K8s) — effectively never; the TPM is not namespaced.

Even when present, the resource-manager device `/dev/tpmrm0` must exist and the
service user typically needs membership of the `tss` group.

> Note on `systemd-creds`: its *encrypted credentials* feature needs systemd
> ≥ 250 (absent on e.g. Ubuntu 20.04/22.04, Debian 11, RHEL 8), and in its
> host-key mode it stores the wrapping key in `/var/lib/systemd/credential.secret`
> — on disk, like Phase 1 — so it adds little without the TPM backend. The
> security win is the TPM specifically, which is why Phase 3 targets the TPM
> directly.

### Migration / interplay

- **Enabling on an existing node** — on next start with `tpm2`/`auto`, the
  provider establishes a TPM-sealed KEK and `rewriteMachineSlot` re-wraps the
  SMK under it; the stale `machine.key` file is then removed. No secret value is
  re-encrypted.
- **Disabling / TPM lost (e.g. firmware reset, hardware change)** — on a primary
  the machine slot can no longer be unsealed → the store is locked →
  `Unlock(code)` with the recovery code re-establishes a machine slot under the
  currently-selected provider. The recovery slot is provider-independent and
  always works, which is exactly why it is the durable root of recoverability.
  On a secondary there is nothing to recover: the unreadable rows are dropped, a
  fresh key is established under the currently-selected provider, and the values
  are refetched from the primary on the next prepare.
