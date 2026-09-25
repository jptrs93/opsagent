# Value reference pairs: implementation plan

Status: steps 1–4 implemented, 2026-09-25, for the release after v0.0.612. The e2e run (step 5) and the migration removal (step 6) are open. See Implementation status.

References from a deployment spec, a system setting, or an ACME binding to a
secret, config, or asset value change from the event log row id to the pair
`(entity id, value version)`. The pair is the identity the domain already
uses: the secret AEAD binds `(secret_id, value_version)`, scheduled instances
pin `(deployment_id, deployment_version)`, and the HCL editor renders
`secret("space", "folder/name", 3)`. Env var references are the remaining
place where a storage row id crosses an aggregate boundary.

The change is a one-shot replacement with a startup blob rewrite, the same
pattern as the v0.0.611 scheduling migration. Workers move in the same
release behind a cluster protocol bump.

## Why

- A row id is an AUTOINCREMENT artefact. The event logs have been copied and
  rebuilt in several migrations; a `(id, version)` reference survives a
  rebuild, a row id does not.
- Reference logic becomes field compares. "Does this deployment use secret
  S" is `ref.id == S`; "repoint every reference to the new value" is "same
  id, replace version". Today both need the set of every pinnable row id of
  the entity.
- One reference shape across the system. Deployments already pin by pair.
- A floating reference is expressible later as version zero. A row id has no
  such form.

## Settled decisions

1. **One-shot, not dual fields.** Adding `secret_ref` next to
   `secret_version_id` for a transition doubles every reader for the
   transition and still needs the blob rewrite at the end to strip the old
   field from history. It is the one-shot plus a temporary second code path.
2. **Workers move to pairs in the same release.** A worker cannot translate a
   row id into a pair on its own; it holds no table mapping ids to entities.
   Keeping workers on row ids would need the primary to translate pairs back
   to ids on push, leaving a permanent bridge and making the worker's copy of
   a spec differ from the primary's. Protocol bumps are routine: the constant
   went to 11 in the v0.0.611 scheduling commit.
3. **Old tags are reserved, new tags are added.** Historical blobs are
   rewritten in place, so no reader ever needs to decode the old fields after
   startup. Reserving prevents accidental reuse.
4. **One shared `ValueRef` message.** Secrets, configs, and assets use the
   same `{ id, version }` shape. The field name says which entity it points at.
5. **The row id stays the storage join key.** `pq` resolves a pair to a row
   through a partial unique index; nothing outside `pq` sees the row id as a
   reference.
6. **Dangling ids fail the migration, except in dead env var history.** A
   reference whose row id resolves to no value-changing row panics with the
   row and field rather than writing a zero reference. The one exception is
   an env var reference in a deployment version that is neither the
   deployment's latest nor pinned by a non-finalized scheduled instance: it
   becomes the literal value `"<unknown ref>"` and the migration logs a
   warning naming the row, version, field, and old row id. The flippingcopilot
   primary had 108 such references (secret rows 5 and 18, asset rows 5, 6,
   and 19, all in env vars of versions from 2026-06-18 to 2026-07-15), whose
   value rows predate the event logs keeping every version. The literal is
   accepted knowingly: redeploying one of those versions passes validation
   and starts with the placeholder string instead of failing.

## Reference surface today

Holder | Field | Persisted where | Read by
--- | --- | --- | ---
`EnvVarValue` | `secret_version_id`, `config_version_id`, `asset_version_id` | `deployment_event_log.value`, every row | validation, reference rewrite, eviction exposure, cluster allowed refs, runtime inputs, runner, frontend
`AssetMount` | `asset_version_id` | `deployment_event_log.value` | same as above
`SecretCertSource` | `secret_version_id` | `deployment_event_log.value` | validation, ingress plan, netproxy cert cache, runtime inputs
`SecretRef`, `ConfigRef` (system config) | `version_id` at tag 3, tags 1 and 2 reserved | `system_config_revisions.blob`, every revision | settings loader, `ref_usage`, frontend settings usage
`AcmeCertBinding` | `secret_version_id` | not persisted; rebuilt from `LatestMetaByName` on each publish | netproxy on workers
Worker `local_scheduled_instance_cache` | full pushed `ScheduledInstanceState` blobs | worker SQLite | operator, runner
Worker `local_runtime_inputs` | key `(kind, ref_id)` | worker SQLite | runner
Worker asset cache | file named by asset version id | `AssetCacheDir()` | runner, retention
Cluster wire | `ClusterSecretsRequest.ids`, `ClusterConfigsRequest.ids`, `GET /v1/cluster/asset?asset_version_id=` | | worker fetch, primary allowed-refs check

Every row of `deployment_event_log` matters, not only the latest: the
scheduler reads pinned older versions and the frontend decodes full history.

The `asset_migrations` table's `old_config_version_id` and
`new_config_version_id` are system config revision ids, not value references.
They are out of scope.

## Target shapes

```proto
// model/deployments.proto
message ValueRef {
  int32 id = 1;       // stable secret_id / config_id / asset_id
  int32 version = 2;  // value_version of an immutable value-changing event
}

message EnvVarValue {
  reserved 1, 2, 5;
  reserved "secret_version_id", "config_version_id", "asset_version_id";
  optional string value = 3;
  string asset = 4;                       // display key, unchanged
  optional int32 address_deployment_id = 6;
  optional int32 address_space_id = 7;
  ValueRef secret = 8;
  ValueRef config = 9;
  ValueRef asset_ref = 10;
}

message AssetMount {
  reserved 1;
  reserved "asset_version_id";
  string container_path = 2;
  FilePermission permission = 3;
  ValueRef asset = 4;
}

message SecretCertSource {
  reserved 1;
  reserved "secret_version_id";
  ValueRef secret = 2;
}

// model/networking.proto
message AcmeCertBinding {
  string hostname = 1;
  reserved 2;
  reserved "secret_version_id";
  ValueRef secret = 3;
}

// model/system_config.proto
message SecretRef {
  reserved 1, 2, 3;
  reserved "version_id";
  ValueRef ref = 4 [(cp.go_value) = true];
}
message ConfigRef {
  reserved 1, 2, 3;
  reserved "version_id";
  ValueRef ref = 4 [(cp.go_value) = true];
}

// model_cluster_operations.proto
message ClusterSecretsRequest { repeated ValueRef refs = 2; reserved 1; }
message ClusterSecretValue   { ValueRef ref = 3; bytes value = 2; reserved 1; }
message ClusterConfigsRequest { repeated ValueRef refs = 2; reserved 1; }
message ClusterConfigValue   { ValueRef ref = 3; string value = 2; reserved 1; }
```

`EnvVarValue.asset_ref` avoids colliding with the existing `asset` display
key field; renaming the display key is a separate cleanup. The exclusive-one
check in the runner and validator counts `secret`, `config`, `asset_ref`,
`value`, and the address pair.

`/v1/cluster/asset` takes `asset_id` and `version` query parameters.

## Storage

Add to `schema_secrets.sql`, `schema_configs.sql`, `schema_assets.sql`:

```sql
CREATE UNIQUE INDEX IF NOT EXISTS secret_value_versions
  ON secret_event_log (secret_id, value_version) WHERE value_changed != 0;
CREATE UNIQUE INDEX IF NOT EXISTS config_value_versions
  ON config_event_log (config_id, value_version) WHERE value_changed != 0;
CREATE UNIQUE INDEX IF NOT EXISTS asset_value_versions
  ON asset_event_log (asset_id, value_version) WHERE value_changed != 0;
```

The index makes the invariant "one immutable row per pair" a constraint
rather than a convention, and backs the pair lookups. Schema files run on
every startup with `IF NOT EXISTS`, so no migration entry is needed for them.

New `pq` queries, each returning the same joined row the by-id query returns
today: `GetSecretVersionByRef`, `GetConfigVersionByRef`,
`GetAssetVersionJoinedByRef`. The by-id variants stay for internal callers
that already hold a row id, such as the asset content store and the secrets
manager cache.

The secrets manager `Meta` already carries `SecretID` and `Version`. Add a
`MetaByRef` lookup and keep the cache keyed by row id internally.

## Migration

One Go shape migration in `pq/migrate_value_refs.go`, run from `Open` after
`ApplyMigrations`, following the removed `migrate_scheduling.go`:

1. Build three maps `rowID -> ValueRef` with one select per event log:
   `SELECT id, secret_id, value_version FROM secret_event_log WHERE value_changed != 0`
   and the config and asset equivalents.
2. Walk `deployment_event_log` in id order. Decode each `Deployment`. For
   every old-tag reference present, set the new field from the map and clear
   the old one. Skip rows with no old-tag references. Re-encode changed rows.
3. Walk `system_config_revisions` the same way for `SecretRef` and
   `ConfigRef` at every setting site.
4. Apply all updates in one transaction. A row id absent from its map panics
   with the table, row id, and field, except for env var references in
   historical deployment versions (settled decision 6).

The old tags must remain decodable during the migration only. The generated
decoders skip unknown fields, so the migration reads the old tags through a
small hand-written decoder over the raw bytes rather than keeping the old
fields in the proto. `migrate_scheduling.go` kept the old fields in the proto
for one release instead; either works, and the raw decoder avoids a second
release to strip them.

Idempotence: a rewritten row has no old-tag bytes, so a second startup finds
nothing to do.

Add the entry to the `migrations.sql` history note when the migration code
is removed after rollout. Databases from before this release must step
through it, the same rule the v0.0.611 note states.

## Worker and cluster protocol

- `ClusterProtocolVersion` 11 to 12.
- `runtimeinputs.SecretRefs` and `ConfigRefs` return `[]ValueRef`.
  `ResolveSecret` and `ResolveConfig` take a `ValueRef`.
- `local_runtime_inputs` key becomes `(kind, ref_id, ref_version)`. The
  AEAD AAD binds all three. The table is worker-local cache; drop and
  recreate it in the worker schema rather than migrating rows, since the
  first session after upgrade refetches whatever the specs reference.
- Asset cache files are named `<asset_id>@<version>` with the `_x`
  executable suffix kept. `RetainAssets` and `secondary/retention.go` key on
  the pair. Content-addressing by sha is a possible later change; it is not
  needed here.
- The runner's `resolveEnvValue` and `container.go` mount resolution read
  the new fields.
- `netproxy/netstate.go` and `lib/ingressplan` label certificate sources
  by `secret:<id>@<version>`.

The primary sends the full live scheduled instance set for the node on every
session start, and the worker's `applySnapshot` upserts every blob and
finalizes anything absent. This is verified in
`clusterhandler/session.go` and `secondary/cluster_session.go`. So a worker
upgraded before its primary holds old-shape cached blobs until the primary
upgrades and the session reconnects. During that window a running workload
is unaffected, because reattach keys on the spec version and does not resolve
references. A workload that crashes and restarts in the window fails to
resolve, backs off, and recovers when the snapshot lands. This is the same
exposure the v0.0.611 protocol bump had.

## Primary domain changes

- `deployments/validate.go`: resolve each reference by pair; the
  own-or-global space check reads the entity from the resolved row as today.
- `values/references.go`: `deploymentUsesReferences` compares `ref.id`;
  `replaceDeploymentReferences` sets `ref.version` to the new value version
  and no longer needs the set of pinnable row ids. `versionedValueIDs` is
  removed.
- `nodes/evict.go` exposure and `clusterhandler/handler.go` allowed refs
  collect `ValueRef` sets.
- `acmeissue` publishes `ValueRef{meta.SecretID, meta.Version}`.
- `systemconfig` settings loaders and `webuihandler/ref_usage.go` resolve by
  pair.
- `webuihandler/cluster.go` eviction summary lists pairs.

## Frontend

The derived version lists in `state/derive.js` already expose `id` (row id)
and `version` (value version) per value, and each view model carries the
stable entity id. Changes are renames and shape swaps:

- `components/deploymentHcl.js`: emit `{secret: {id, version}}` and the
  config and asset forms; `versionedReferenceForID` becomes a lookup by
  entity id and version, which removes the reverse search from row id.
- `components/deploymentForm.js`: form rows hold entity id plus version.
- `lib/referenceUsage.js`, `pages/secrets.js`, `pages/assets.js`: usage
  checks compare `ref.id` against the entity id instead of a set of row ids.
- Settings pages that hold `SecretRef` or `ConfigRef`.

The frontend is embedded in the primary binary, so it flips with the primary.

## Rollout

The group upgrade overlay walks secondaries first and the primary last. With
the protocol bump, an upgraded secondary refuses the old primary and retries
with backoff until the primary is upgraded, then receives the full snapshot.
No operator action beyond the normal upgrade.

## Steps

Each step builds and passes tests on its own.

1. Partial unique indexes and pair lookups in `pq` and the secrets manager.
   Additive.
2. Proto change and regeneration. Compiler-driven sweep of the Go and
   frontend call sites listed above, with the cluster wire and protocol bump.
3. Startup migration with a test that builds a v0.0.612-shape database,
   seeds references at every holder, opens it, and asserts the rewritten
   blobs and the second-open no-op.
4. Docs: `docs/product/deployments.md`, `docs/engineering/secrets.md`,
   `docs/engineering/engine.md`, `docs/engineering/api.md`,
   `docs/engineering/networking.md`, the agent instructions, and the
   global-state-stream plan's follow-on note.
5. One e2e harness run covering a deployment with secret, config, and asset
   references, a value update with "update deployments", and a worker
   restart.
6. After every cluster has rolled forward: remove `migrate_value_refs.go`,
   record it in the `migrations.sql` history note.

## Alternatives considered

- **Dual fields for a transition.** Rejected, see settled decision 1.
- **Primary-only change with a worker bridge.** The primary translates pairs
  back to row ids when pushing specs and answering fetches. Avoids the
  protocol bump at the cost of a permanent translation and divergent spec
  copies. Rejected, see settled decision 2.
- **Keep row ids.** The HCL surface already shows pairs, so the user-visible
  form does not change either way. The gain of migrating is internal:
  rebuild-safe references and simpler reference logic.

## Implementation status

Steps 1–4 landed as planned, with these deviations and details:

- The migration's raw decoder uses `encoding/binary` varints rather than
  `protowire`, so `google.golang.org/protobuf` stays an indirect dependency.
  It rewrites only the listed paths (container specs 1–3, ingress cert
  sources, every settings `SecretRef` and `ConfigRef`). A row with no
  old-tag bytes is left byte-for-byte unchanged.
- `local_runtime_inputs` is recreated by `sq.Open`, which drops the table
  before the schema runs when it has no `ref_version` column. It is not
  dropped in the schema file.
- Worker asset cache files are named `<asset_id>@<version>` rather than the
  planned `<asset_id>_<version>`, because the layout before 2026-07-06 wrote
  `<old asset id>_<version>` and a cache hit is trusted by name alone. Files
  from both earlier layouts (that one and the bare row id) are removed by the
  retention sweep. The implicit env asset path inside the container stays
  `/opendeploy-env-assets/<asset_id>_<version>`.
- The UI's secret reveal and asset content download still address a value
  by its event row id (`event_id`). These are UI reads of one history row,
  not references.
- "Update deployments" after a value change repoints env var references by
  pair. It still does not repoint ingress `SecretCertSource` references,
  which is unchanged behaviour.
- Dangling env var references in historical versions become the
  `"<unknown ref>"` literal (settled decision 6). Dangling asset mount,
  ingress cert, and settings references, and any dangling reference in a
  latest or live-pinned deployment version, still panic.
- A dry run against snapshots of the flippingcopilot primary and prod
  secondary (2026-09-25): no duplicate value versions; 745 of 1,461 blobs
  rewritten; all 5,178 resolvable references map to exactly their original
  rows; 70 historical rows receive the literal; settings, config revisions,
  and the snapshot load; a second open is a no-op. The prod secondary drops
  21 cached runtime inputs and holds 6 of 8 cached instances with old-shape
  refs until the upgraded primary sends its snapshot.
- The prod secondary's asset cache holds about 2.9 GB of orphaned files named
  `<old asset id>_<version>` by the layout used before 2026-07-06, plus about
  2 GB named by row id. The retention sweep removes both after the upgrade;
  the row-id ones that are still referenced are downloaded again under the
  new name on the next prepare.
- The partial unique indexes are created by the schema files before the
  migration runs. A database holding two value-changing rows for the same
  `(entity id, value_version)` fails startup at index creation.

## Open items

- Whether to rename the `EnvVarValue.asset` display key while the message is
  being reshaped.
- Whether the worker asset cache should move to content addressing by sha at
  the same time.
- Whether a floating reference (version zero) is wanted; nothing in this
  plan depends on it.
