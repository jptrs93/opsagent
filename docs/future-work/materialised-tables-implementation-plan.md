# Materialised Tables Implementation Plan

Status: complete (2026-10-01), targeted at v0.0.615. Depends on the write
log shipped in v0.0.614 (`write_events`, `write_event_mutations`, the
`Commit` dual write and the startup backfill; see
`docs/engineering/api.md`, The write log). Every group landed straight in
the step 4 shape (writers emit mutations, the reducer owns the tables); see
"Status" at the end for what shipped and where it deviates.

## Goal

The write log is the only durable truth. Every other table in the primary
database, except `asset_store` and `global_seq`, is a materialised view: it
holds the rows its readers need, in the shape its readers want, and can be
dropped and rebuilt from the log at any time. Tables stop carrying history,
stop being able to reproduce the event stream, and stop computing "the live
row" at read time.

Three things follow and are the deliverables of this plan:

- A reducer: one function per entity type from a logged mutation to table
  writes, plus a retention projector that removes rows the projection rules
  no longer keep. The same code serves a startup rebuild, a repair command,
  the test oracle, and (last step) the live write path.
- Reshaped tables: current rows keyed by entity id with real unique
  constraints, version tables only where versions are live state, no
  autoincrement row ids, no `event_type`, no per-entity version counters, no
  `*_changed` flags, no seq-ordered indexes.
- Openings served from the tables: a stream bootstrap is a read of the rows
  the viewer may see, emitted as create mutations with each row's own `seq`,
  `time`, and `author`; replay reads the log. `BootstrapMutations`,
  `MutationsInRange`, the 21-table registry and the replay half of the
  converters are deleted.

## Projection rules

Live state is a pure function of the log and every rule below is applied in
the same way by the store (on write and on rebuild) and by the browser (on
live mutations). There are no garbage-collection events on the stream.

- Secret, config, asset: every version of every non-deleted entity, plus one
  identity row (name, directory, space, newest value version). Renames and
  moves update the identity row and are not history.
- Deployment: the newest version when the entity is not deleted, plus every
  version a retained scheduled instance pins. A deleted deployment has no
  current row; its pinned versions stay until the last pin goes.
- Scheduled instance: every non-final instance, plus per ordinal of a
  non-deleted deployment with no non-final instance, its newest final one.
- Observed statuses: the newest report per retained parent.
- Everything else (space, user, both directory kinds, network policy, node,
  authz template, grant and global rule, agent and user session, Nix store
  reset, secret keyslot, system config): the newest row per non-deleted
  entity.

The cross-entity rules that drive retention are: an instance pins a
deployment version (the one rule that keeps a non-newest row alive), and a
deployment spec or system setting pins a value version (free under "every
version", a real rule only if value versions are ever pruned).

## Preconditions

- Every cluster has started at least once on v0.0.614 or later, so its log
  reaches `global_seq`. The build that drops the old tables asserts this at
  open (`LatestWriteEventSeq == GetGlobalSeq`) and refuses to start
  otherwise, rather than falling back to the old tables.
- The log payloads carry everything the tables hold. Verified against the
  converters in `pq/mutations.go`: deployments carry `version`,
  `spec_version`, `created_time`, scheduling and the def; instances carry
  `created_at`, deployment id and version, node, ordinal, state (the table's
  denormalised `space_id` and `deployment_spec_version` come from the pinned
  deployment version at reduce time); secrets carry the sealed bytes; users
  carry credentials; sessions carry token hashes; statuses carry their HLC;
  keyslots and system config carry their full documents. A payload field
  added later is absent in older events and the reducer must default it.

## Step 0: sweep (done 2026-10-01)

With v0.0.614 tagged, the startup migrations that the log makes unnecessary
and that the reshape would otherwise have to carry through were removed
(the two production clusters were still on v0.0.612 at the time, so the
rollout order v0.0.612, v0.0.614, v0.0.615 is mandatory and the open-time
check below enforces the stop):

- `pq/migrate_event_tables.go` (`renameLegacyEventTables`,
  `copyLegacyEventTables`) and their calls in `pq.Open`.
- `pq/migrate_value_refs.go` and its test: the one-shot `(id, version)`
  blob rewrite.
- `pq/migrate_inline_assets.go` and `domain/assets/migrate_inline.go`: the
  inline asset blob externalisation.
- `pq/migrate_secret_seals.go` and `domain/secrets/migrate_seals.go`: the
  seal rebinding and the system secrets fold.
- Every statement in `pq/sql/migrations.sql`, leaving the history note.
- `users.MigrateDuplicateCredentials`, the v0.0.614 passkey credential
  clean-up, with its test.

Kept: the legacy deployment blob compatibility is proto `reserved` numbers,
not Go code, and old `deployment_event_log.value` rows still decode through
it until step 3 drops that table, so
`state/deployment_def_legacy_blob_test.go` stays until then.

After the sweep a database older than v0.0.614 is refused by the write-log
check, which `pq.Open` runs before the schema or any migration touches the
file, with a message naming the release to start on; the file is left as
it was.

## Step 1: the reducer and the rebuild (against the current schema)

`pq/materialise.go`:

- `func (q *Queries) Reduce(ctx, seq, time, actor int64, m *apigen.CoreMutation) error` with one case per
  `CoreEntityType`: create and update upsert the entity's row(s) from the
  payload, delete removes them. The reducer is the only code that knows how
  a payload maps to columns.
- `func (q *Queries) Retain(ctx, u *apigen.CoreWriteUpdate) error`, the
  projector: inspects the update's mutations and applies the retention
  rules that remove rows (an instance reaching a final state prunes the
  ordinal's older finals and any deployment version nothing pins; a
  deployment delete prunes its final instances and unpinned versions; a
  parent delete prunes its status row).
- `func (q *Queries) RebuildFromLog(ctx) error`: truncates every
  materialised table, reads `write_events` joined with
  `write_event_mutations` in `(seq, idx)` order in pages, and runs `Reduce`
  then `Retain` per event inside one transaction, then resets `entity_ids`
  (below) from the highest id seen per type.

Oracle (`state` tests): after every commit in the existing fixtures, copy
the database, `RebuildFromLog`, and compare each materialised table with
the original row by row. This replaces `AssertUpdateMatchesRows` and the
bootstrap-versus-replay fold tests as the property the store relies on.
Until step 4 the writers still write the tables directly, so this step is
where any disagreement between a writer and the reducer surfaces.

Deliverable of this step on the current schema: the reducer exists and
agrees with every writer; nothing in production changes.

## Step 2: openings from the tables, replay from the log

- The browser opening (`openEventsLocked` bootstrap branch) becomes a read of
  the materialised tables: each retained row is emitted as a create mutation
  carrying the row's `seq`, `event_time`, `author` and a payload built from
  the row (for a value version: the identity row's name, directory and space
  plus that version's value; for a pinned deployment version: that version's
  def with the deployment's current scheduling). Rows are ordered by `seq`.
  The per-entity visibility decision becomes a row filter.
- Replay reads `pq.WriteEventsInRange`. `LatestMutation` reads the log by
  the `(entity_type, entity_id, seq)` index. `LatestSeqOf` reads the current
  row's `seq`. `VisibilityChangesSince` scans the bounded replay window in
  the log and compares each payload's space with the previous payload of
  the same entity.
- `BootstrapMutations`, `RetainedScheduledInstances`,
  `promoteFirstToCreate`, `MutationsInRange`, `mutationRows`, the
  `mutationTables` registry and the replay-side use of the converters are
  deleted. `statetest.Fold` stays for the browser-side equivalence tests.
- The oracle for the opening becomes: the opening at seq S folds to the same
  entity set as `RebuildFromLog` over the log up to S, after visibility.

## Step 3: the reshape, one group per commit

Each group lands as: new schema file, reducer cases, writers switched to
upserts, readers switched to the new columns, old table dropped at the end
of `pq.Open` after a successful rebuild.

Cross-cutting shape:

- Every retained row carries `seq`, `event_time`, `author`, and
  `created_time` where the entity shows it. No `id` autoincrement, no
  `event_type`, no `version` counters, no `*_changed` flags, no
  `idx_*_seq` indexes.
- Deletes are `DELETE`. Ids are never reused, so allocation moves to
  `entity_ids (entity_type INTEGER PRIMARY KEY, next INTEGER NOT NULL)`,
  maintained by the reducer (max id seen plus one) and read by the writers.
- Uniqueness is a constraint: `UNIQUE (space_id, name)` on deployments,
  `UNIQUE (space_id, directory_id, name)` on secrets and configs,
  `UNIQUE (space_id, directory_id, key)` on assets,
  `UNIQUE (space_id, parent_id, name)` on both directory tables,
  `UNIQUE (identifier)` on nodes. The in-transaction checks stay as the
  source of the user-facing error.

Groups, in order of risk:

1. Latest-only entities. `spaces`, `users`, `value_directories`,
   `asset_directories`, `network_policies`, `authz_rule_templates`,
   `authz_grants`, `authz_global_rules`, `agent_sessions`, `user_sessions`,
   `nix_store_resets`, `secret_keyslots`, `system_config` (one row). One row
   per live entity keyed by entity id. `nodes` joins this group: one row
   with the operator and reported facts as columns.
2. Observed statuses. `scheduled_instance_status` and `node_status` keyed by
   parent id alone, upserted by the HLC merge (the HLC stays as a column for
   the stale-report check), deleted with the parent.
3. Values. `secrets`, `configs`, `assets` identity rows plus
   `secret_versions (secret_id, value_version, seq, event_time, author,
   smk_version, ciphertext, nonce, PRIMARY KEY (secret_id, value_version))`,
   `config_versions` with `value`, `asset_versions` with `size_bytes`,
   `sha256`, `storage_key` and a `sha256` index for dedupe. The partial
   `*_value_versions` unique indexes become these primary keys.
4. Deployments and instances. `deployments` current row (`version`,
   `spec_version`, `space_id`, `name`, scheduling columns) plus
   `deployment_versions (deployment_id, version, spec_version, seq,
   event_time, author, def BLOB, PRIMARY KEY (deployment_id, version))`
   retained by the pin rule; `scheduled_instances` one row per retained
   instance, upserted on each state change, with the
   `(deployment_id, instance_ordinal)` index. The "recently deleted
   deployments" view, if kept, reads the log.

The browser tree is reshaped to mirror the tables in the same step as each
group: a value is an identity plus a map of versions, a deployment is a
current row plus retained versions, so a snapshot row lands in the slot a
live mutation would update. `pruneVersions` stays as the browser-side
projector for live updates.

## Step 4: writers emit mutations

Once the reducer is the only code that maps payloads to columns, `mutate`
stops writing tables. It reads current state and allocates ids through the
transaction queries as now, returns the mutations, and `Commit` appends them
to the log, runs `Reduce` and `Retain`, then the triggers, then publishes.
The tables cannot disagree with the log by construction, the
`AssertUpdateMatchesRows` contract and the "correctness by code review" note
in api.md are retired, and the rebuild and the live path are one code path.
Domain tests assert on the published update and on table reads, as they do
today.

## Rollout

- The first start on the reshape build: open, assert the log reaches
  `global_seq`, create the new tables, `RebuildFromLog`, drop the
  `*_event_log` tables and the two old status tables. One transaction per
  group is acceptable; the whole rebuild is a few seconds at current sizes.
- `RebuildFromLog` is exposed as an operator command (`primary rebuild-tables`)
  for repair, and the oracle test runs it after every fixture commit.
- The worker, the netmap publisher, the scheduler feed and the cluster wire
  read through `pq` and `state` only, so they change with the readers and
  need no protocol change.
- Secrets stay sealed throughout: the log holds ciphertext and keyslots, the
  rebuild never needs the master key, and nothing about the key hierarchy or
  the machine keys changes.

## Status (2026-10-01)

Done, targeted at v0.0.615, out of the planned order because the value
tables were the riskiest reshape and the only ones with a per-version
history to project:

- `pq/materialise.go`: `Reduce`, `ReduceUpdate`, `RebuildFromLog`,
  `NextEntityID` over `entity_ids`, for secrets, configs, assets, value
  directories, and asset directories (the reduced types; a no-op for every
  other type). `Commit` calls `ReduceUpdate` after the triggers and before
  the log append, so these writers return mutations only (design B of step
  4 for this group): `pq.SecretMutation(meta, id, secret)`,
  `pq.ConfigMutation`, `pq.AssetMutation`, `pq.ValueDirectoryMutation`,
  `pq.AssetDirectoryMutation`, `pq.DeleteMutation(meta, type, id)`.
- Schema (`sql/schema_values.sql`, `sql/schema_assets.sql`): identity rows
  `secrets`, `configs`, `assets`; version rows `secret_versions`,
  `config_versions`, `asset_versions` keyed by `(entity id, value_version)`,
  each with its own envelope and never rewritten (`ON CONFLICT DO NOTHING`);
  `value_directories`, `asset_directories`; the namespace tables
  `value_names` and `asset_keys` with primary key `(space_id, parent_id,
  name|key)` and unique `(kind, id)`, so sibling uniqueness across kinds is
  a constraint. A delete removes identity, versions, and name; a deleted
  value's pinned refs no longer resolve (the delete guards already refuse a
  referenced value). `SecretEvent`, `ConfigEvent`, and `AssetEvent` reserve
  `version`, `event_id`, and `event_type` (tags 2, 4, 6).
- `WriteEventsInRange` and `LatestMutation` read the write log for every
  type (step 2's replay half). `MutationsInRange` and the
  startup backfill are gone; `pq.Open` refuses a log that stops short of
  `global_seq` and writes seq 0 (the two seeded spaces) on a fresh database.
  The opening is `pq.Snapshot` (`pq/snapshot.go`) over the tables, see the
  2026-10-02 entry below.
- Oracles: `TestRebuildFromLogReproducesMaterialisedTables` (rebuild equals
  the live tables row by row), `TestOpenRefusesTruncatedWriteLog`, and the
  existing fold tests extended to assets and both directory kinds.
- Operator command: `opendeploy primary rebuild-tables`.
- Migration (`pq/migrate_materialise.go`, `materialiseLegacyTables`): when
  any of the sixteen moved `*_event_log` tables is present, one transaction
  rebuilds the materialised tables from the log, compares them with the old
  tables' live rows and version payloads (sealed bytes included; nested
  documents re-encoded so an older encoder's bytes compare equal), and drops
  the old tables; any mismatch leaves the database untouched and refuses to
  start (`TestMaterialiseLegacyTablesVerifiesAndDrops` and
  `...RefusesAMismatch` drive it against the v0.0.614 table shapes). Before
  the rename and the schema, which run outside that transaction,
  `backupLegacyDatabase` copies the file to `<db>.pre-materialise` with
  `VACUUM INTO` and keeps an existing copy across retries, so a refused
  start and a return to v0.0.614 both have the original file; the refusal
  names the copy (`TestOpenRefusesADatabaseFromBeforeTheWriteLogUntouched`
  covers the pre-v0.0.614 refusal leaving the file and no copy behind).
  The v0.0.614 re-seal pass rewrites the write log's copy of
  each secret's bytes together with the table row (fixed before that
  release reached any cluster), which is what makes the log a complete
  source for the rebuild.

Also done (2026-10-01), group 1 minus the sessions, resets, keyslots, system
config, and nodes: `spaces`, `users`, `network_policies`,
`authz_rule_templates`, `authz_grants`, `authz_global_rules` as latest-only
rows keyed by entity id with the payload facts as columns, a nested
document blob where there is one, and the envelope of the last write
(`sql/schema_spaces.sql`, `schema_user.sql`, `schema_network_policies.sql`,
`schema_authz.sql`). Writers return mutations only
(`nodes.CreateSpace/UpdateSpace/DeleteSpace`, `users.Write` which now
allocates the id inside its commit, `networkpolicies.Create/Update/Delete`
with the `expected_seq` check against the row's `seq`, `authz/store.go`);
`seedGlobalRule` asks the log whether the name was ever written so an
operator's deletion sticks; `upsertBuiltinRuleTemplate` compares the row
with the shipped definition. Seq 0 of a fresh database is reduced and
logged in one transaction (`seedWriteLogGenesis`), and the schema no longer
seeds the two spaces itself. `NetworkPolicyEvent` reserves `version`,
`event_id`, `event_type` (tags 2, 4, 6); `AuthzGrantEvent` is removed. The
oracle tests cover all eleven reduced types; `TestRebuildFromLogReproducesMaterialisedTables`
covers the sixteen materialised tables.

Also done (2026-10-01), the rest of group 1 except nodes: `agent_sessions`,
`user_sessions`, `nix_store_resets`, `secret_keyslots`, `system_config`
(`sql/schema_user.sql`, `schema_nix_stores.sql`, `schema_secrets.sql`,
`schema.sql`). Sessions and resets keep their stream entity id as the row
id (the legacy id was the first log row's id; new ones come from
`entity_ids`) with `session_id` and `repo` unique; keyslots are keyed by
`node_id * 256 + kind` (`SecretKeyslotEntityID`), and the system config is
the single row 1 (`SystemConfigEntityID`) whose wire version is now the
`seq` of its last write. Writers return mutations only
(`agentsessions.Service` with its transitions as in-place row rewrites
guarded under the commit lock, `users.InsertUserSession/RevokeUserSession`,
`nixstores`, `secrets/store.go` `writeKeyslot`, `NodeSecretKeyslotDeletes`
for eviction, `systemconfig.Store.AppendRevision` returning the seq). The
agent session row drops `revoked_at`, which the payload never carried; the
double-revoke guard is status-based. The oracle tests
(`seedLatestOnlyHistory`) and the rebuild test cover all sixteen reduced
types and twenty-one materialised tables, and the migration test drives
the five legacy shapes with their id rules. `mutationTables` is down to
deployments, scheduled instances, nodes, and the two statuses.

Deviations from the plan: the writers of these groups skipped the interim
"writers still write the tables" step; the two directory kinds moved with
the values rather than with group 1 because they share the namespace
tables; the secrets Manager's cache is keyed by `ValueRef` and `Record` and
`Meta` lost their row ids; `authz_global_rules` drops the never-wired
`disabled` column.

Also done (2026-10-01), the final batch: nodes, both observed statuses,
deployments, and scheduled instances (`sql/schema_nodes.sql`,
`schema_deployments.sql`, `schema_scheduled_instances.sql`), which makes
every type a reduced type and leaves no append-only entity table.

- `nodes` keyed by entity id with the operator and reported facts as
  columns and `identifier` unique; `node_status` and
  `scheduled_instance_status` keyed by parent id, upserted under the HLC
  merge, persisting `runtime_versions` for the first time; `deployments` as
  the current row plus `deployment_versions` keyed by `(deployment_id,
  version)` with the def blob stripped of the version facts;
  `scheduled_instances` one row per retained instance with the
  `(deployment_id, instance_ordinal, id)` index.
- Writers are builders (`pq.NewNode`, `nodes.appendNodeVersion`,
  `pq.DeploymentCreateEvent`, `q.DeploymentUpdateEvent`,
  `q.DeploymentDeleteEvent`, `pq.NewScheduledInstanceEvent`,
  `pq.ScheduledInstanceTransition`, `q.NodeConnectionStatus`,
  `q.NodeObservedMetaStatus`); the converters take the verb
  (`pq.ScheduledInstanceMutation(verb, e)`, `pq.NodeMutation(verb, e)`);
  `DeploymentMutation` of a delete event carries no payload.
- `Commit` reduces the writer's mutations before the triggers and each
  trigger's tail after it, with `q.Apply` for a trigger whose later reads
  depend on its own write; the transaction `Queries` tracks the reduced
  prefix per update.
- Retention (`retainDeployment`) runs in `ReduceUpdate` per touched
  deployment as a function of table state: finals pruned when the
  deployment is deleted, superseded at their ordinal, or older than another
  final at it; statuses follow their instance; versions neither current nor
  pinned go. `RetainedScheduledInstances`,
  `ListLatestScheduledInstancePerOrdinal`, `ListLatestDeploymentEvents`,
  `ListLatestScheduledInstanceEvents`, and `mutation_tables.go` are gone;
  `ListRetainedScheduledInstances` and `ListActiveDeployments` replace them,
  and the scheduler's startup scope is the active deployments plus those
  with non-final instances.
- History reads the log: `ListDeploymentEvents`,
  `ListDeletedDeploymentEvents` (tombstone = previous payload under a
  delete envelope), the two status history readers, and
  `NextEnrollmentRequestedAt`. A stale observed report (older HLC) is
  dropped rather than kept as unpublished history.
- Proto: `DeploymentEvent` reserves the facet counters and `event_id`;
  `NodeEvent` and `ScheduledInstanceEvent` reserve `version`, `event_id`,
  `event_type`. The frontend never read those fields.
- The opening from the tables for every type (now `pq.Snapshot`, a
  deleted-but-pinned deployment as its versions with `meta.deleted` under
  the log's delete envelope); `RebuildFromLog` replays every type.
- Migration: `legacyEventTables` grows to twenty-one;
  `renameLegacyStatusLog` runs before the schema because the old status
  history table carried the new table's name; checks compare the current
  deployment rows and the retained version pairs (defs re-encoded with the
  version facts stripped), the retained instance set computed with the
  legacy per-ordinal rule, the statuses of retained instances by newest
  clock, the latest node rows with JSON lists canonicalised and the underlay
  taken from `addresses[0]`, and the newest node statuses. The migration
  test drives the five legacy shapes with the real retention cases.
- Tests: the oracle compares the retained fold with the tables for all
  twenty-one types including both statuses; the rebuild test covers
  twenty-seven materialised tables; `deployment_def_legacy_blob_test.go`
  is gone with the table it read.

Deviations from the plan in this batch: `deployment_versions` keeps the
whole `Deployment` blob (scheduling included) rather than only the def,
because a pinned version is read back as a `DeploymentEvent` and the
browser already folds the full document; the "recently deleted" view reads
the log as planned.

### Snapshot opening and entity meta (2026-10-02)

The opening is no longer mutations. `pq.Snapshot` (`pq/snapshot.go`,
replacing `pq/materialise_bootstrap.go` and `BootstrapMutations`) reads
every reduced type in a fixed type order into `MaterialisedEntity{
entity_type, entity_id, entity, meta}` entries: one per live row, one per
retained version row of a deployment or value in version order, and one
per pinned version of a deleted deployment with `meta.deleted` set, and
the handler sends it as a `CoreSnapshot{seq, entities}` in place of the
`reset` bootstrap and the `after_seq` replay, both removed together with
`pq.LatestPayload`, `pq.LatestSeqOf`, and `pq.VisibilityChangesSince`. The
derived facts
(`version`, `spec_version`, `value_version`, `created_time`, `author`)
left the payloads for `EntityMeta`, which the reducer stamps from the rows
onto every live create and update (`pq/meta.go`: `rowEnvelope`, `newMeta`,
`rowMeta`, `MetaOf`, `StampMeta` for write receipts) and which the log
never stores. Consequences in the tables: the reducer derives a
deployment's `version` and `spec_version` as it folds
(`deploymentVersionFacts`, `DeploymentSpecsEqual`) and a value's
`value_version` as the next number after its newest version row
(`nextValueVersion`); every identity row keeps the create time of its first
write across later writes (`upsert` returns the existing `created_time`);
`spaces`, `system_config`, `nix_store_resets`, `secret_keyslots`,
`node_status`, and `scheduled_instance_status` gained `created_time` in
their table definitions, filled by the legacy migration's rebuild on a
v0.0.614 database (which also moves the session `created_at` columns to
milliseconds); seq 0 carries the time the log was born so the
seeded spaces have a create time. The legacy migration no longer compares
the old tables' created columns and derives the deployment counters it
checks with the same rule as the reducer. Oracles: `assertUpdateMeta`
(every live create and update carries meta consistent with its envelope
and type), `assertSnapshotMatchesRebuild` (the live snapshot equals, entry
for entry and meta included, the snapshot of a `RebuildFromLog` run in a
rolled-back transaction after every fixture commit),
`assertSnapshotWellFormed`, and the replay fold oracle asserting the log
carries no meta; `AssertUpdateMatchesRows` is gone.

Found by the first full e2e run after retention moved into the commit
(2026-10-02): a rollover finalizes the old instance and creates its
replacement in one commit, retention prunes the finalized row inside that
commit, and the scheduled-instance subscription, which re-read each
affected instance from the tables, skipped it, so the worker never learned
the instance had ended and its netproxy kept serving the removed TLS
passthrough route. The projection now falls back to
`pq.PrunedScheduledInstanceState` (instance from the commit payload,
pinned version from the tables or the write log, status from the commit or
the log); `TestScheduledSubscriberDeliversAnInstancePrunedByItsFinalizingCommit`
covers it.

### Clean-up before tagging v0.0.615 (2026-10-02)

Nothing since v0.0.614 had reached a cluster, so the intermediate steps
were collapsed: `pq.Open` now checks the write log before anything else
and copies the file before the move (above); the separate
`rebuildForCreatedTimes` pass and the six `ALTER TABLE ... ADD COLUMN
created_time` statements are gone, since every v0.0.614 database goes
through the legacy migration's rebuild and the schema files define the
column; `migrations.sql` holds only the history note; and the helpers the
refactor left without callers were deleted (the REST list filters in
`webuihandler/visibility.go`, `nodeAllowedSpaces`, `visibleNetworkPolicies`,
`respond`/`respondErr`, `nodes.ListClusterNodes`, `nodes.MustReadLiveState`,
`users.ListPublic`/`Count`/`DedupeCredentials`, `networkpolicies.List`,
`assets.ListAssetIDsBySha`, `deployments.ValidateSpecWithAssets`,
`pq.Events`, `pq.IsReducedType`), with the enforcement tests that used the
filters now asserting on the opening snapshot of the caller's event stream.

## Open questions

- Whether `deployment_versions` keeps a per-version `scheduling` snapshot or
  only the def. Today scheduling is a facet of the deployment row; a pinned
  version only needs its def, and the browser derives the rest.
- Whether to keep `scheduled_instances.space_id` and
  `deployment_spec_version` denormalised for the scheduler's hot reads or
  join `deployment_versions`.
- Payload evolution policy, adopted for now: a `CoreEntity` field is never
  renumbered or retyped; a removed field reserves its tag; an added field
  gets its default from the reducer when an old payload lacks it, and the
  reducer may read a reserved old tag during a transition. A payload schema
  version on `write_events` is the fallback if that ever proves too weak.
