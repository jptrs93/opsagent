# Data model cleanup: implementation plan

Status: implemented 2026-10-05 against v0.0.615 and unreleased; see
[Status (2026-10-05)](#status-2026-10-05) at the end for what landed, the
deviations, and the open items. The rest of this page is the plan as
agreed. Every
cluster is on v0.0.615, so the plan assumes it and opened with a sweep of
the migration code that assumption made redundant. The DSL under
`datamodel/` is the target model, carries every agreed change below, and
validates (12 files, 134 definitions) with `e2e check`, `e2e fmt --check`,
and `e2e laws`; the e2edesign example and its guide are the same files.
Every item under Proposed model changes is agreed (2026-10-05), including
`uint64` ids, the HCL block names following the type renames, and the
wrapper-message names; the self-deployment left `OpendeploySpec` for a
container spec recognised by identity as the second step of the work
plan. A dry run of the whole conversion on copies of the cluster databases
precedes the rollout; it ran clean on 2026-10-05. The generator is the
cleanproto branch `feat/go-presence-and-oneofs`, pinned by commit in
`proto_generate.sh`; nothing from it is tagged or merged to main.

The end state is one api-contract written from the DSL: every entity shape
is the DSL's, field numbers restart from 1 and equal the DSL tags, choices
are protobuf `oneof`s instead of "exactly one of these optional fields",
absence is `Maybe[T]` in Go instead of a zero sentinel, and the write log,
the materialised tables, and every other persisted protobuf blob are
rewritten once at startup from the old encoding to the new one. The old
contract stays in the tree for exactly one release as `api-contract-old`,
generated into `backend/apigenold` by the current cleanproto, so that the
conversion can decode what the cluster has on disk.

## Strategy

1. Remove the startup migration code that only a database from before
   v0.0.615 needs, as its own commit, so the conversion is written against
   one known input format.
2. Move the per-node `opendeploy` system deployment from `OpendeploySpec`
   to a container spec with a sentinel image, recognised by its identity
   (space 0, name `opendeploy`) the way `opendeploy-net` already is, so
   the workload union has one alternative before the model changes.
3. Rename `api-contract/` to `api-contract-old/` and generate it with
   cleanproto v1.25.1 (the version `proto_generate.sh` pins today) into
   `backend/apigenold`, models only (`-go.server=false`, no client). Nothing
   outside the conversion imports it.
4. Write the new `api-contract/` from `datamodel/`, by hand, and generate it
   with the cleanproto branch into `backend/apigen` and
   `frontend/src/capi`. Field numbers follow the DSL tags; the DSL already
   numbers every record and union consecutively from 1 with `id` first.
5. On the first start of the release, convert every persisted blob from
   `apigenold` to `apigen` shapes, including the complete write log, then
   drop the materialised tables and rebuild them from the converted log
   with the existing `RebuildFromLog`. Secondaries convert their caches the
   same way or discard the ones the primary resends.
6. The release after this one deletes `api-contract-old`, `apigenold`, and
   the conversion. The release is therefore a mandatory stop, like
   v0.0.614 was for v0.0.615.

## What exists

**The DSL.** `datamodel/core/*.dm` is byte-identical to
`../e2edesign/examples/opendeploy`, whose guide
(`e2edesign/docs/opsagent-model.md`) records every place it already departs
from the protos: typed `SecretRef`/`ConfigRef`/`AssetRef`/`DeploymentRef`
pairs, unions for kind-plus-optional-fields, `?T` for zero sentinels,
`[4]u8`/`[16]u8`/`[32]u8` for addresses and digests, prefix lists for the
ingress address selector, `IpFilter` entries with a mode, no `Unspecified`
enum cases, `created_at` and the counters moved to `EntityMeta`, typed
authorization selectors, the sessions keyed by their random id, and
`ScheduledInstance.id` as a UUID. `datamodel/model.root` says
`module opendeploy`, which looks for `datamodel/opendeploy`; it has to read
`module opendeploy core`, after which `e2e check`, `e2e fmt --check`, and
`e2e laws` pass (the `target/release/e2e` binary in e2edesign had to be
rebuilt first; it predated the `.dm` format).

**The generator branch.** cleanproto `feat/go-presence-and-oneofs`
(`MIGRATION.md` there is the reference) generates `Maybe[T]{Value, Present}`
for `optional` scalars and for every singular message field unless
`(cp.go_value) = true`, `[]T` instead of `[]*T` for repeated messages,
`map[K]T` for map values, and a named pointer struct per `oneof`
(`CertSourceValueOneof{Acme *AcmeCertSource, Secret *SecretCertSource}`)
held as `Maybe[...]` on the message, or directly when the oneof carries
`(buf.validate.oneof).required = true`. Every message gains `Validate` and
`EncodeChecked`; `Encode` panics on an invalid value. JSON uses `omitzero`
and needs Go 1.24 (the backend is on 1.27). Two gaps block us:

- The JS and TS generators reject any schema with a `oneof`
  (`js generation of oneofs is not supported yet`). The frontend needs a
  mapping before the new contract can be generated. The natural one mirrors Go: the group is a
  plain object under the oneof's name with exactly one own property set,
  `{value: {acme: {...}}}`, which is also how the current "exactly one
  optional field" wrappers already look in JS, so the frontend code that
  tests `x.acme !== undefined` keeps its shape.
- `cp.js_type = "number"` is accepted on `int32` and `int64` only. The DSL
  keys are `u64`; without the option a `uint64` field is a `BigInt` in JS,
  which would touch every id comparison in the frontend. Allow the option
  on `uint64` with the same safe-integer guard. The fallback is `int64`
  with `js_type = "number"`, which the branch supports today; the wire
  bytes are identical for non-negative values.

Both are small and belong on the branch before it is tagged; the new
`proto_generate.sh` pins that tag. Ids are `uint64` (agreed); the `int64`
fallback is not taken.

**The persisted surfaces.** Renumbered tags do not fail to decode; they
decode into the wrong fields, so every store below needs a format marker
and a conversion, or a stable tag layout. The inventory (every
`Encode`/`Decode` of an `apigen` type that reaches disk):

| Where | Type | Nature |
| --- | --- | --- |
| `write_event_mutations.payload` | `CoreEntity` per mutation; `User.credentials` nests an encoded `InternalUser` | Append-only history, read in normal operation too. The one real rewrite |
| `deployment_versions.value`, the three authz `data_blob`s (they hold the `*Spec`, not the entity), `network_policies.data_blob`, `users.data_blob`, `system_config.config_blob`, `scheduled_instance_status.runner_extra_blob` | entity payloads | Materialised from the log; rebuilt, never converted |
| `write_event_mutations.payload`, raw tags 15 and 16 of `Deployment` | the v0.0.614 counters `loggedDeploymentCounters` reads straight from the bytes | Needed by every rebuild; a decode-and-re-encode drops them |
| `secondary.db` `local_scheduled_instance_cache.blob` | `ScheduledInstanceState` | The worker's cold-start truth: workloads stay down without it until the primary answers |
| `secondary.db` `local_kv` `cluster_network`, `worker_cluster_net_map`, `acme_state` | `ClusterNetworkInfo` (decode panics), `ClusterNetMap`, `AcmeState` | Boot inputs for the dataplane before the primary is reachable; ACME is resent |
| `secondary.db` `scheduled_instance_status.runner_extra_blob` | `RunnerStatus` | Local history, latest row matters |
| `<data>/netproxy/netstate.pb`, `certbundle.pb` | `NetState`, `CertBundle` | Written by the agent, read by the separate `opendeploy-net` process on every node including the primary |
| `<data>-metrics/YYYYMMDD.wal` | `MetricsSample` frames, tags 1 and 2 hard-coded in `peekSample` | Today's and any uncompacted day; parquet after compaction is not protobuf |
| Litestream S3 backups | raw `primary.db` pages | Old generations stay old; a restore runs `state.Open`, so it converts |

Not protobuf: the log store (custom frames and parquet), secret and
keyslot bytes, config text, token hashes, the JSON arrays in `nodes`,
sealed runtime inputs, TLS files, release directories, and the frontend's
`localStorage`. The enum-valued integer columns (`entity_type`, `op`,
`state`, the status columns, `kind`, `roles`) keep their meaning because
the DSL keeps every existing case number and only drops the zero cases.
The cluster wire and the enrollment wire are regenerated with the rest, so
a secondary and a primary on different releases cannot talk; enrollment
carries no version check at all. See Rollout.

## Proposed model changes

Each item names the DSL change and the conversion rule. The two the user
asked for come first; the rest are proposals to accept or strike. "No
information lost" means the new value determines the old one up to the
representation.

### Asked for

1. **`name` becomes `key` on secrets, configs, and value directories.**
   `SecretFs.name`, `ConfigFs.name`, and `ValueDirectory.name` become `key`,
   matching `AssetFs.key` and `AssetDirectory.key`; the laws, the
   `value_names` table (`value_keys`), the `SecretCreateRequest`,
   `ConfigCreateRequest`, rename requests (`new_key`), the handlers, the
   HCL form, and the agent instructions follow. Pure rename.
2. **`AuthzRuleTemplate` becomes `AuthzGrantTemplate`.** Root 12, its
   `AuthzEntityRef` alternative, the `authz_rule_templates` table
   (`authz_grant_templates`), the `/v1/access/rule-templates/*` routes
   (`/v1/access/grant-templates/*`), and the request names. With it:
   `AuthzRuleTemplate.template: AuthzTemplateBody` becomes
   `spec: AuthzGrantTemplateSpec`, and the grant alternative
   `TemplateAuthzGrant` becomes `AuthzTemplateGrant` so every authz type
   shares the prefix. Pure rename.

### Identity and keys

3. **Keep integer entity ids on sessions, Nix store resets, and
   keyslots.** The DSL keys `AgentSession` and `UserSession` by their random
   string id, `NixStoreReset` by `repo`, and `SecretKeyslot` by
   `(node_id, kind)`. The write log keys every mutation by an integer
   `entity_id`, the sessions tables already have integer ids, and a string
   or composite key would need a key encoding in the log that the DSL's own
   persistence design has not fixed yet. Proposal: `id@1: u64 [key]` on all
   four, with `session_id: string [min_length = 1, mutable = false]` and a
   `unique (session_id)` law on the sessions, and `unique (repo)` on
   `NixStoreReset`. Nothing is lost; the integer ids exist today.
4. **Give keyslots their own id and a wrapping union**, as
   `docs/product/todo.md` and the e2edesign guide already record:
   `SecretKeyslot { id, smk_version, wrapped_smk, nonce,
   wrapping: MachineKey@1 { node_id: Node.id } | RecoveryCode@2 { kdf_salt } }`.
   Conversion: id 2 and `node * 256 + 1` are rewritten to fresh ids, `kind`
   and `node_id` fold into the union, `kdf_salt` lives only on the recovery
   alternative. The two laws `recovery_slot_at_node_zero` and
   `salt_follows_kind` go; one machine slot per node and one recovery slot
   stay writer-enforced until the language can state them.
5. **`ScheduledInstance.id` stays `u64`, not `uuid`.** The log store names
   files and the metrics store keys parquet rows and `MetricsSample` by the
   integer instance id on every node, and a UUID gains nothing a cluster
   needs. The DSL changes to `id@1: u64 [key]`.
6. **All ids are `u64` on the wire.** Today deployments, spaces, nodes, and
   values use `int32` and authz uses `int64`. One width everywhere,
   `uint64` with `cp.js_type = "number"`, and the DB columns are already
   64-bit. The Go sweep changes `int32` ids to `uint64` throughout the
   backend; this is the largest mechanical part of the backend work.

### Deployments

7. **Zero-means-default numerics become optional.** The DSL idiom is
   absence, not a sentinel: `ContainerReadinessSignal.timeout_seconds`,
   `ContainerRuntime.dev_shm_size_kb`, `ContainerRuntime.file_descriptor_limit`,
   `HttpsConfig.max_request_body_bytes`, `HttpsConfig.flush_interval_ms`,
   and `TlsPassthroughConfig.host_port` (0 is 443) become `?T` with
   `min = 1`. Conversion maps 0 to absent. Strings whose empty value means
   "image default" (`user`, `override_working_dir`, `override_command`)
   stay as they are, since empty and absent carry the same information.
8. **Names inside the typed alternatives drop their prefixes.**
   `AddressEnv.address_deployment_id` and `address_space_id` become
   `deployment_id` and `space_id`; `AssetEnv.asset_ref` becomes `asset`
   and the copied display key `AssetEnv.asset` becomes `key`. The display
   key stays: it is a copy of the asset key at write time and the UI shows
   it without a lookup. Pure rename.
9. **Source type names say what they are.** `NixDockerBuild` builds a
   nix2container image, not a Docker one, and `RemoteDockerImage` is any
   registry image: `NixImageBuild` and `RemoteImage`. `CustomHostMount`
   becomes `HostMount`. The HCL block names in the editor follow the type
   names (agreed), so the grammar, its generator, and the mapping in
   `components/deploymentHcl.js` change with them.
10. **`Deployment.name` gets `min_length = 1`.** The writer already
    refuses an empty name. Found while writing the contract (2026-10-05):
    `HttpBackendProtocol` had `H2c` as its only case, which would have made
    every HTTPS ingress h2c, since the DSL has no unknown case; the
    unspecified value means HTTP/1.1 in netproxy today. The enum gains
    `Http1@2` (`H2c` keeps 1, because `NetState` on the netproxy disk
    carries the same enum) and the conversion maps 0 to `Http1`.
11. **The workload union keeps only `ContainerSpec`, and the
    self-deployment becomes one.** The per-node `opendeploy` system
    deployment in space 0 still runs on `OpendeploySpec` today: `SelfSpec`
    in `lib/engine/internaldeploy/specs.go` returns a spec whose only field
    is `OpendeploySpec`, and six backend sites plus five frontend sites
    branch on it (the preparer dispatch and the two self-upgrade checks in
    `lib/engine/operator.go`, the systemd runner choice in
    `lib/engine/runner/runner.go`, the skip in
    `lib/log/logmanager/manager.go`, `IsSelfSpec`, the `WorkloadVersion`
    helpers in `model_ext.go`, the public-path rejection in `validate.go`,
    and the `opendeploySpec` fallbacks in `lib/deployment.js`,
    `deploymentCreationUpdate.js`, and `pages/status.js`). Work plan step 2
    moves it to what `opendeploy-net` already is: a `ContainerSpec` whose
    source is a remote image with a sentinel name (`SelfImage`, beside
    `NetproxyImage`), the release in `ContainerSpec.version` where
    `WorkloadVersion()` reads every other deployment's, host networking,
    default runtime; and every branch keys on the identity test
    `IsSelfConfig` that half the codebase already uses. The runtime fields
    mean nothing for a systemd-run binary, which the internal-spec guard
    already protects from edits. `EnsureSelf` repairs the stored spec on
    the primary's next start, as it does for any drift today; old log
    versions keep their `opendeploy_spec` bytes until the conversion maps
    them to the same container form with the version carried, so nothing
    is lost. The one path to test with care is the operator's first
    observation self-upgrade check, which upgrades every node. The DSL
    stays as written with `ContainerSpec` as the single alternative; a
    single tagged alternative is still a union. `container_2_spec` and
    `container_3_spec` go; nothing ever wrote them. `NetworkingMode` 0
    (unspecified, normalised to virtual on writes but present in old log
    payloads) converts to `Virtual`, which retires the legacy-mode gate in
    `webuihandler/deployments.go`.

### Authorization

12. **Selectors stay as the DSL writes them** (decided): each position is
    a record with the two optional lists `exact_…` and
    `all_…_excluding` and the `one_or_the_other` law, and the template
    positions are `AuthzArgument | that record`. In proto an optional list
    is a presence-bearing wrapper message (see the mapping table), so a
    position is a message with two `Maybe` list fields and the law is
    checked in the authz validator as today.
13. **`AuthzEntity` becomes `AuthzEntityKind`.** It names a kind;
    `AuthzEntityRef` names an instance.
14. **Conversion of the untyped selectors.** Today a position is
    `{wildcard, argument_id, include, exclude}` combined additively with
    exclusions winning. Wildcard maps to `all_…_excluding exclude`;
    include without wildcard maps to `exact_… include minus exclude`;
    an argument alone maps to `AuthzArgument`; an entity ref takes its kind
    from the rule's entity-kind position. The builtin templates and the
    seeded visibility rule convert under these rules, and the dry run on
    the cluster copies before the rollout confirms the rest.

### Nodes, instances, statuses

15. **Node times that are 0 outside their phase become optional.**
    `Node.enrollment_requested_at` and `NodeOperator.enrolled_time` are
    `?i64`. Conversion maps 0 to absent.
16. **`ScheduledInstanceStatus` drops `deployment_id` and
    `running_version`.** The DSL already does; the status table keeps a
    `deployment_id` column for its index, filled by the reducer from the
    instance row, and `running_version` is computed where
    `WithRunningVersion` computes it today. `ScheduledInstance` drops
    `deployment_spec_version` for the pinned record's
    `EntityMeta.spec_version`, which the cluster wire carries beside the
    pinned deployment.
17. **`NetPortMatch.ports` becomes `range`.** `ports.ports` reads badly.
    Conversion: `{port, port_end}` becomes `{start: port, end: port_end
    or port}`.

### Envelope and settings

18. **The envelope keeps its shape.** `CoreWriteUpdate`, `CoreMutation`
    (now a required `oneof`), `EntityMeta`, `MaterialisedEntity`,
    `CoreSnapshot`, `CoreEntityType`, and `CoreEntity` (a required `oneof`
    whose field numbers stay the root tags 1 to 21) are not in the DSL and
    carry over renumbered. `actor` stays a signed integer with the
    negative-means-agent convention; a typed actor would change every
    `author` column for no new information and is deferred.
19. **Settings unions resolve the shadowed literal.** `StringSetting` and
    `BoolSetting` become `literal | ConfigRef`; today a set reference wins
    over the literal beside it, so the conversion keeps the reference and
    drops the shadowed literal. This is the one place a stored byte is not
    carried over; the byte had no effect.
20. **`/v2/deployments/update` becomes `/v1/deployments/update`** with
    `DeploymentUpdateRequest` and a required `oneof` of the five kinds.
    The contract is breaking anyway and the `V2` suffix outlives its
    reason.

### Considered and left out

A single directory tree for assets and values (a product change, and two
trees can hold colliding sibling names today); a typed actor on the
envelope (above); dropping the asset display key (it is a historical copy
the UI reads); `IPv4Address` and `IPv6Address` as bare `bytes` (the DSL's
nominal records cost one field access); and turning `MsgToSecondary` and
`MsgToPrimary` into `oneof`s (they are one-field frames today, but the
change buys nothing and the session code is easier to keep as is).

## Mapping the DSL to protobuf

The alignment is manual, so the rules are written down once.

| DSL | Proto | Go (branch) |
| --- | --- | --- |
| `id@1: u64 [key]`, `Root.id` references | `uint64` with `(cp.js_type) = "number"` | `uint64` |
| `i32`, `i64`, `u32`, `u8`, `bool`, `string`, `bytes` | the same, `uint32` for `u8` and `u32` | value |
| `i64`, `u64` counters, seqs, sizes, byte counts, epoch milliseconds read as numbers | `int64` or `uint64` with `(cp.js_type) = "number"`; a 64-bit field without the option is a `bigint` in JS (cleanproto branch commit `9299520`), kept only for `RawLogLine.time`, `LogRecord.time` and `MetricsSample.time`, which carry nanoseconds | value |
| `?scalar` | `optional` scalar | `Maybe[T]` |
| record field (required) | message field `[(cp.go_value) = true]` | `T` |
| `?record` | message field | `Maybe[T]` |
| `A@1 \| B@2` (required) | wrapper message with `oneof value` and `(buf.validate.oneof).required = true`, field `[(cp.go_value) = true]` | `W{Value WValueOneof}` |
| `?(A@1 \| B@2)` | the same wrapper, field without `go_value` | `Maybe[W]` |
| `[](A \| B)`, `map<K, A \| B>` | repeated or map of the wrapper | `[]W`, `map[K]W` |
| `?[]T` | wrapper message `TList { repeated T values = 1; }` as a presence-bearing field | `Maybe[TList]` |
| `[]T [min_items, max_items]` | `repeated T` with `(buf.validate.field).repeated` | `[]T` |
| `[N]u8` | `bytes` with `(buf.validate.field).bytes.len = N` | `[]byte` |
| enum without a 0 case | enum with `X_UNSPECIFIED = 0` and `(buf.validate.field).enum = {defined_only: true, not_in: [0]}` | value |
| enum with a 0 case | the same cases | value |
| `i64` epoch milliseconds | `int64 [(cp.go_type) = "time.Time", (cp.js_type) = "Date"]`, `optional` for `?i64` | `time.Time`, `Maybe[time.Time]` |
| `updated_at: i64` HLC nanoseconds | `google.protobuf.Timestamp` with `go_value` | `time.Time` |
| `string [min_length, max_length]` | `(buf.validate.field).string` | value |
| `[min, max]` on numbers | `(buf.validate.field).uint64` and friends | value |

Field numbers equal the DSL tags. Union alternatives keep their DSL
alternative tags inside the wrapper. Wrapper messages are named for the
role: `Workload`, `ContainerSource`, `EnvVar`, `Placement`, `IngressConfig`,
`IngressNode`, `IpPrefix`, `CertSource`, `NetworkPolicyPeerTarget`,
`SettingValue`, `KeyslotWrapping`, `AuthzEffect`, `AuthzGrantSource`,
`AuthzArgumentValues`, and the selector positions. Laws stay in the DSL;
the ones that map to buf.validate rules are declared on the field, the
rest stay in the domain validators where they are today. Messages and
fields keep the DSL names in snake case. Request and response messages,
the envelope, and the cluster and enrollment wire are renumbered from 1 in
declaration order. Three tags are deliberately kept from the old contract
so a mismatched node fails cleanly: `MsgToPrimary.cluster_hello = 6`,
`ClusterHello.cluster_protocol_version = 2`, and
`MsgToSecondary.cluster_protocol_version = 9`. `ClusterProtocolVersion`
becomes 13. `EnrollmentHello` gains a `cluster_protocol_version` so the
primary can refuse an enrolling node of the wrong release, which it cannot
today. The four node-local formats named under Conversion (`NetState`,
`CertBundle`, `MetricsSample`, `RawLogLine`) keep every tag.

The branch validates on decode in the generated mux, so the handler-side
"exactly one of these fields" checks (`DeploymentUpdateRequestV2`,
`RepoValidateRequest`, `CertSource`, `CoreMutation`, `CoreEntity`,
`ContainerBundleSource`, `NodeSelector`) go away with the `oneof`s. The
hand-written `backend/apigen/*_ext.go` files move over with their
receivers renamed: `ValueRef` helpers become methods on the four typed
refs behind one interface, `DeploymentEvent` helpers move to
`DeploymentRecord`, and `CoreMutation.Kind/Type/EntityID/Entity/Meta`
read the oneof.

## Conversion

The conversion is one package of pure functions,
`backend/storage/primarydb/legacyconv`, importing `apigenold` and `apigen`
and nothing else: `Entity(t CoreEntityType, old []byte) (*apigen.CoreEntity, error)`
plus one function per blob kind (`InternalUser`, the three authz blobs,
`NetworkPolicy`, `SystemConfig`, `RunnerStatus` extra fields,
`ClusterNetMap`, and the rest of the inventory). It is table-driven and
unit-tested against fixtures encoded with `apigenold`, and it is the only
code that imports `apigenold`.

`pq.Open` drives it on the primary. The order: refuse a database that
v0.0.615 never opened (one that still has a `deployment_event_log` table,
or no `write_event_mutations` table at all), naming the step; copy the
file beside itself as the pre-conversion backup (the pattern
`backupLegacyDatabase` uses today, re-created after the sweep removes
it); and in one transaction
rewrite every `write_event_mutations.payload` through
`legacyconv.Entity`, lift the two raw-read counters into two new columns
`write_event_mutations.logged_version` and `logged_spec_version` (so
`loggedDeploymentCounters` reads columns instead of reserved tags and the
old bytes can go), renumber the keyslot entity ids (item 4), and write a
`format_version` row last. Then apply the new schema, drop every
materialised table, and `RebuildFromLog`. The `entity_type`, `op`, `seq`,
`time`, and `actor` columns are unchanged. The nested `InternalUser` blob
inside `User.credentials` is decoded and lifted into
`User.authentication`, so the nested encoding disappears. Secret
ciphertexts and keyslot bytes are copied as bytes; the secret AEAD is bound
to the secret id alone since v0.0.614, so nothing is unwrapped and the SMK
is not needed. `users.data_blob`, the authz blobs,
`network_policies.data_blob`, `system_config.config_blob`,
`deployment_versions.value`, and `runner_extra_blob` come back from the
rebuild and are never converted; `asset_store`, `entity_ids`,
`global_seq`, and `write_events` are untouched. `upsertBuiltinRuleTemplate`
compares stored bytes with a fresh encoding, so a non-canonical conversion
costs one harmless log write per builtin template; the rebuild writes
canonical bytes, so it does not.

The secondary converts at `sq.Open` under its own `format_version` key:
`local_scheduled_instance_cache.blob` (one `ScheduledInstanceState` per
live instance; it is what starts workloads before the primary answers, so
it is converted, not dropped), `local_kv` `cluster_network` and
`worker_cluster_net_map` (the dataplane boots from them), and
`runner_extra_blob`. `acme_state` is deleted; the primary resends it at
the session head. The enrollment acceptance writes the same caches, so
`cacheEnrollmentInstance` and the enrollment path write the new shapes
directly.

Four message trees are node-local storage or IPC formats rather than
model, and keep their tag numbers so that nothing on a node needs
converting in step: `NetState` and `CertBundle` (the agent writes
`netstate.pb` and `certbundle.pb`, the separate `opendeploy-net` process
reads them, and the two upgrade at different moments), `MetricsSample`
(today's and any uncompacted day's WAL frames, with tags 1 and 2
hard-coded in `peekSample`), and `RawLogLine`. They are declared in the
new contract with their current numbers and a comment naming the store
that pins them; their enums (`IngressKind`, `HttpBackendProtocol`,
`EndpointState`, `NetProtocol`) keep their values, which the DSL does
anyway. Litestream backups need nothing: a restore runs `state.Open` and
converts.

### Verification

Three checks, in this order:

- `legacyconv` unit tests: every message kind, every union arm, every
  zero-to-absent field, round-tripped through `apigenold` fixtures.
- The fold oracle. For each entity type, `legacyconv` applied to the old
  materialised row equals the row the rebuild produced from the converted
  log, meta included. This reuses the live-snapshot-against-rebuild oracle
  from the v0.0.615 materialisation (`pq.Snapshot` compared with a rebuild)
  with the old side converted first.
- The dry run on the cluster copies, following the recipe in the
  release-migration-check memory: open a copy of the flippingcopilot
  primary and secondary databases with the new binary, with the
  conversion in a mode that reports every payload it refuses (items 11 and
  14) without writing. This is where the authz selector combinations and
  any historic `OpendeploySpec` payload show up, and it decides whether the
  conversion rules need an extension or the data a one-off repair before
  the release.

## Rollout

Every node of a cluster has to move together: the cluster wire changes
tags, so a secondary on the new release cannot complete a hello against
an old primary beyond the version frame, and the three kept tags make that
a logged protocol mismatch rather than a decode failure. The UI's group
upgrade keeps its order (secondaries first, primary last); secondaries sit
in the mismatch retry loop until the primary follows. The release notes say
so, and that v0.0.615 must have started once before this release (the open
refuses otherwise). The release after this one removes
`api-contract-old`, `apigenold`, `legacyconv`, and the `format_version`
check, and is a mandatory stop in the same sense v0.0.614 is for v0.0.615.

## Work plan

Ordered; each step leaves the tree building and the tests green, except
step 6 which is one compile-fix sweep committed as a unit.

1. **Migration sweep.** One commit that assumes every database has
   started on v0.0.615: delete `storage/primarydb/pq/migrate_materialise.go`
   (`backupLegacyDatabase`, `renameLegacyStatusLog`,
   `materialiseLegacyTables`, `repairLegacyLog`) with
   `migrate_materialise_test.go` and `migrate_repair_test.go`, delete
   `requireCompleteWriteLog` in `write_events.go` and the calls in
   `pq.Open`, replace the history note in `pq/sql/migrations.sql` with one
   line saying migrations before v0.0.615 were removed on this date, and
   drop the `local_kv` delete in `storage/secondarydb/sq/sql/migrations.sql`.
   `seedWriteLogGenesis` stays (fresh installs) and
   `loggedDeploymentCounters` stays until step 7 moves the counters into
   columns. `pq.Open` then refuses a database with a `deployment_event_log`
   table, pointing at v0.0.615. The scratch copies of the cluster databases
   from the v0.0.615 check (opened once on v0.0.615) are the test input.
2. **Self-deployment as a container spec.** On the current contract:
   `SelfSpec` returns the container form from item 11 with a `SelfImage`
   sentinel, `IsSelfSpec` compares against it, the six backend and five
   frontend branches on `OpendeploySpec` switch to `IsSelfConfig` or to
   the sentinel image where the preparer dispatch already matches
   `NetproxyImage`, and `WorkloadVersion`/`SetWorkloadVersion` keep their
   `OpendeploySpec` fallback for reading old log versions only. `EnsureSelf`
   repairs the live spec on the next primary start. Tests: the operator's
   first-observation self-upgrade check, the systemd runner selection, the
   enrollment handler's choice of the node deployment, and the status page.
   This step can ship on its own release ahead of the rest.
3. **cleanproto branch.** JS and TS `oneof` mapping, `js_type = "number"`
   on `uint64`, tag it, pin the tag in `proto_generate.sh`. Rebuild
   e2edesign's CLI so `e2e check` runs on `datamodel/`.
4. **DSL.** Fix `model.root`; apply items 3, 4, 5, 7, 8, 9, 10, 13, 15,
   and 17; `e2e check`, `e2e fmt --check`, and `e2e laws` clean; update
   `e2edesign/docs/opsagent-model.md` with the decisions (the e2edesign
   copy and this tree's copy stay identical).
5. **Old contract.** `git mv api-contract api-contract-old`; a
   `proto_generate_old.sh` producing `backend/apigenold` with v1.25.1,
   models and operations only, no mux, no client, no JS; commit it
   generated.
6. **New contract and backend sweep.** Write `api-contract/` from the DSL
   under the mapping table, with `events.proto`, the operations, and the
   three service files renumbered; generate; port `*_ext.go`; fix the
   backend by tier from `apigen` upward (`storage/primarydb/pq` reducer
   and readers, `state`, the domain packages, `webuihandler`,
   `clusterhandler`, `scheduler`, `netmappublisher`, `app/secondary`,
   `lib/*`), replacing zero tests with `Present` tests and
   nil-pointer-choice tests with oneof pointer tests, and `int32` ids with
   `uint64`. Rename the tables (`value_keys`, `authz_grant_templates`) in
   `sql/`. 313 files reference `apigen` today.
7. **Conversion.** `legacyconv` with its fixtures, the `pq.Open` path,
   the secondary cache conversion, the fold oracle, the dry-run mode, and
   the run against the cluster copies.
8. **Frontend.** Regenerate `capi`; sweep the 36 files importing it, the
   11 reading the container spec, the HCL grammar, generator, and mapping
   (`hcl/`, `components/deploymentHcl.js`) with the renamed block names,
   the access editors and `lib/authz.js` for the renamed and
   presence-based selectors, and the fixture seam.
9. **Docs and harness.** `docs/engineering/api.md`, `secrets.md`,
   `auth.md`, the agent instructions page and its render test, the
   CLAUDE.md index; the e2e harness run; release with the rollout note.

## Decisions

All agreed on 2026-10-05: every item under Proposed model changes;
`uint64` ids with the cleanproto `js_type = "number"` addition rather than
`int64`; the HCL block names follow the type renames; the self-deployment
becomes a container spec recognised by identity rather than a second
workload alternative; the wrapper-message names in the mapping section
are the ones to use. Nothing is open; the dry run on the cluster copies is
the remaining gate before the rollout.

## Status (2026-10-05)

Implemented in the working tree on 2026-10-05. Steps 1, 2, 4, and 5 are
committed (`33547f7`, `0f1eda3`, `65061c1` with `2cb063b`, `ba39d2a`); the
rest is uncommitted and nothing is released.

### What landed per step

1. **Migration sweep.** Committed. `pq.Open` refuses a database with a
   `deployment_event_log` table (`refuseLegacyDatabase`) and names
   v0.0.615 as the step to take.
2. **Self-deployment as a container spec.** Committed.
   `internaldeploy.SelfSpec` is a `remote_image` with the `opendeploy`
   sentinel, host networking, and `RECREATE`; every branch keys on
   `IsSelfConfig`, and `deployments.EnsureSystem` rewrites an edited spec
   at the primary's start.
3. **cleanproto.** The presence-and-oneofs work lives on the cleanproto
   branch `feat/go-presence-and-oneofs`, unmerged and untagged;
   `proto_generate.sh` pins its commit `9299520` (a sibling `../cleanproto`
   checkout is used when present). Go gets `Maybe[T]`, `<Msg>ValueOneof`
   pointer structs, `Validate()`, `Encode()` that panics on an invalid
   message, and `EncodeChecked()`; JS gets `bigint` for 64-bit fields with
   `js_type = "number"` opting ids, seqs and counters into guarded numbers.
4. **DSL.** Committed, plus the `HttpBackendProtocol` `Http1` case
   (item 10). A `materialised` module with `NetState`
   (`datamodel/materialised/netstate.dm`) is new in this tree and
   `model.root` lists `core` and `materialised`. Four more fields went
   optional on 2026-10-05: `Secret.sealed`, `AgentToken.hash`,
   `UserSession.token_hash`, and `ScheduledInstanceState.status`, so the
   browser strip is an absence rather than an emptied field.
5. **Old contract.** Committed. `api-contract-old/proto_generate_old.sh`
   generates `backend/apigenold` with cleanproto v1.25.1, models only
   (`-go.server=false`).
6. **New contract and backend sweep.** `api-contract/` written from the
   DSL; `backend/apigen` regenerated (`model.gen.go`, `encode.gen.go`,
   `validate.gen.go`, `maybe.gen.go`, `mux.gen.go`, `client.gen.go`); the
   `*_ext.go` ported, with `address_ext.go` (`IpAddress`, `IpPrefix`,
   `ParseAddr`, `ParsePrefix`, `Masked()`) and `value_ref_ext.go` (the
   backend-internal `ValueRef` behind `SecretRef`, `ConfigRef`, and
   `AssetRef`) new; ids `uint64` throughout with `lib/network` keeping
   `int32` addressing ids and callers narrowing at that boundary; tables
   renamed (`value_keys`, `authz_grant_templates`); routes
   `/v1/access/grant-templates/*` and `/v1/deployments/update` with
   `DeploymentUpdateRequest.update` a required oneof. `ClusterProtocolVersion`
   is 13 and the enrollment hello now checks it too
   (`enrollment_protocol_mismatch`, 400). `HasAccess` evaluates deny rules
   held as grants in the deny pass beside the global denies;
   `cannot_deny_access` is unchanged. The deployment validator no longer
   normalises an unspecified networking mode (the schema rejects it) and
   `IpPrefix.Masked()` refuses host bits in filters. The self deployment
   and `opendeploy-net` are container specs recognised by identity; there
   is no `OpendeploySpec`.
7. **Conversion.** `storage/legacyconv`, `pq/convert_legacy.go`
   (`pq.Open` converts once; `DryRunConversion` reports without writing),
   `sq/convert_legacy.go`, the `legacyconv` unit tests,
   `TestOpenConvertsAVersion1Log`, and the env-gated cluster-copy tests
   `TestConversionOfAClusterCopy` (`OPENDEPLOY_CONVERSION_CHECK_DB`) and
   `TestConversionOfASecondaryCopy` (`OPENDEPLOY_CONVERSION_CHECK_SECONDARY_DB`).
   The mechanics are documented in `docs/engineering/api.md` under Data
   model conversion.
8. **Frontend.** `capi` regenerated. `lib/ipaddr.js` formats and parses
   `IpAddress` and `IpPrefix`. The HCL block names follow the type
   renames (`remote_image`, `nix_image_build`, `host_mount`, `https`,
   `tls_passthrough`, env `address{deployment_id, space_id}` and
   `asset{key}`); the old `container_image` and `nix_docker_build` names
   are rejected with a rename hint.
9. **Docs.** `api.md`, `auth.md`, `engine.md`, `assets.md`, `secrets.md`,
   `frontend.md`, `networking.md`, `logging.md`, the CLAUDE.md index, and
   this section. The agent instructions page and the e2e harness run are
   open (below).

### Deviations from the plan

- The counter columns are `write_event_mutations.version` and
  `spec_version`, not `logged_version` and `logged_spec_version`.
- `legacyconv` lives under `storage/`, not `storage/primarydb/`, because
  `sq` imports it too. `pq/convert_legacy.go` imports `apigenold` as well,
  for `DecodeCoreEntity` and the keyslot id callback, so `legacyconv` is
  not its only importer. `Entity` takes an `IDs` argument (the log row's
  entity id, the keyslot id callback, the template lookup, the historic
  flag, and the ULA prefix) that the plan's signature did not have.
- An unspecified `NetworkingMode` converts to `HOST`, not `VIRTUAL`
  (item 11): the runner joined the host namespace for every mode but
  `VIRTUAL`, so a saved spec without a mode ran on the host, and the
  conversion keeps what ran. The host-network gate in
  `webuihandler/deployments.go` stays for saved `HOST` specs rather than
  retiring.
- Historic rows (a mutation that is not its entity's latest) get two
  repairs instead of refusals, both found by the dry run: an env var that
  carries only an asset display key beside a `"<unknown ref>"` literal,
  residue of the value-reference-pairs rewrite, is dropped, and the first
  three system config revisions, which predate the networking init, take
  the latest revision's ULA prefix. A display key beside a literal
  converts to the literal. The latest mutation is never repaired.
- Item 16: the secondary's `scheduled_instance_status` drops its
  `deployment_id` column rather than keeping it for an index, and
  `runner_pid` is NULL while no process runs.
- The fold oracle of the Verification section was not built in that form.
  The old materialised rows are dropped before the rebuild; the check is
  that a second `RebuildFromLog` reproduces the snapshot the conversion's
  own rebuild produced, that every reader runs, and that a reopen converts
  nothing.
- Shapes the plan did not spell out: `Asset.sha256` is 32 bytes rather
  than a hex string; `SystemConfig` carries `id` (pinned to 1);
  `User.authentication` replaces the nested `InternalUser` blob under
  `credentials`; `AgentSession.token{hash, prefix, expires_at}` is one
  optional record; `Node.reported.underlay_address` and `host_addresses`
  are `IpAddress`, listen addresses `IpPrefix`, and a port forward filter
  `IpFilter{mode, prefix}`; bool and string settings are
  `value: literal | config_ref` and secret settings an optional
  `SecretRef`; `NetworkPolicy` peers are `target: space{space_id} |
  deployment{deployment_id}` with `action` `ALLOW` only.
- Behaviours the HCL can no longer express: `flush_interval_ms = -1`,
  `any_address()` as a stored selector (it is the empty list),
  `readiness_timeout_seconds = 0`; `IpFilter` `deny` entries are HCL-only,
  carried through the UI form unchanged.
- cleanproto stays a branch pinned by commit, not a release. The branch
  commit of 2026-10-05 (`9299520`) flipped the JS default for 64-bit
  integer kinds to `bigint`; `(cp.js_type) = "number"` opts a field into the guarded
  number and is now written on every 64-bit field the browser reads as a
  number. The generated `model.js` is unchanged except for the three
  nanosecond fields.

### Dry run

Run 2026-10-05 against fresh copies of the flippingcopilot primary and
secondary, the cluster on v0.0.615: zero refusals on both. The primary
converted 1477 deployment payloads, 2145 instances, and 1945 instance
statuses; the secondary kept 8 of 8 cached instance states. Three findings
shaped the rules: historic payloads carry `value = "<unknown ref>"` beside
an asset display key (residue of the value-reference-pairs rewrite), the
first three system config revisions have no ULA prefix, and an older
pre-pairs secondary copy's cache blobs did not convert while the live
secondary's do, because the session head rewrites every blob. The last
confirms the rule that a refused cache blob is dropped and refetched rather
than stopping the node.

### Rollout

Every node of a cluster moves together: a secondary on the new release
reads a protocol mismatch against an old primary and sits in its retry loop
until the primary follows, and the UI's group upgrade keeps its order,
secondaries first and primary last. v0.0.615 must have started once on the
database; `pq.Open` refuses otherwise. The pre-conversion copy
`primary.db.pre-datamodel-conversion` stays beside the database until the
operator deletes it. The release after this one removes `api-contract-old`,
`apigenold`, `legacyconv`, and the version 1 branch of the `format_version`
check, and is a mandatory stop in the same sense v0.0.614 is for v0.0.615.

### Open items

- The release notes carrying the rollout note.
- The e2edesign mirror (`examples/opendeploy`) holds the core module only.
  The `materialised` module is informational and still being developed, so
  the mirror is not synced on purpose.

### E2E runs

The harness ran green on 2026-10-05 at the secrets-page fix (1002 flow
steps, no primary panics) after two runs that each stopped at one case the
unit suites could not reach: the logs page throwing on an int64 outside
the safe integer range, and the secrets page sending zero deployment ids.
The tree then gained the bigint default, the commit pin, and the unsigned
quantities, and the squashed commit was re-run before tagging; see the
release notes for the result.

### Unsigned quantities (2026-10-06)

Every non-negative quantity that the DSL had as `i32` or `i64` is now
`u32` or `u64`, matching `NetPortMatch.start` and `end`, which were
already `u32`: the ports (`PortForward`, `TlsPassthroughConfig`,
`HttpsConfig`, and the `NetState` and `ClusterNetMap` ports), the
container limits `dev_shm_size_kb`, `file_descriptor_limit`,
`timeout_seconds`, `flush_interval_ms`, `max_request_body_bytes`,
`ScheduledInstance.instance_ordinal` and the `NetState` ordinals, and
`RunnerStatus.running_pid`. The two ingress `container_port` fields also
gained the 1 to 65535 bound the proto already carried. Kept signed: the
epoch-millisecond times (cleanproto maps `time.Time` from `int64` only),
the sequence numbers (the store's `seq` is a signed `int64` and SQLite
INTEGER is signed), and `RunnerStatus.exit_code` (-1 for a signal-killed
process). The varint encoding of a non-negative `int32` and a `uint32` is
identical, so no stored blob changes; `legacyconv` refuses a negative value
in the old blob. `lib/network` keeps `int32` ordinals and the metrics and
log formats keep `int32` ordinals, with the cast at the boundary. The
validator rejects a present zero on the optional limits, as the schema's
minimum of 1 does.
