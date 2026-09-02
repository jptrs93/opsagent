# Event-log consolidation: secrets, configs, assets, network policies, nodes

## Context and goal

`deployment_event_log` and `scheduled_instance_event_log` established the
storage pattern: one append-only table per entity, one row per event, identity
denormalised onto every row, `version` per entity bumping on every event,
`created_time` copied to every row, `event_type` as the deletion truth, ids
allocated as `MAX(entity_id)+1` under the service mutex, and the log never
pruned so ids are never reused.

This plan applies the same consolidation to the five remaining multi-table
entities:

| Entity | Tables replaced | New table |
|---|---|---|
| secrets | `secrets` + `secret_spaces` + `secret_versions` | `secret_event_log` |
| configs | `configs` + `config_spaces` + `config_versions` | `config_event_log` |
| assets | `assets` + `asset_spaces` + `asset_versions` | `asset_event_log` |
| network policies | `network_policies` + `network_policy_versions` | `network_policy_event_log` |
| nodes | `nodes` + `node_versions` | `node_event_log` |

Settled by request:
- Secrets, configs, and assets carry two explicit sub-versions under the
  top-level `version`: `value_version` (content) and `space_version` (space
  assignment). Each bumps only when its facet changes.
- Asset content storage stays separate: `asset_store`, `asset_directories`,
  and `asset_migrations` are untouched.
- Deployment references to value versions must keep working unchanged.

Out of scope: `value_directories` (mutable tree, shared secrets/configs
namespace), `system_secrets`, `secret_keyslots`, `node_statuses`,
`scheduled_instance_status`, and all wire-model changes. The apigen protos
already model per-facet version lists (`SpaceVersions`, `Versions` /
`ContentVersions`), so this is a storage-only refactor; no proto, API, or
frontend change is required.

## Invariants that must survive

These are the constraints the design below is built around. Each was verified
against the current code.

1. **Value version ids are pinned everywhere and must be preserved
   byte-for-byte.** `secret_versions.id`, `config_versions.id`, and
   `asset_versions.id` are stored as int32 in: deployment def blobs
   (`EnvVarValue.SecretVersionID/ConfigVersionID/AssetVersionID`,
   `SecretCertSource.SecretVersionID`, `AssetMount.AssetVersionID`) —
   including historical `deployment_event_log` rows and delete tombstones,
   which are never rewritten; cluster settings blobs (`SecretRef`/`ConfigRef`
   in `system_config_revisions`); ACME state (`AcmeCertBinding.
   SecretVersionID`, also the netproxy cert cache key); worker-side
   `local_runtime_inputs.ref_id` (secondarydb); and worker asset cache
   **filenames** (`<AssetCacheDir>/<id>`, parsed back by retention). A changed
   id breaks pinned specs, worker caches, and settings silently.
2. **Secret AAD binds `(secrets.id, value version number)`.**
   `userSecretAAD` = `opendeploy-secret:user:s<secret_id>:v<version>`
   (`lib/secrets/secrets.go:694`). Identity ids and the per-secret value
   counter must migrate exactly; `value_version` may only ever bump on value
   writes. Name, directory, and space changes must not touch it (renames do
   not re-encrypt — pinned by `secrets_test.go:322`).
3. **Pinned versions of soft-deleted entities stay resolvable.**
   `GetConfigVersionByID` and `GetAssetVersionJoinedByID` deliberately do not
   filter `deleted_at` (pinned by `space_history_soft_delete_test.go:137`;
   engine paths still serve deleted assets' bytes). Conversely the secrets
   Manager startup load (`ListSecretVersionRecords`) deliberately excludes
   deleted secrets (`:109`).
4. **Deleting an identity frees its name, and a re-created identity gets a
   fresh id** (`space_history_soft_delete_test.go:112–133`). Identity ids are
   never reused.
5. **asset_store GC joins on sha256 across all historical versions.**
   `ListUnreferencedAssetStoreRows` sweeps store rows with no
   `asset_versions.sha256` match; deleted assets' versions keep content
   alive. Staging store rows have `sha256 = ''` — a version-side `''` value
   would pin every staging row forever, so non-value event rows must carry
   `NULL`, never `''`.
6. **A space-endpoint call that only changes the directory appends no space
   history** and does not bump the space facet
   (`space_history_soft_delete_test.go:20–28`).
7. **The sibling-name law spans three tables** (`secrets`, `configs`,
   `value_directories` — `state/values.go:30`; assets analogously span
   `assets` + `asset_directories`) and is enforced under `s.Mu`, not by SQL.
8. **The rotation flow** (`state/versioned_value_references.go`) inserts the
   new value version and every rolled deployment event in one tx sharing one
   `global_seq`, and rewrites **all historical** version ids of the identity,
   not just the latest (`TestInsertSecretAtomicallyUpdatesAllHistorical
   References`). Deleted deployments keep stale refs in their tombstones.
9. **Node enrollment accept races on the top-level node version**
   (`state/enrollment.go:155`): `MustUpsertEnrollmentRequest` returns the
   current version as the session's `expectedVersion`; accept aborts with
   `ErrEnrollmentRequestChanged` on mismatch.
10. **Every network-map input write advances `global_seq` in its own tx**;
    the publisher errors if map content changes without a seq advance
    (`publisher.go:147`). A seq advance without content change is harmless.
11. **Network policy ids are never recycled** (schema comment): a rule
    referencing a deleted entity compiles to vacant prefixes.
12. **Nothing pins node or policy version-row ids.** Node version numbers
    are consumed only by the accept race guard; `node_versions` history has
    no production reader; nothing anywhere references
    `network_policy_versions.id`. These two logs may re-id rows freely as
    long as `(entity_id, version)` pairs are preserved.

## Target schemas

Common columns on all five (same meanings as `deployment_event_log`):
`id INTEGER PRIMARY KEY AUTOINCREMENT`, `global_seq`, `event_time` (epoch ms),
`created_time` (first event's `event_time`, copied to every row), `author`,
`<entity>_id`, `version` (bumps on every event), `event_type` (AuthzVerb: 1
create / 2 update / 3 delete), `UNIQUE (<entity>_id, version)`.

Identity facets (name, directory, space, node identity columns) are
denormalised onto **every** row, so the latest row per entity is the complete
current state. Value payloads are present **only on rows that write the
value** and are `NULL` otherwise — that nullability is what makes a row a
pinnable value version (see Event semantics).

```sql
CREATE TABLE IF NOT EXISTS secret_event_log (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,  -- value rows: the pinnable version id
    global_seq         INTEGER NOT NULL,
    event_time         INTEGER NOT NULL,
    created_time       INTEGER NOT NULL,
    author             INTEGER NOT NULL,
    secret_id          INTEGER NOT NULL,
    version            INTEGER NOT NULL,
    value_version      INTEGER NOT NULL,   -- AAD-bound; bumps only on value writes
    space_version      INTEGER NOT NULL,   -- bumps only on space moves
    name               TEXT    NOT NULL,
    value_directory_id INTEGER NOT NULL,
    space_id           INTEGER NOT NULL,
    smk_version        INTEGER,            -- NULL unless this event writes the value
    ciphertext         BLOB,
    nonce              BLOB,
    event_type         INTEGER NOT NULL,
    UNIQUE (secret_id, version)
);
```

`config_event_log`: same shape with `value TEXT` (nullable) as the payload.

`asset_event_log`: `key` + `asset_directory_id` as the identity facets;
payload `size_bytes INTEGER`, `sha256 TEXT` (both NULL on non-value rows —
invariant 5). Keep a partial index
`ON asset_event_log (sha256) WHERE sha256 IS NOT NULL` for the GC join.

```sql
CREATE TABLE IF NOT EXISTS node_event_log (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq     INTEGER NOT NULL,
    event_time     INTEGER NOT NULL,
    created_time   INTEGER NOT NULL,
    author         INTEGER NOT NULL,
    node_id        INTEGER NOT NULL,
    version        INTEGER NOT NULL,
    name           TEXT    NOT NULL,
    identifier     TEXT    NOT NULL,
    enrolled_time  INTEGER NOT NULL,       -- 0 until accept; stamped by the accept event, copied forward
    status         INTEGER NOT NULL,
    roles          TEXT    NOT NULL,       -- JSON, unchanged shapes
    addresses      TEXT    NOT NULL,
    wg_public_key  TEXT    NOT NULL,
    allowed_spaces TEXT    NOT NULL,
    event_type     INTEGER NOT NULL,
    UNIQUE (node_id, version)
);
```

`network_policy_event_log`: `policy_id`, `data_blob BLOB NOT NULL` (delete
rows carry the last content, like deployment tombstones). No sub-versions —
single facet.

Nodes get no sub-versions either: nothing consumes them, and the accept race
guard wants the top-level version (any concurrent change should abort the
accept; bumping on rename is a strictly safer guard than today).

## Event semantics

- **Top-level `version`** bumps on every event, computed in Go from the
  previous row (`prev.Version + 1`, deployments pattern), enabling future
  optimistic-concurrency use. Policies keep their existing
  `expectedVersion` conflict check against it unchanged.
- **`value_version`** bumps iff the event writes the value; equals the old
  `secret_versions.version` / `config_versions.version` /
  `asset_versions.version` counter. For secrets this is the AAD-bound number
  (invariant 2).
- **`space_version`** bumps iff `space_id` changes. Directory-only moves
  through the space endpoint bump neither sub-version (invariant 6).
- **Pinnable value version id** = the `id` of a row whose payload is non-NULL.
  Resolvers use `WHERE id = ? AND <payload> IS NOT NULL` so a
  rename/space/delete row id can never resolve as a value. Only value-row ids
  are ever handed out (`InsertXxxVersion` replacements return them).
- **Event kinds per value entity**: create (v1, writes the value), set value,
  rename, move directory, move space, delete. All map onto
  `event_type ∈ {1,2,3}`; the facet is recovered from sub-version deltas and
  payload nullability when reconstructing the per-facet wire lists.
- **Deleted** = latest row has `event_type = 3`. Delete rows carry the
  identity facets forward and a NULL payload. There is no un-delete (none
  exists today for any of these entities), and like deployments, delete is
  terminal: append paths panic when the latest row is a delete. Name/key
  uniqueness scans consider only entities whose latest row is not a delete,
  which frees the name (invariant 4).
- **Current state** = `MAX(version)` row per entity, everywhere. This
  replaces today's three inconsistent "newest" definitions (space = max
  space-row id; policies = max version-row id; nodes = max version).
- **Id allocation**: `MAX(<entity>_id) + 1` over the log inside the creation
  tx, serialized under `s.Mu` (deployments/scheduled-instances pattern).
  Because migration preserves ids and the log is never pruned, this continues
  each AUTOINCREMENT sequence exactly and never reuses an id.

### Reads, restated against the log

- Secrets Manager startup load: value rows (`ciphertext IS NOT NULL`) of
  secrets whose latest row is not a delete, joined with the latest row for
  current name/space (invariant 3, both halves).
- `GetConfigVersionByID` / `GetAssetVersionJoinedByID` equivalents: resolve
  the pinned row by id, overlay current identity/space from the entity's
  latest row, **no deleted filter** — deleted entities' latest row still
  carries usable identity facets.
- Per-facet wire lists (`SpaceVersions`, `Versions`/`ContentVersions`):
  scan the entity's rows in version order, emit a space entry when
  `space_version` bumps and a value entry when the payload is non-NULL.
  The conversions already group and reverse in Go; only the input changes.
- Sibling-uniqueness counts: latest-row scans filtered on
  `event_type != 3`, same shape as today's correlated-subquery counts, still
  under `s.Mu` (invariant 7).
- asset_store GC: `NOT EXISTS (SELECT 1 FROM asset_event_log e WHERE
  e.sha256 = s.sha256)` — NULL payloads keep staging rows sweepable, and
  historical value rows (including deleted assets') keep content pinned
  (invariant 5).
- Nodes: `nodeCurrentFrom` becomes a self-join on `MAX(version)`;
  `node_statuses` LEFT JOIN unchanged; the `SELECT id FROM nodes WHERE
  identifier = ?` subqueries resolve against latest rows. `json_each` over
  `roles` works unchanged. `ListEnrollmentNodeRows` orders by
  `created_time DESC`.

### Writes, restated against the log

Every write becomes "read latest row → derive next row → append in tx with
`NextGlobalSeq`". Three flows deserve care:

- **Secret create/set**: the seal callback runs inside the tx and needs
  `(secret_id, value_version)` before insert; secret_id now comes from
  `MAX+1` (create) or the latest row (set) instead of `RETURNING id`. Same
  tx boundaries as today (`secrets_store.go:113/:155`).
- **Rotation** (`setVersionedValueWithDeploymentUpdatesLocked`): unchanged
  shape — one tx, one seq shared by the new value event row and every rolled
  deployment event. `versionedValueIDs` becomes "all value-row ids of the
  identity, in version order" (invariant 8).
- **Node writes**: `appendNodeVersion`'s diff-gate (no-op ⇒ no row, no seq)
  carries over on the versioned facets. `RenameNode` and
  `UpdateNodeAccepted` stop being in-place UPDATEs and become events — see
  Behavior changes.

### SQL uniqueness moving to Go

The node log loses `UNIQUE(name)` and `UNIQUE(identifier)`. Replacements,
all under `s.Mu` (precedent: deployment identity, scheduled-instance
at-most-one-serving):

- Rename: explicit latest-row name scan before the append; return
  `DuplicateNodeNameErr` directly. Deletes the error-string match at
  `webuihandler/cluster.go:49` — the only such match in the repo.
- Enrollment accept: the same scan, giving the accept path real
  duplicate-name handling for the first time (today it 500s on the raw
  constraint error).
- Identifier uniqueness: enforced by the `MustUpsertEnrollmentRequest`
  lookup-then-create branch, which already runs under `Mu` in one tx; the
  scan just moves from `UNIQUE(identifier)`-backed `ErrNoRows` to a
  latest-row lookup.

## Migration

Same crash-safe pattern as v0.0.559: schema pass creates the empty logs;
migrations copy guarded by `WHERE NOT EXISTS (SELECT 1 FROM <new_log>)`;
drops follow; "no such table" on re-run is tolerated. Given the merge logic
below (interleaving, synthetic rows, explicit id preservation), implement the
three value-entity copies as a **Go-side one-time migration** (precedent:
v0.0.549 / v0.0.554) running after ApplySchema, with the drops staying in
`migrations.sql`; nodes and policies are simple enough for pure SQL.

Per value entity (secrets shown; configs/assets identical in shape):

1. **Value rows first, ids preserved.** Each `secret_versions` row becomes a
   value event with an **explicit `id` = the old version-row id**
   (invariant 1). `value_version` = old `version`. Inserting explicit ids
   first leaves AUTOINCREMENT positioned past the max, so subsequent rows
   and all future events allocate above every pinned id — no collisions.
2. **Space rows 2..n** (the first space row folds into the create event)
   become update events with fresh ids, bumping `space_version`.
3. **Soft-deleted identities** get a synthetic delete event (`event_time` =
   `deleted_at`, author 0, NULL payload).
4. **Ordering/top-level version**: merge each entity's events by
   `(global_seq, created_at)` and number `version` 1..n. Interleaving
   accuracy between facets is cosmetic — the wire model surfaces per-facet
   histories, and current state depends only on the final row — but seq
   ordering keeps the audit trail honest. Rows stamped seq 0 predate the
   counter and sort by `created_at`.
5. **Unreconstructable history**: renames and directory moves were in-place
   UPDATEs, so historical rows get the **current** name/`value_directory_id`
   denormalised onto them. `created_time` = the identity row's `created_at`.
6. Identities with zero version rows (already invisible to every reader) are
   skipped.

Nodes: each `node_versions` row becomes an event (v1 = create), preserving
`(node_id, version)` pairs (invariant 9 — although in practice enrollment
sessions are in-memory and do not survive the restart); current
`name`/`identifier` denormalised onto every row; `created_time` =
`nodes.created_at`; `enrolled_time` = `nodes.enrolled_at` on every row (the
accept boundary is not reconstructable). Policies: each version row becomes
an event; `deleted_at != 0` appends a synthetic delete carrying the last
content blob.

Drops: `secrets`, `secret_spaces`, `secret_versions`, `configs`,
`config_spaces`, `config_versions`, `assets`, `asset_spaces`,
`asset_versions`, `nodes`, `node_versions`, `network_policies`,
`network_policy_versions`.

## Code sweep, per entity

Ordered smallest-blast-radius first; each stage is independently shippable
in the same release and follows the same script: schema + queries + sqlc
regen → pq hand-written reads → state write flows → callers → migration →
tests.

**Stage 1 — network policies** (pattern prover, nothing external pins ids):
`pq/network_policies.go` (single read becomes MAX(version) join),
queries.sql policy block, `state/network_policies.go` (create/update/delete
become appends; delete gains an authored, timestamped event row; the
discarded-seq hack at `:155` becomes the delete event's own seq),
`network_policies_test.go` (reads physical tables directly).

**Stage 2 — nodes**: `pq/nodes.go` rewrite (current-join, insert/append,
identity updates become events), `state/nodes.go` + `state/enrollment.go`
(rename/accept as events, Go-side uniqueness), `webuihandler/cluster.go:49`
error-string match removal, `enrollmenthandler` unchanged in shape
(`expectedVersion` still the top-level version), `state/service_test.go`
raw-table fixtures.

**Stage 3 — assets**: `pq/assets.go` (`assetSpaceExpr` and joins collapse to
latest-row reads), asset blocks in queries.sql, `state/assets.go` write
flows, GC query + partial sha index, `webuihandler/assets.go` version-id-set
helpers (now "value-row ids"), `space_history_soft_delete_test.go`.

**Stage 4 — configs, Stage 5 — secrets** (shared shape; secrets last
because of the seal-in-tx coupling): `pq/values.go`, config/secret blocks in
queries.sql, `state/user_configs.go` / `state/secrets_store.go`,
`state/versioned_value_references.go` (`versionedValueIDs`),
`lib/secrets/secrets.go` store interface + startup load (cache stays keyed
by value-row id; `LatestMetaByName` unchanged), conversions
(`user_config_conv.go`, `secret_conv.go`), `webuihandler`
secrets/user_configs/deployment_validation/config.go resolvers (signatures
unchanged — only the pq layer beneath them moves), value-directory delete
counts (`CountSecretsInDirectory`/`CountConfigsInDirectory` become
latest-row counts).

## Behavior changes (accepted, to note in the release)

1. Renames, directory moves, and deletes of secrets/configs/assets gain
   history rows with author and `global_seq` — today they are untracked
   in-place UPDATEs. Harmless seq advances (invariant 10).
2. Node renames become versioned events (visible in history, seq-advancing)
   and duplicate-name detection moves to an explicit check; enrollment
   accepts colliding on name get a proper error instead of a raw 500.
3. Network policy deletes gain an event row with author and timestamp.
4. Top-level `version` counters exist for all five entities (new,
   internal-only until something consumes them).
5. The ingress-cert `SecretVersionID` is still not rotated by the rotation
   flow — pre-existing gap, explicitly out of scope here.

## Rollout

Same one-way door as v0.0.559, times five: after first boot of the new
binary the legacy tables are dropped; rollback requires a database restore,
and a pre-migration binary must never run against a migrated database (its
`CREATE TABLE IF NOT EXISTS` would resurrect empty legacy tables and
reallocate ids from 1 — for secrets that also collides with AAD-bound
identity ids). Land all five stages in one release so the door is paid once.

Verification:
- Per-entity migration tests in `pq` (v0.0.559 pattern: build the legacy
  shape, `Open`, assert copied rows, preserved ids, drops, reopen
  idempotency). For secrets, additionally decrypt a migrated ciphertext
  through the Manager to prove the AAD tuple survived.
- Targeted unit tests: pinned-ref resolution across migration (live,
  historical, and deleted identities); rotation rolling all historical ids;
  name-reuse-after-delete with fresh identity id; directory-only space call
  appending nothing; enrollment accept race; policy version conflict.
- Boot against a copy of a real pre-migration database; check secrets
  reveal, deployment validation, asset serving, and history views.
- Full-wipe e2e run (`testing-vms/run.sh`).
- Sweep the migration code in a later release once every cluster has rolled
  forward, per the `migrations.sql` history-note convention.
