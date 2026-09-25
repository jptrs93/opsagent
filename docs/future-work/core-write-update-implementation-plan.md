# Core write update: implementation plan

Status: phase 1 planned, 2026-09-25. Phase 2 direction recorded, not yet
planned. Depends on the value reference pair work in
`value-reference-pairs-implementation-plan.md` landing first: both touch
`secrets/store.go`, `secrets/secrets.go`, `values/configs.go`, and
`values/references.go`.

The end state is a state stream whose updates are facts, not projections:
one `CoreWriteUpdate` per Commit carrying seq, time, actor, and a list of
create, update, and delete mutations with the caller-owned definition as
payload. Version ordinals, facet versions, created times, and row ids are
derived by each consumer from the mutations it has folded. For that to work,
every change the write model considers real must be visible as a diff of the
definition, and a write that changes nothing must produce no mutation.

Phase 1 makes the value entities satisfy that rule. Phase 2 replaces
`CoreUpdate` with `CoreWriteUpdate`.

## Phase 1: seal id and no-op config writes

### Goals

1. A secret value write is a visible change to the `Secret` definition.
   Today `Secret { fs, space_id }` does not change when the value does; only
   the row's ciphertext and the projected `value_version` do.
2. The AEAD binding names a fact. Today it binds `(secret_id, value_version)`,
   a counted ordinal. It moves to `(secret_id, seal_id)`, an opaque identity
   issued once per value write.
3. A config write whose value equals the current value is a no-op: success,
   the current event returned, no row, no seq.

### Settled decisions

1. **Opaque seal id, not a ciphertext hash.** `smk_version` is recorded on
   every row, so re-sealing under a rotated key is a future operation. A
   hash-derived id would change on rotation and read as a new value. An
   opaque id issued at value write time survives rotation and is the right
   associated data for it.
2. **Zero re-seal migration.** The new AAD is
   `opendeploy-secret:user:s<secret_id>:<seal_id>`. Legacy rows get
   `seal_id = 'v' || value_version`, which reproduces today's AAD bytes
   exactly, so every existing ciphertext opens under the new function without
   the key. New ids use a different prefix so the namespaces cannot collide.
3. **Same plaintext twice is a new secret version.** The server would have to
   open the previous ciphertext to compare. Secret writes are rare and
   deliberate; a fresh seal is acceptable. This can be tightened later inside
   the seal path without another migration.
4. **No-op is success.** A no-op write returns the current event and consumes
   no seq. An error would break idempotent retries. `Commit` already treats
   an empty update as no commit.
5. **The value no-op does not suppress deployment rewrites.** A config set
   with `update_referencing_deployments` where the value is unchanged still
   repoints any referencing deployment that pins an older version to the
   current one. A deployment already at the current version is skipped, so no
   deployment event with an unchanged definition is written.
6. **Deployments are out of scope.** `RestartUpdate` writes a deployment
   event with an unchanged definition on purpose, and the frontend's
   `deploymentRestartEvent` detects a restart as a version with no facet
   changed. Making restart a visible diff is phase 2 work.
7. **`value_version` stays.** It remains the projected ordinal and half of
   `ValueRef`. Phase 1 only moves the cryptographic binding off it.

### Storage

`schema_secrets.sql` gains one column on `secret_event_log`:

```sql
seal_id TEXT NOT NULL DEFAULT '',  -- identity of the sealed value; AAD binds (secret_id, seal_id)
```

`migrations.sql` gains:

```sql
ALTER TABLE secret_event_log ADD COLUMN seal_id TEXT NOT NULL DEFAULT '';
UPDATE secret_event_log SET seal_id = 'v' || value_version WHERE seal_id = '';
```

Both are idempotent under `ApplyMigrations`: the add is tolerated as a
duplicate column on re-run, and the update matches nothing once applied.
Every row gets a seal id, including carry-forward rows, because the seal id
travels with the ciphertext it names.

Seal id format for new writes: `k` followed by 20 random bytes in lowercase
base32 without padding. Legacy ids are the literal `v<n>` strings.

### Secrets package

- `SealFunc` becomes `func(secretID int32, sealID string) (SealedValue, error)`.
  The store generates the seal id inside the write transaction, where it
  computes the version today, and passes it to the callback.
- `Record` and `Meta` gain `SealID string`.
- `userSecretAAD(secretID int32, sealID string)` formats the new string. It is
  the only AAD site; `openRecordLocked` and `sealFuncLocked` are its callers.
- `pq.SecretEvent` gains `SealID`. `InsertSecretEvent` writes it.
  `InsertSecretCarryEvent` copies `p.seal_id` forward in SQL alongside
  `smk_version`, `ciphertext`, and `nonce`. `secretEventColumns`,
  `scanSecretEvent`, and the record listing query at the bottom of
  `pq/values.go` read it.
- `CreateWithVersion` and `appendVersionWithDeploymentUpdates` issue the id
  and set it on the row and the returned `Record`.
- The cache in `Manager` is keyed by row id and unchanged.

### Wire

`Secret` gains `string seal_id = 3`. A value write is then a diff of the
definition, which is what phase 2 needs. Nothing in the frontend reads it in
phase 1; `derive.js` keeps deriving version lists from `valueVersion`.

### Config no-op

In `AppendConfigVersion`, the insert callback compares `value` with
`prev.Value.Value`. When equal it inserts nothing and returns the current
`value_version` with an empty `CoreUpdate`. `SetVersionedValueWithDeploymentUpdates`
then runs its rewrite loop against that version; `replaceDeploymentReferences`
reports whether it changed anything, and deployments it did not change are
skipped rather than rewritten. The handler returns the current `ConfigEvent`.

`RenameConfig`, `MoveConfigDirectory`, and `MoveConfigSpace` already return
`nil, nil` when nothing changes, as do their secret counterparts. The value
path is the only one without the check.

### Not changed in phase 1

- Asset uploads with identical content still append a version linking the
  existing content row. Assets carry `sha256` in the definition, so the same
  no-op rule applies cleanly; it is left for the phase 2 sweep with
  deployments.
- Secret value writes with identical plaintext, per decision 3.
- The worker's `local_runtime_inputs` AEAD, which binds kind and reference
  under the machine key and is unrelated to the primary's seal.

### Steps

Each step builds and passes tests on its own.

1. **Rebase onto the value reference pair change.** `SealFunc` and the
   append paths are edited by both.
2. **Column and migration.** Schema line, the two migration statements, and
   `pq` read and write plumbing for `seal_id` with the carry-forward copy.
   Add a `pq` test that inserts a row, appends a carry event, and asserts the
   seal id is copied.
3. **AAD move.** New `userSecretAAD`, `SealFunc` signature, seal id issuance
   in the store, `Record` and `Meta` fields. Update `store_test.go`,
   `secrets_test.go`, `webuihandler/secrets_test.go`, and
   `state/snapshot_replay_test.go` for the signature and the new field.
4. **Legacy open test.** Seal a value under the old
   `s<id>:v<version>` string directly with `aeadSeal`, insert the row with an
   empty seal id, run `Open` so the migration backfills `v<n>`, unlock the
   manager, and assert `RevealByID` returns the plaintext. This is the proof
   of decision 2.
5. **`Secret.seal_id` on the wire.** Proto field, regeneration, and the
   snapshot and update paths that build `Secret` from rows.
6. **Config no-op.** The comparison in `AppendConfigVersion`, the
   changed-report from `replaceDeploymentReferences`, and tests in
   `values_test.go`: same value produces no event and no seq; same value with
   update-deployments repoints a deployment pinned to an older version and
   skips one already current; the handler returns the current event.
7. **Docs.** `docs/engineering/secrets.md`: the AAD paragraph, the key files
   note on `SealFunc`, and a history note that legacy seal ids are the
   `v<n>` strings. `docs/product/deployments.md` or the configs section that
   describes value versions: a no-op set creates no version.
8. **After rollout.** Fold the two migration statements into the
   `migrations.sql` history note per its convention. Databases from before
   this release must step through it.

### Verification

- Unit suites in `secrets`, `values`, `webuihandler`, `pq`, and `state`.
- Manual: on a dev primary, set a config to its current value and confirm
  the version list does not grow and the response carries the current
  version; set a secret and confirm the new row has a `k`-prefixed seal id;
  restart the primary and reveal a secret written before the upgrade.
- The e2e harness has no secret or config case that would catch a regression
  here; the unit coverage in step 4 and step 6 is the gate.

## Phase 2: CoreWriteUpdate

Direction agreed, not yet planned. Recorded so phase 1 is built toward it.

- `CoreWriteUpdate { seq, time, actor, repeated CoreMutation }` with
  `CoreMutation` holding exactly one of `CreateMutation`, `UpdateMutation`,
  `DeleteMutation`, each carrying `entity_type`, `entity_id`, and for create
  and update a `CoreEntity` payload with one field per entity definition.
  Numbers of the `CoreEntityType` enum equal the `CoreEntity` field numbers.
- No version ordinals, facet versions, created times, or row ids on the
  wire. Consumers derive them by folding mutations.
- `Snapshot` becomes a compacted log of `CoreWriteUpdate`: full retained
  history for secrets, configs, assets, and deployments; one synthetic create
  at current state for entities where only the present matters, keeping the
  original seq, time, and actor. One reducer serves bootstrap and steady
  state.
- Optimistic concurrency tokens become the seq of the entity's last
  mutation, not an ordinal.
- Spaces, users, and directories lose their `deleted` flags and join the
  global seq as mutations. Authz templates and global rules stop shipping as
  replacement lists. `SystemConfig` is a singleton entity.
- Observed statuses are not mutations and move to a sibling field on
  `StateStreamMsg`.
- Any precomputed projection data lives outside `CoreWriteUpdate`, in a
  sibling `derived` field that can change or be dropped without touching the
  fact schema.
- Deployment restart becomes a visible diff, for example a generation
  counter under `scheduling`, so that the no-op rule can apply to deployments
  and the empty-write restart detection can go.
- Open: whether `ValueRef` should pin by `(id, seq)` of the writing mutation
  instead of the projected `value_version`.
