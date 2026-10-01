# Materialised Tables Implementation Plan

Status: planned (2026-10-01). Depends on the write log shipped in v0.0.614
(`write_events`, `write_event_mutations`, the `Commit` dual write and the
startup backfill; see `docs/engineering/api.md`, The write log).

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

With v0.0.614 rolled out, the startup migrations that the log makes
unnecessary and that the reshape would otherwise have to carry through were
removed:

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

After the sweep a database older than v0.0.614 fails to open on a missing
table or column, which is the documented behaviour for skipped releases.

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

## Open questions

- Whether `deployment_versions` keeps a per-version `scheduling` snapshot or
  only the def. Today scheduling is a facet of the deployment row; a pinned
  version only needs its def, and the browser derives the rest.
- Whether to keep `scheduled_instances.space_id` and
  `deployment_spec_version` denormalised for the scheduler's hot reads or
  join `deployment_versions`.
- Payload evolution policy: a renamed or removed proto field in a
  `CoreEntity` must stay decodable from old events. Reserved tags as today,
  plus a reducer default per added field, is the proposal; a payload schema
  version on `write_events` is the alternative if that ever proves too weak.
