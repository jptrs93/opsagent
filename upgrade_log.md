# Upgrade log

Notes for operators upgrading a cluster, newest release first. Each entry
covers what changes on disk and on the wire, what to check before upgrading,
and what to expect during and after the rollout.

## v0.0.619 (released 2026-11-24)

v0.0.618 was tagged and never published: its build stopped at the new
vulnerability check on an OpenTelemetry advisory published that week
(GO-2026-6974). This release is the same code with that module bumped.

### What changed

- **Users sign in with their own username and password.** A passkey is
  optional per user, added from Users & roles › You. The new setting
  `auth.require_passkey` (installer `--require-passkey true`, restore
  override `REQUIRE_PASSKEY`) turns password sign-in off. The old
  `auth.password_login_enabled` setting and `--password-login` flag are
  accepted and ignored.
- **The master password signs nobody in.** It opens a 10-minute root
  session that can only create users with their grants, issue single-use
  enrol or recovery links (`/enrol/<token>`, valid 7 days), and open a
  session for a user. It no longer takes a username, creates a user, or
  grants `cluster_admin`; a new user gets exactly the grants its creator
  chooses. Signing in as any user with the master password is gone.
- **Host ports below 1024 need `use_privileged_ports`.** A deployment that
  forwards a host port below 1024, TCP or UDP, or that takes a TLS
  passthrough host port below 1024 other than 443, can only be created or
  updated by a caller holding the new `use_privileged_ports` permission on
  it. Like the other two host permissions it is checked against the saved
  and the proposed spec on every update, including a version change and a
  stop or start. Cluster admins hold it; space admins and agent sessions do
  not.
- **A port forward must be free on its node.** A save is refused when
  another deployment on the node forwards the same protocol and port, or
  when the port belongs to the platform: the Web UI, cluster and enrollment
  listeners on the primary, and the WireGuard port (51833/UDP) on every
  node. UDP forwards are checked for the first time. A settings change that
  moves a listener onto a forwarded port is refused.
- **A port forward no longer matches loopback.** The DNAT rules exclude
  `127.0.0.0/8` and `::1`. Before, a host-local connection to
  `localhost:<port>` on a forwarded port timed out, whether it was meant
  for the forward or for a host service on the same port. Now it reaches
  the host service. Reach a forward from the host on a node address.
- **containerd 2.3.7 and runc 1.5.3** replace 2.3.5 and 1.5.1 as the
  bundled runtime, and the agent is built with Go 1.27.4. The release
  build now fails when govulncheck finds a reachable advisory.

### Before upgrading

- **Check that every user has a way in.** A user with a passkey signs in
  with it as before. A user who relied on master-password login has no
  password of their own after the upgrade: open a root session with the
  master password and issue them a recovery link, or open a session for
  them, and have them set a password under Users & roles › You.
- **Password sign-in is on unless `auth.require_passkey` is set.** No
  existing user has a password, so nothing opens until a user sets one.
  Set `auth.require_passkey` after the upgrade to keep the cluster
  passkey-only.
- **List the deployments that hold a port below 1024.** After the upgrade
  only a caller with `use_privileged_ports` can deploy a version to them,
  stop or start them. If a space admin or an agent session runs those
  deploys, grant the permission on those deployments first thing after the
  upgrade; the permission cannot be granted before it.
- **Check custom rules that use `all_verbs_excluding`.** Such a rule
  conveys `use_privileged_ports` from the first start on this release,
  because the list was written before the permission existed. Add the
  permission to the list where that is not intended. The builtin
  `space_admin` template is updated at startup.
- **Check for forwards that share a port.** Two deployments that already
  forward the same protocol and port on one node keep running, the older
  one receiving the traffic, but neither can be saved again until one
  changes its port. The same applies to a deployment that forwards a
  platform port, and no cluster settings change is accepted while one
  exists.

### Rollout

- **Every node restarts containerd once**, when its agent first starts on
  this release and installs the new runtime.
- **Do not roll the primary back** once it has started on this release. The
  first start adds columns to the `users` and `user_sessions` tables and
  rewrites the builtin grant templates to name the new permission, and an
  older binary rejects a permission it does not know when it encodes a
  template.
- Workers see no change on the cluster connection, so the usual order
  holds: secondaries first, primary last.

### After upgrading

- Existing users show as pending on the Users page until their next
  sign-in. Their sessions and grants are unaffected.

## v0.0.617 (released 2026-10-08)

### What changed

- **One node projection per node replaces the per-worker table reads.**
  The primary publishes each node's scheduled instances, network map, and
  ACME subset from one in-memory fold of the write stream
  (`app/primary/nodepublisher`). A node receives its complete
  `NodeProjection` at enrollment and at the head of every session, then one
  per commit that changed something it holds, and applies each in one
  transaction. The secondary replays its status history above the
  watermark carried on each instance row instead of the primary echoing
  status rows. Cluster protocol 13 → 15.
- **Network maps are scoped per node.** A node's map holds the nodes and
  routes within its policy reachability: its own placements, the spaces
  its placements may initiate to, and the global space. A cluster whose
  deployments are all host-mode sees each node's map shrink to the node
  itself, which removes the WireGuard peer between nodes that no workload
  traffic used. Per-node stamps live in the new `node_netmap` table,
  written on first start.
- **Invariants are enforced at write time only.** Enrollment and the
  session hello reject a node without a WireGuard key or with an underlay
  address family different from the cluster's; the primary checks its own
  family at boot and refuses to start on a conflict. Deployment validation
  stores the canonical ingress hostname, and a listen setting with a 4in6
  host is rejected. The render, the netstate writer, and the ingress
  evaluator read the stored values without re-checking them, and the
  evaluator's lower-id fallback for two stored claims that collide is gone.
- **The settings page loads on a cluster whose system config revision
  sits at seq 0**, which is any cluster that has not saved settings since
  the v0.0.614 backfill.
- **Deleting a space is refused while anything still uses it.** The check
  runs under the write lock and covers deployments, secrets, configs, value
  and asset directories, assets, network policies, and access grants,
  templates, and global rules that name the space; the refusal lists every
  kind in the way. Before, only active deployments blocked the delete.
- **The data model conversion and the previous contract are gone.**
  `api-contract-old`, `backend/apigenold`, the conversion package, the
  primary and secondary open-time conversions, and the pre-v0.0.615
  refusal are removed. The primary checks the `format_version` row and the
  secondary the `format_version` cache key, and both refuse to start on any
  other value, naming v0.0.616 as the release to start first.

### Before upgrading

- **Every node must have started v0.0.616 once.** A primary that skips it
  refuses to start with `this database predates the data model contract:
  start it on v0.0.616 once before upgrading`, and a secondary with `start
  this node on v0.0.616 once before upgrading`. There is no repair in this
  release; install v0.0.616, start, then upgrade.
- **Keep a v0.0.616 binary.** It is the only release that can open a
  database or a litestream backup written before the data model
  conversion. A restore from an older backup goes through it first.

### Rollout

- Secondaries first, primary last, as the group upgrade orders it. A
  secondary on this release reads a protocol mismatch against a v0.0.616
  primary and retries until the primary follows; its workloads keep
  running from the local cache.
- A node whose row has no WireGuard key or whose underlay family differs
  from the cluster's is refused at its next hello. No known cluster has
  such a node.

### After upgrading

- The `primary.db.pre-datamodel-conversion` copy beside each primary
  database can be deleted once the cluster has run this release.

## v0.0.616 (released 2026-10-06)

### What changed

- **The API contract is generated from the data model DSL under
  `datamodel/`.** Every protobuf message, enum, and endpoint shape in
  `api-contract/` is written from the model, and the Go and JS code is
  generated by the cleanproto branch `feat/go-presence-and-oneofs`, pinned
  by commit. Optional fields are `Maybe[T]` in Go instead of pointers,
  unions are `oneof` wrappers, ids and seqs are `uint64`, and ports,
  container limits, instance ordinals, and pids are unsigned. Requests
  are checked against the schema's buf rules on decode and responses
  before encode.
- **Request and model shapes change for API clients.** Secret, config,
  asset, and directory create requests take `key` instead of `name`, and
  renames take `new_key`. `/v2/deployments/update` is
  `/v1/deployments/update` with a required `update` union. Access rule
  templates are grant templates at `/v1/access/grant-templates/*`. An env
  var that addresses a deployment carries `address{deployment_id,
  space_id}`. Node addresses are typed `IpAddress` and `IpPrefix` values.
  The agent instructions page describes the new shapes; an agent working
  from an older copy of that page has to fetch it again.
- **HCL block names follow the type renames.** The source blocks are
  `remote_image` and `nix_image_build`; the editor rejects the previous
  `container_image` and `nix_docker_build` names with a rename hint. Mounts
  and ingress use `host_mount`, `https`, and `tls_passthrough`. A spec can
  no longer say `flush_interval_ms = -1`, `readiness_timeout_seconds = 0`,
  or an unspecified networking mode.
- **The self deployment and `opendeploy-net` are container specs
  recognised by identity.** There is no `OpendeploySpec`; the primary
  rewrites an edited self deployment spec at start.
- **64-bit fields reach the browser as `bigint`** unless the schema opts
  them into a guarded JS number. Ids, seqs, and counters are numbers;
  nanosecond times are `bigint`.
- **Keyslots have their own id and a wrapping union.** The machine slot id
  `node * 256 + 1` and the recovery slot id 2 are rewritten to fresh ids at
  conversion; the wrapped SMK, nonce, and salt bytes are copied unchanged.
- **Cluster protocol 12 → 13**, checked on the enrollment hello as well as
  the session hello.
- **The previous contract ships one last time** as `api-contract-old` and
  `backend/apigenold`, used only by the conversion. The release after this
  one removes both, the conversion package, and the version 1 branch of
  the `format_version` check, so it is a mandatory stop in the same sense
  v0.0.614 is for v0.0.615.

### Before upgrading

- **The primary must have started on v0.0.615 at least once.** `pq.Open`
  refuses a database that still has a `deployment_event_log` table or no
  `write_event_mutations` table, naming v0.0.615 as the step to take.
- **Back up `primary.db`.** The conversion rewrites every write log payload
  and is one-way. The primary also writes its own copy beside the database
  (below).
- **Dry-run the conversion against a copy of `primary.db`.** With the new
  binary's tree:

  ```sh
  OPENDEPLOY_CONVERSION_CHECK_DB=/path/to/copy.db \
    go test ./storage/primarydb/pq/ -run TestConversionOfAClusterCopy -v
  OPENDEPLOY_CONVERSION_CHECK_SECONDARY_DB=/path/to/secondary-copy.db \
    go test ./storage/secondarydb/sq/ -run TestConversionOfASecondaryCopy -v
  ```

  The primary test fails on any payload the conversion refuses; a refusal
  is a failed rollout, not something the primary repairs at start.
- **Agents and scripts that call the API** need the new request shapes
  before the primary moves. Browser sessions and agent tokens stay valid.

### What the primary does on first start

1. Copies `primary.db` to `primary.db.pre-datamodel-conversion` beside it.
   The copy stays until the operator deletes it.
2. In one transaction: rewrites every `write_event_mutations.payload` from
   the old contract to the new one, lifts the two deployment counters into
   the `version` and `spec_version` columns, renumbers the keyslot entity
   ids, and writes the `format_version` row. `write_events`, `asset_store`,
   `entity_ids`, and `global_seq` are untouched.
3. Applies the new schema, drops every materialised table, and rebuilds
   them from the converted log. Secret ciphertexts and keyslot bytes are
   copied as bytes; nothing is unsealed and the machine key is not needed.
4. Two historic-row repairs are logged rather than refused: an env var
   that carries only an asset display key beside a `"<unknown ref>"`
   literal is dropped, and the first system config revisions written before
   the networking init take the latest revision's ULA prefix. The latest
   mutation of an entity is never repaired.
5. A later start converts nothing.

### What each secondary does on first start

- Converts its cached `ScheduledInstanceState` blobs, the `cluster_network`
  and `worker_cluster_net_map` entries, and the runner extra blobs under
  its own `format_version` key, so workloads can start before the primary
  answers. A cached blob the conversion refuses is dropped and fetched
  again at the session head rather than stopping the node.
- Deletes `acme_state`; the primary resends it at the session head.
- Drops the `deployment_id` column from `scheduled_instance_status`.
- `netstate.pb`, `certbundle.pb`, the metrics WAL frames, and the raw log
  lines keep their tag numbers and are not touched, so the separate
  `opendeploy-net` process and the agent can upgrade at different moments.

### During the rollout

The group upgrade walks secondaries first and the primary last. Between a
secondary's upgrade and the upgraded primary's first snapshot reaching it:

- the secondary cannot connect (protocol 13 against 12) and retries with
  backoff; the mismatch is logged, not a decode failure;
- its converted instance cache keeps running workloads running and lets
  it cold-start an instance whose inputs are already cached;
- a secondary that has not been upgraded cannot connect to the upgraded
  primary either, so upgrade the primary promptly after the secondaries.

### Cluster notes

**flippingcopilot** (primary coflip-staging 54.39.100.199, secondary
coflip-prod 54.39.100.89, both on v0.0.615). Dry runs on fresh copies of
both databases on 2026-10-05 and again on 2026-10-06 at the release
commit, zero refusals both times. On 2026-10-06: 5,957 payloads
converted, 22 deletes, 1,466 counters lifted, two keyslots renumbered
(machine slot 513 → 1, recovery slot 2 → 2); a second rebuild reproduces
the snapshot; every reader runs; every secret ciphertext and keyslot
wrapping hashed identical before and after. The secondary converted 8 of
8 cached instances and 3,552 status rows. The e2e harness ran green on
the release commit on 2026-10-06 (2,012 flow steps, 30m38s).

**allevia**: not dry-run yet. Run both conversion tests against copies of
its `primary.db` and the secondary's `secondary.db` before upgrading.

## v0.0.615 (released 2026-10-04)

### What changed

- **Secrets, configs, assets, both directory kinds, spaces, users, network
  policies, the three authz tables, agent and user sessions, Nix store
  resets, keyslots, and the system config are materialised tables rebuilt
  from the write log.** `secret_event_log`, `config_event_log`,
  `asset_event_log`, `value_directory_event_log`,
  `asset_directory_event_log`, `space_event_log`, `user_event_log`,
  `network_policy_event_log`, `authz_rule_template_event_log`,
  `authz_grant_event_log`, `global_access_rule_event_log`,
  `agent_session_event_log`, `user_session_event_log`,
  `nix_store_reset_event_log`, `secret_keyslot_event_log`, and
  `system_config_event_log` are replaced by `secrets` + `secret_versions`,
  `configs` + `config_versions`, `assets` + `asset_versions`,
  `value_directories`, `asset_directories`, the namespace tables
  `value_names` and `asset_keys`, `spaces`, `users`, `network_policies`,
  `authz_rule_templates`, `authz_grants`, `authz_global_rules`,
  `agent_sessions`, `user_sessions`, `nix_store_resets`,
  `secret_keyslots`, `system_config`, and the id counter `entity_ids`. The
  reducer in `pq/materialise.go` maintains them from the committed
  mutations; the write log is the only durable history. A deleted value
  takes its version rows with it (its history stays in the log); the delete
  endpoints already refuse a value a deployment references. A deleted
  space, user, policy, template, grant, rule, or keyslot has no row at all.
  Sessions and resets keep the entity id they had on the stream; new ones
  take theirs from `entity_ids`. The agent session document never carried
  `revoked_at`, so the column is gone: the status says revoked and the
  row's `event_time` says when. The system config's version on the wire is
  now the seq of its last write, not a row id.
- **The write log is required, not backfilled.** The startup backfill that
  v0.0.614 ran is gone; a database whose log stops short of `global_seq`
  refuses to start.
- **`SecretEvent`, `ConfigEvent`, `AssetEvent`, and `NetworkPolicyEvent`
  lose `version`, `event_id`, `event_type`** (tags 2, 4, 6 reserved), and
  `AuthzGrantEvent` is removed from the schema. The event stream and the
  browser tree are unchanged; the one-shot and list responses carry `seq`,
  `author`, the times, `value_version` where there is one, and `value`.
- **New operator command** `opendeploy primary rebuild-tables`: regenerates
  the materialised tables from the write log. Run it with the primary
  stopped.

### Before upgrading

- **The primary must have started on v0.0.614 at least once**, so its write
  log is complete. A primary skipping from v0.0.613 or earlier refuses to
  start with `the write log ends at seq N but the database is at seq M`.
- **Back up `primary.db`.** The table move is one-way.

### What the primary does on first start

- In one transaction: rebuilds the new tables from the write log, checks
  them row by row against the old tables' live state (sealed secret bytes
  included), and drops the sixteen old tables. One log line. Any mismatch
  leaves the database untouched and the primary refuses to start, naming
  the first differing row.
- Writes seq 0 (the two seeded spaces) to the write log on a fresh
  database only.

### After upgrading

- Secrets stay sealed throughout: the rebuild moves ciphertext, never opens
  it, and the machine keys and keyslots are untouched.
- Check `primary.db` size: the eleven dropped tables held a full copy of
  every event, so `VACUUM` reclaims the space when convenient.

## v0.0.614 (released 2026-10-01)

### What changed

- **Secret ciphertexts are bound to the secret id alone, and every row is
  re-sealed.** The AEAD associated data is `opendeploy-secret:s<secret_id>`
  instead of `(secret_id, value_version)`. At the first start where the
  secrets store unlocks (machine key present, or a recovery unlock), every
  `secret_event_log` row is opened under its old binding and re-sealed in
  place. `Secret` field 3 (`seal_id`, never released) is reserved.
- **System secrets live in the secret event log, in space 0.** The cluster
  and workload CA material, the web UI CA and bundle, and the ACME account
  key move from the `system_secrets` table into `secret_event_log` as
  secrets in the root of space 0, authored by the system, during the same
  re-seal pass; the table is dropped once every row has moved. Space 0
  values are unreachable through every API and never appear in the state
  stream, whatever the caller's grants.
- **Space 0 is closed to users.** Secret, config, and asset requests in
  space 0 are refused before any grant is consulted, and a deployment can no
  longer be created in space 0 or moved into it (`spaceId must be between 1
  and 4095`). Cluster-level checks that name space 0 (nodes, users,
  settings, access) and the self and netproxy deployments are unaffected.
- **Asset content lives on disk and in S3, never in the database.** The
  `inline_blob` column of `asset_store` and the `asset_migrations` table are
  gone. Every asset version now carries a `storage_key` on
  `asset_event_log` and on `Asset`, naming its content file in the
  large-asset root and its S3 object. Uploads of every size stage to
  `large-assets/<key>` and, with backup enabled, are copied to S3 before the
  version is committed. Design and status: `docs/engineering/assets.md`.
- **`user_event_log.last_login_at` is dropped** from the table and from the `User`
  proto (field 4 reserved). The users page no longer shows a last-login
  column; the newest personal session for the user carries the same time.
- **Bearer tokens are opaque and every existing token is invalid.** Tokens
  are now `u_<id>.<secret>` (browser and bootstrap sessions) and
  `a_<id>.<secret>` (agent sessions), verified against the session row's
  hash instead of an RSA signature. The `public_keys` table is dropped.
  Every operator is signed out on upgrade and every agent token stops
  working; agents must request a new session.
- **`personal_sessions` becomes `user_session_event_log`** with a `kind` column,
  and the `/v1/personal-sessions/*` endpoints become `/v1/user-sessions/*`.
  The old table is dropped rather than migrated: its rows held hashes of the
  old token format and could never match again. The per-request activity
  touch and `last_active_at` are gone with it; the sessions page loses its
  "Last active" column.
- **Sessions are part of the state stream.** Every create, approval, and
  revocation is a commit that stamps a `global_seq` and publishes the session in
  `CoreUpdate.agent_sessions` or `CoreUpdate.user_sessions` to its owner. The
  `StateStreamMsg.agent_sessions` sidecar (field 8) is reserved, `UserSession`
  gains `user_id` and loses `current`, and `LoginResponse` gains
  `session_id`. Session rows are never deleted.
- **Scopes are gone.** A user session has a `UserSessionKind`, `FULL` or
  `BOOTSTRAP` (master-password exchange, passkey registration only), and an
  agent session always acts as a full, delegated session. Route policies
  name the kinds they accept. `agent_session_event_log.scopes` is dropped; `scopes` is
  reserved on `AgentSession` (5) and `LoginResponse` (3), which gains `kind`.
- **Passkey logins no longer grow the user record.** Each login used to append
  a copy of the credential to the user's blob. The credential is now replaced
  by id, and existing duplicates are collapsed onto the newest copy at startup.
- **Nine tables become append-only event tables.** `spaces`, `users`,
  `value_directories`, `asset_directories`, `system_config_revisions`,
  `nix_store_resets`, `agent_sessions`, `user_sessions`, and
  `secret_keyslots` become `space_event_log`, `user_event_log`,
  `value_directory_event_log`, `asset_directory_event_log`,
  `system_config_event_log`, `nix_store_reset_event_log`,
  `agent_session_event_log`, `user_session_event_log`, and
  `secret_keyslot_event_log`, named like every other event log. Each keeps
  one row per event (`global_seq`, `event_time`, `author`, entity id,
  `event_type`, full document) and is never updated or deleted in place. Live state is the
  newest row per entity that is not a delete, and `primary.db` grows by one
  row per space rename, directory move, settings save, passkey login, or
  session transition. A deleted space's id is no longer reused. Nothing
  changes on the wire.
- **Keyslots are keyed by kind and node.** `secret_keyslot_event_log.slot` (`machine`
  or `recovery`) becomes `kind` (`SecretKeyslotKind`: 1 machine, 2 recovery)
  plus `node_id`, so a future replica can hold its own wrapped copy of the
  master key. The wrapped bytes and their bindings are unchanged. Evicting a
  node deletes its machine slot. Every keyslot write is a commit with a seq
  and the acting user as author.
- **Settings saves are no longer blocked by an asset transition.** The
  reconciler converges placement from the `asset_store` flags; `BackupStatus`
  field 8 (`asset_migration_running`) is reserved and `asset_pending > 0` is
  the "syncing" signal in the cluster page.

### Before upgrading

- **Back up `primary.db`.** The secret re-seal, the `storage_key` backfill,
  and the dropped `inline_blob` column are one-way.
- **Make sure the secrets store will unlock.** The re-seal needs the master
  key. A primary whose `machine.key` is missing starts locked as before, and
  the re-seal then runs at the recovery unlock; until then internal reads
  (cluster TLS, ACME) fail as they would on any locked store.
- **Free disk under `large-assets/` on the primary** for the sum of the
  inline asset blobs currently in `primary.db`:

  ```sql
  SELECT COUNT(*), COALESCE(SUM(LENGTH(inline_blob)), 0)
    FROM asset_store WHERE local_status = 0 AND remote_status = 0;
  ```

### What the primary does on first start

- Adds `storage_key` to `asset_event_log` and backfills it from the
  `asset_store` row with the same sha256; drops `asset_migrations`,
  `personal_sessions`, and `public_keys`, and sweeps the long-dead
  `system_config`, `secret_config_directories`, `config_displays`, `events`,
  `local_runtime_inputs`, and `local_scheduled_instance_cache` tables that
  older installs still carry. Nothing reads any of them.
- After the secrets store unlocks, re-seals every `secret_event_log` row
  under the `secret_id` binding (and the copy of its bytes in the write log,
  which was filled from the table before this pass), appends each
  `system_secrets` row to the event log in space 0, and drops `system_secrets` (and the `seal_id` column
  on databases from unreleased v0.0.614 builds). One log line with the row
  counts. A row that opens under no known binding is logged and left, the
  artifacts stay, and the pass retries at the next unlock.
- Rebuilds `spaces`, `users`, `value_directories`, `asset_directories`,
  `system_config_revisions`, `nix_store_resets`, `agent_sessions`,
  `user_sessions`, and `secret_keyslots` as the `*_event_log` tables: the
  new table is created beside the old one, every row is copied in as a
  create event with `global_seq = 0` (the config revision keeps its id,
  which is its version number; the `user_event_log.last_login_at` and
  `agent_session_event_log.scopes` columns are not carried over; the machine keyslot
  takes the primary's node id), and the old table is dropped. One log line per table. This runs once
  and takes well under a second at the row counts these tables have.
- Rewrites each user whose passkey list holds duplicate credential ids,
  keeping the newest entry per id, and logs one line per user changed.
- Writes each inline blob to `large-assets/<key>`, flags the row as locally
  present, and then drops the `inline_blob` column. A blob whose length does
  not match `size_bytes` is skipped with an error log and left in the column,
  which then stays until the row is fixed by hand.
- With backup enabled, the reconciler uploads every locally present row that
  has no S3 copy, so `asset_pending` is non-zero until the former inline
  content is in the bucket.

### During the rollout

- Every browser lands on the login page and every agent token is refused
  with 401 from the first request after the primary restarts.
- Agent sessions that were approved but not yet collected still hand out a
  token, in the new format, on pickup.
- Secondaries are unaffected: the cluster protocol is unchanged and the
  asset content endpoints serve the same bytes from the new location.

## v0.0.613 (2026-09-25, commit 837917a)

### What changed

- **Value references are `(id, version)` pairs.** Every secret, config, and
  asset reference in deployment specs, cluster settings, ACME bindings,
  worker caches, and the cluster wire moves from an event log row id to
  `ValueRef{id, version}`: the stable entity id plus its value version.
  The reference shape is described in `docs/engineering/secrets.md`.
- **Value event logs drop the space facet.** `space_version` and
  `space_changed` are removed from `secret_event_log`, `config_event_log`,
  and `asset_event_log`, and from the `SecretEvent`, `ConfigEvent`, and
  `AssetEvent` protos.
- **Cluster protocol 11 → 12.** An upgraded secondary refuses an old
  primary, and the other way round.

### Before upgrading

- **Back up `primary.db`.** Every change below is one-way: the dropped columns
  and the rewritten blobs cannot be read by v0.0.612. Rolling back means
  restoring the pre-upgrade database, not reinstalling the old binary.
- **Check for duplicate value versions.** The new partial unique indexes on
  `(entity id, value_version) WHERE value_changed != 0` are built at startup,
  and a duplicate stops the primary. Run this read-only query against the
  primary database; it must return no rows:

  ```sql
  SELECT 'secret', secret_id, value_version, COUNT(*) FROM secret_event_log
   WHERE value_changed != 0 GROUP BY secret_id, value_version HAVING COUNT(*) > 1
  UNION ALL
  SELECT 'config', config_id, value_version, COUNT(*) FROM config_event_log
   WHERE value_changed != 0 GROUP BY config_id, value_version HAVING COUNT(*) > 1
  UNION ALL
  SELECT 'asset', asset_id, value_version, COUNT(*) FROM asset_event_log
   WHERE value_changed != 0 GROUP BY asset_id, value_version HAVING COUNT(*) > 1;
  ```

- **Dangling references in current versions stop startup.** The migration
  also stops the primary if a deployment's latest version, or a version a
  live instance runs, references a value row that does not exist, and for
  any dangling asset mount, ingress certificate, or settings reference.
  Dry-running the migration against a copy of `primary.db` finds these
  before the real upgrade.
- **Free disk on each secondary** for one extra copy of the assets its
  deployments mount (see the asset cache notes below).

### What the primary does on first start

1. Drops the space facet columns from the three value event logs
   (`migrations.sql`).
2. Builds the three unique indexes on `(entity id, value_version)`.
3. Rewrites every `deployment_event_log.value` and
   `system_config_event_log.config_blob` in one transaction
   (`pq/migrate_value_refs.go`), replacing each row id with its pair. Rows
   without references are left byte-for-byte unchanged, and later starts
   rewrite nothing.
4. In a deployment version that is neither the latest nor run by a live
   instance, a dangling env var reference becomes the literal value
   `"<unknown ref>"`, with a `Store` warning in the primary's system log
   naming the row, version, field, and old row id. Redeploying such a
   version passes validation and starts with that placeholder string.

### What each secondary does on first start

- **Runtime input cache is rebuilt.** The encrypted `local_runtime_inputs`
  table is dropped and recreated with a version column. Secrets and configs
  are fetched again from the primary on the next prepare. Until then the node
  cannot cold-start workloads that use secrets or configs without reaching
  the primary.
- **Asset cache files are renamed.** Cached assets are now named
  `<asset_id>@<version>` (plus `_x` for executable mounts). Files from both
  earlier layouts (`<row id>`, and `<old asset id>_<version>` from before
  2026-07-06) are no longer used and the retention sweep deletes them.
  Assets still in use are downloaded again under the new name on the next
  prepare. Running containers are not affected: their bind mounts hold the
  file data until the container is replaced, and only then is that space
  freed.

### During the rollout

The group upgrade walks secondaries first and the primary last. Between a
secondary's upgrade and the upgraded primary's first snapshot reaching it:

- the secondary cannot connect (protocol 12 against 11) and retries with
  backoff;
- its cached instance specs still have the old shape and decode with empty
  references;
- running containers keep running, and the operator retries the input fetch
  in the background;
- a container that crashes in this window cannot restart (its env var and
  asset references do not resolve) and retries with backoff until the
  snapshot arrives.

Upgrade the primary promptly after the secondaries to keep this window
short.

### Cluster notes

**flippingcopilot** (primary coflip-staging 54.39.100.199, secondary
coflip-prod 54.39.100.89). A dry run on copies of both databases taken
2026-09-25 10:00 UTC:

- no duplicate value versions;
- 745 of 1,461 blobs rewritten; all 5,178 resolvable references map to
  exactly their original rows; cluster settings resolve (GitHub token →
  secret 50@2, backup and large-assets S3 keys → secret 52@1); config
  revisions and the snapshot load; a second start rewrites nothing;
- 108 dangling env var references (secret rows 5 and 18, asset rows 5, 6,
  and 19) in 70 historical versions of deployments 3, 4, 5, 6, 23, 24, and
  27, written 2026-06-18 to 2026-07-15. None is in a latest or live version,
  so they become `"<unknown ref>"` and the primary starts;
- coflip-prod drops 21 cached runtime inputs; 6 of its 8 cached instances
  hold old-shape references until the primary's snapshot arrives;
- coflip-prod's asset cache: about 2.9 GB of orphaned pre-July files and about
  2 GB of row-id files are deleted by the sweep; the ones still in use
  (the largest is 1.1 GB) are downloaded again.

**allevia**: not dry-run yet. Run the duplicate query and a dry run of the
migration against a copy of its `primary.db` before upgrading.
