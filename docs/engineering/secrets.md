# Secrets management

## Overview

The secrets store lets operators save encrypted key/value pairs and reference
them from a deployment's environment as `${name}` (e.g.
`DB_PASS=${staging.db.password}`). Values are decrypted at process spawn time,
on the node that runs the deployment, and never appear in stored config, the UI
state stream, the cluster replication feed, or logs.

A signed-in operator can also decrypt a single value on demand via the explicit
`PostV1SecretsReveal` endpoint (surfaced as the per-row "Reveal" button in the
UI). This is the **only** API path that returns a plaintext value — `List`
returns metadata only, and `Set` is write-only. A value is decrypted into a
response solely on this explicit request; it is still never logged, replicated,
or persisted outside the encrypted store.

It is **primary-only**: the encrypted store and its keys live on the primary and
are never replicated to secondaries.

Key files:
- `backend/secrets/secrets.go` — `Manager`, the key hierarchy, AEAD, and the
  `machineKeyProvider` boundary.
- `backend/storage/sqlite/secrets_store.go` — `secrets.Store` on the primary
  `StorageAdapter` (DB passthrough for the `secret_keyslots` and `secrets`
  tables).
- `backend/engine/runner/secrets.go` — `SecretResolver` and `${name}` expansion
  at spawn time.
- `backend/handler/secrets.go` — the CRUD / status / recovery endpoints.
- `frontend/src/pages/secrets.js` — the Secrets page.

OpenDeploy also stores internal key material in the same encrypted table with
`internal = 1`. Internal secrets use the reserved `opendeploy.` name prefix, are
not listed in the Secrets UI, cannot be revealed through user-facing APIs, and
cannot be referenced from deployment env vars. Current internal secrets hold the
primary cluster CA/server mTLS material generated on first startup.

## Key hierarchy (envelope encryption)

```
recovery code ──Argon2id(salt)──► recovery KEK ─┐
                                                 ├─ wrap ─► SMK ──AEAD(AAD=name)──► secret values
machine KEK (provider-supplied) ────────────────┘
```

- A single 32-byte **Secrets Master Key (SMK)** encrypts every value with
  XChaCha20-Poly1305. The secret's name is bound as **associated data**, so a
  ciphertext cannot be moved to another name.
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
| A secondary node / `secondary.db` | Safe — secrets never reach a secondary |
| UI stream / logs | Safe — only `${...}` placeholders and key names appear; plaintext is returned only by an explicit, authenticated `Reveal` request |
| Root on the primary | Game over (true of any host-side secrets manager; Phase 3 narrows the *stolen-disk / offline* case) |

## Lifecycle

- **First run** — generate the SMK, establish a machine KEK, write the machine
  slot. The store is unlocked. A warning is logged until a recovery code is
  generated (a required setup step).
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

## Spawn-time resolution

`${name}` placeholders are expanded in `osProcessEnv` output at spawn time
(`backend/engine/runner/secrets.go`), not at config time. This keeps resolved
values out of stored config, replication, and logs (spawnDaemon logs env keys
only), and means a rotated secret is picked up on the next restart. `$$` escapes
a literal `$`. An unknown secret, a locked store, or no resolver on the node is
a **fail-closed** spawn error, surfaced in the run output via `writeSpawnError`.

## The `machineKeyProvider` boundary

How the machine KEK is protected at rest is isolated behind one interface:

```go
type machineKeyProvider interface {
    establish() ([]byte, error) // create + persist a fresh KEK
    load() ([]byte, error)      // recover the KEK on this machine (else error => locked)
}
```

Critically, **the DB keyslot format is provider-agnostic**: the machine slot
always holds `AEAD(SMK, KEK)`. Only how the KEK itself is stored differs, and
that is the provider's private business. This is the seam Phase 3 plugs into.

Phase 1 ships one implementation, `fileMachineKey` (KEK in a 0600 file in the
data dir).

---

## Phase 2 (planned): secret distribution to secondaries

Today a deployment that runs **on a secondary** and references `${...}` fails
closed, because the secondary has no resolver. Phase 2 adds in-memory
distribution:

- Add `MsgToWorker.SecretsBundle{deployment_id, version, [{key, value}]}` to the
  cluster protocol.
- On the primary, when a deployment's config or a referenced secret changes,
  compute that deployment's secret references, decrypt them, and push the bundle
  over the existing mTLS feeder (`backend/primary/session.go`).
- On the secondary, set `runner.Secrets` to an **in-memory** resolver backed by
  the received bundles. Secrets are **never written to `secondary.db`** and are
  dropped when the version is retired.

Tradeoff: a secondary cannot cold-start a deployment while the primary is
unreachable. If that resilience is needed, an at-rest cache wrapped by a
TPM-sealed secondary key (Phase 3 on the secondary) is the escalation.

---

## Phase 3 (planned): TPM-sealed machine key

### Goal

Replace the on-disk machine KEK with a TPM-sealed one **where a TPM is
available**, so a stolen disk (or an offline DB+keyfile copy) cannot decrypt the
SMK. It must be **opt-in via config** and **fall back transparently** to the
file provider on nodes without a usable TPM. The keyslot format, the recovery
slot, and all higher layers are unchanged — only a new `machineKeyProvider`
implementation is added.

### Mechanism

Talk to the TPM directly via `github.com/google/go-tpm` (not `systemd-creds`):
the machine KEK is generated by opendeploy at runtime, which fits a direct
seal/unseal model better than systemd's provisioned-credential model, and avoids
a hard dependency on systemd ≥ 250 and an external CLI.

```go
type tpm2MachineKey struct{ blobPath string } // e.g. {dataDir}/machine.key.tpm

func (t *tpm2MachineKey) establish() ([]byte, error) {
    // 1. generate a random 32-byte KEK
    // 2. TPM2 seal it under the SRK (no PCR policy by default — see below)
    // 3. write the sealed blob to blobPath (0600)
    // 4. return the KEK
}

func (t *tpm2MachineKey) load() ([]byte, error) {
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

Selection happens in `secrets.Open`, which already constructs the provider — the
only change there is choosing the implementation from config.

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
- **Disabling / TPM lost (e.g. firmware reset, hardware change)** — the machine
  slot can no longer be unsealed → the store is locked → `Unlock(code)` with the
  recovery code re-establishes a machine slot under the currently-selected
  provider. The recovery slot is provider-independent and always works, which is
  exactly why it is the durable root of recoverability.
