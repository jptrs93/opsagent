# Node Publisher Implementation Plan

One projection per node, published from one place. A connected secondary
receives its complete `NodeProjection` at the head of its session and then
one `NodeProjection` per write event that touches it, carrying the changed
scheduled instances and the changed network map together. The `NodePublisher` owns
that projection for every node, maintains it from the write stream inside
the commit, and is the only path from `state.Service` to a cluster session.

Agreed 2026-10-07. Builds on the per-node network map shipped on 2026-10-06
(`per-node-netmap-implementation-plan.md`). The direction and the staging
were decided together: the publisher works inside the write lock first, the
phase-two dirty set lands before any move outside the lock, and a later move
outside the lock may bring a read layer that serves every read from a
consistent view at or below a seq.

**Status: implemented (2026-10-07, unreleased).** See Status and deviations at the end.

## Goal

- A write event becomes at most one message per node, and that message is
  the whole effect of the write on the node: instance rows and network map
  in one frame, stamped with the write's seq.
- The primary does one unit of work per commit for the node stream, not one
  per connected session. Per-commit cost is proportional to the nodes the
  write reaches, not to the cluster size.
- Enrollment and the session head deliver the same complete projection, and a
  secondary applies a snapshot or an update as one transaction.
- Consistency between ingress claims, port forwards and the Web UI listeners
  is guaranteed by write-time validation alone. The publisher evaluates
  expansion, never collisions.

## Non-goals

- Processing outside the write lock, and the read layer with seq-bounded
  consistent views that would come with it. The interface is shaped for that
  move; the move itself is a later plan.
- Delta encoding of the network map inside an update. The map is sent
  whole when it changes.
- Sending each `(deployment id, version)` record once per snapshot instead of
  inside every instance. Noted as a later normalisation.
- Changes to `netstate.pb` or the netproxy wire format.
- Nix store resets. They stay a separate frame.

## Behaviour this replaces

- Every session holds its own store subscription through
  `MustFetchScheduledSnapshotAndSubscribe`. Each commit runs every session's
  projection inside `notifyLocked`, under the write mutex, re-reading the
  touched instance rows from the tables and filtering by node. A thousand
  sessions is a thousand row reads per commit, all but one discarded.
- Status writes are the most frequent commit, and each one is projected back
  to its own node as a full `ScheduledInstanceState`, because the status is
  embedded in that type. The node wrote the status and does not need it.
- The map arrives through a separate publisher goroutine as a separate frame,
  not ordered with the instance rows of the same commit.
- The session head is a sequence of frames: protocol version, map, ACME
  state, Nix resets, instance snapshot. `EnrollmentAccepted` carries two
  instance rows and a map as three fields.
- ACME state is a global broadcast to every node.
- Ingress collisions are decided on the expansion of listen selectors against
  each node's reported host address inventory. Node reports change that
  expansion without a validated write, so the publisher resolves collisions
  after the fact and needs the Web UI reservations to do it.
- The Web UI binds its listeners once at boot. A listen setting change is
  validated and stored but takes effect at the next restart, and deployment
  validation reads a settings snapshot captured when the handler was built.

## Prerequisites

Three changes to the ingress and settings path land first. They are what
make the publisher's job expansion only.

### Set semantics for ingress claims

`lib/ingressplan` decides collisions on the listen selectors as address
sets, not on their expansion. On one node and one port, two claims collide
when their sets intersect: an empty selector is the whole address space, a
`/0` prefix is one family, a CIDR is its range, a literal is one address.
Two prefixes intersect exactly when one contains the other. A TCP port
forward is the whole space on its node, as today. A reservation is a set too,
the whole space for a wildcard bind or one address for a specific one, and
any intersection between a deployment claim and a reservation is a save-time
error, literal or not.

The host address inventory is then used for expansion only: which concrete
addresses a claim publishes on. An unknown inventory still expands a
wildcard to the wildcard bind. Nothing a node reports can create or remove
a conflict.

Consequences:

- Validation is a pure function of the stored deployments and the stored
  settings. The lower-id rule that resolved a collision between two stored
  deployments is no longer reachable from a valid write.
- The silent exclusion of a reserved port goes away. Today a wildcard
  selector on the primary coexists with the Web UI by having the reserved
  port excluded, which with a wildcard Web UI bind publishes the route
  nowhere on the primary and reports it as a warning. Under set semantics the
  save is rejected naming the Web UI. An HTTPS ingress cannot be placed on
  the primary while the Web UI holds `:443` on every address; the operator
  binds the Web UI to one address or another port first.
- `10.0.0.0/8` next to an empty selector on the same port is rejected even on
  a node with no `10.x` address, because the empty selector means every
  address.
- Stored deployments that were valid under expansion semantics may conflict
  under set semantics after an upgrade. The evaluator keeps the deterministic
  lower-id fallback for stored-versus-stored conflicts so the publish set
  stays total, reports each as a diagnostic, and the primary logs them once
  at boot. Validation forbids creating new ones.

The e2e case `ingress-listen-primary-reservation` changes: the default
listen on the primary is now rejected naming the Web UI, the same as the
literal, and the deployment's warning list stays empty throughout. The
`ingress-listen-implementation-plan.md` collision rules and the Listen
selectors section of `docs/engineering/networking.md` are updated.

### The Web UI follows its listen settings

A settings save that changes `http_web` or `https_web` listen or enabled
binds the new listener as part of validation, so a port that cannot be
bound fails the save, then swaps it in and shuts the old server down with
the existing shutdown timeout. TLS material is already reissued on save when
the names change. After this the stored settings are the single truth for
what the primary holds, and the reservation derived from them matches the
kernel.

### Deployment validation reads the current settings

`webuihandler.Handler.Config` is a pointer to the settings captured at
construction. It becomes a function returning the current snapshot, as
`runtime.go` already does for the asset store, and its uses, the
reservations for deployment validation and the passkey origins among them,
go through it.

## Design

### Wire types

Defined in `datamodel/materialised_node/node_projection.dm` and mirrored in
`api-contract/model_cluster_operations.proto`, the way `ClusterNetMap` is.

`NodeProjection` is the one wire type. `MsgToSecondary` carries it as
`node_snapshot` at the session head and as `node_update` afterwards;
`EnrollmentAccepted` carries it as `node_snapshot`. The fields are the same,
the semantics differ by position:

- `seq`: the write seq the message reflects, non-decreasing per session.
- `instances`: as a snapshot, every scheduled instance assigned to the node
  that is not finalised; as an update, the full rows of the instances this
  write changed on the node, a finalised instance included once with its
  final state. Each row is a `NodeInstance`: the instance, the deployment
  record it pins, and `status_watermark`, the `updated_at` of the newest
  status the primary holds for it, absent when it holds none. The secondary
  replays its local history above the watermark after a snapshot.
- `net_map`: always present on a snapshot; on an update, present when the
  node's map changed in this write.
- `acme`: always present on a snapshot; on an update, present when the
  node's ACME subset changed.

`MsgToSecondary` carries `node_snapshot` and `node_update`; the fields
`scheduled_instances_snapshot`, `scheduled_instance_update`,
`cluster_net_map` and `acme_state` are removed and their tags reserved.
`EnrollmentAccepted` carries `node_snapshot` in place of `node_deployment`,
`node_net_deployment` and `cluster_net_map`, tags reserved. The cluster
protocol version is bumped.

### The fold cache

The publisher keeps an in-memory materialisation of everything the node
projection reads, seeded once under the write lock at construction and then
advanced by applying each `CoreWriteUpdate`'s mutations in order. The
mutations carry full entity payloads, so no table is read after seeding.

- Nodes by id, with the member flag computed by the same rule as
  `pq.MemberNodeStatuses`. A node leaving the member set drops out of every
  map; eviction finalizes its placements in the same commit.
- Scheduled instances by id until finalised, with the hosting node, space,
  ordinal, target state and pinned `(deployment id, version)`.
- Deployment records by `(id, version)` while any live instance pins them,
  plus the current record per deployment id for the ingress plan and the
  policy space resolution. A deployment mutation adds the new version; a
  version is dropped when its last pinning instance is finalised.
- Status watermarks per instance from status mutations. These update the
  cache and produce no update.
- Network policies, spaces, the cluster prefix.
- The ingress plan result, recomputed when a deployment or a node changes:
  `publish` per node and `PublishedDeployments` per node.
- Per node: the current map with its canonical bytes, hash and stamp; the
  current ACME subset and its hash.

`prepareRender` becomes the derivation over this cache instead of over
`FetchNetworkMapInputs`. Seeding reuses the same reads
`FetchNetworkMapInputs` and `ListLiveScheduledInstanceStates` do today.

### Dirty set

Each applied update yields two node sets.

Instance rows: the hosting node of each touched instance.

Map:

- A placement change dirties its node, every node with the placement's space
  in its forward or reverse set, every node hosting a global-space
  placement, and every publisher of the placement's deployment.
- A policy change dirties every node with a placement in either space.
- A node row change dirties that node and every node whose current map lists
  it as a peer.
- A deployment change dirties its placements' nodes and every node whose
  forward set contains the deployment's space; a change touching listen
  selectors or port forwards recomputes the ingress plan and dirties every
  node whose `published` set changed.
- A space change, a system config change, or anything not attributable above
  dirties every node.

The dirty set is an over-approximation gated by the byte compare, so a wrong
rule costs render time and never correctness. An oracle test renders every
node after each random commit and asserts the dirty-set render equals it.

### Derivation and publish

For each dirty node the publisher renders the map if the node is map-dirty,
compares canonical bytes, stamps a changed map with the write's seq, builds
the update from the touched rows on that node and the changed map,
and enqueues it on the node's session queues if it is non-empty. The
`node_netmap` stamps table and its adoption on restart move over unchanged.

ACME input is not a write event. The ACME manager hands the publisher a new
global state; the publisher scopes it per node, compares hashes, and
enqueues an update carrying only `acme` with the current applied seq.
Per-node scope: the cert bindings and challenges whose hostname belongs to
an HTTPS route of a deployment the node publishes or hosts. The manager
records the hostname beside each challenge token to make that possible.

### Sessions and queues

`Subscribe(nodeID)` takes the publisher mutex between two applied updates,
builds the node's complete projection from the cache at the current applied seq,
registers a bounded queue, and returns the snapshot, the queue and an
unsubscribe. The session sends the snapshot, then drains the queue. The
in-process applier and the primary's netstate writer subscribe the same
way, as the primary node, through adapters that fold snapshot plus updates
into the interfaces they consume today.

Enqueue is a non-blocking send. On a full queue the publisher closes the
queue and drops the subscription; the session ends its stream and the
secondary reconnects, receiving a fresh snapshot. There is no merge and no
replay.

The barrier moves over unchanged: `RecordApplied`, `ForgetNode`,
`DecisionInForce` with the per-node rule, and `AckUpdates`. The rendered
check reads the publisher's applied seq, which equals the last committed
seq while the publisher runs inside the lock.

Ingress diagnostics stay a separate subscription for browsers.

### Inside the lock

The store subscription's projection is the publisher's handler. It applies,
derives and enqueues inside `notifyLocked`, after the SQLite transaction has
committed and before the write mutex is released. Three rules follow:

- It never reads the store. The mutex is not reentrant, and the fold cache
  exists so that nothing needs it.
- It never blocks. Queue sends are non-blocking by construction.
- It carries no recovery. The design had a recovered panic poison the cache
  and reseed it on the next subscribe; that was removed on 2026-10-07 with
  the other defensive paths (see Status and deviations). A panic in the fold
  is a bug and takes the primary down with its stack trace.

The code is arranged for the later move outside the lock: the handler calls
`apply(update) -> outputs` and then `enqueue(outputs)`, the publisher exposes
`AppliedSeq()` and `WaitForSeq(seq)`, and no derivation code assumes it runs
inside `Commit`. Moving out is then the handler body becoming a send into a
serial queue drained by one goroutine.

`Refresh` disappears. Enrollment and the scheduler read the publisher after
their own commit and see it by construction.

### Secondary

- A snapshot is applied in one transaction: assignments written,
  assignments absent from the snapshot finalised, the map accepted and
  cached, the ACME subset cached. `notifySynced` fires after it. The status
  history replay above each watermark follows as today.
- An update is applied in one transaction: the rows, the map through the
  existing acceptance rule, the ACME subset. `NetMapStatus` is reported
  after a map change as today.
- `statusPushLoop` is unchanged; the secondary no longer receives its own
  statuses back.
- Enrollment bootstrap applies the snapshot the same way; startup finds the
  netproxy deployment and node id in the cached assignments as today and the
  prefix in the cached map.
- The ACME holder is fed from the projection instead of the broadcast frame.

### Removals

`app/primary/netmappublisher` is replaced by `app/primary/nodepublisher`
with its render, scope, stamps and barrier code moved in. The per-session
`MustFetchScheduledSnapshotAndSubscribe` on the primary goes, along with
the session's map, ACME and reset subscriptions except the reset one. The
primary's ACME holder stays as the manager's output and becomes the
publisher's input. `netproxy.ClusterNetMapSource` is served by the
publisher adapter on the primary and by the holder on the secondary as now.

## Steps

1. **Ingress set semantics.** Evaluator, validation messages, the e2e case,
   the two docs. Independent and shippable alone.
2. **Web UI rebind and current settings.** Bind-on-validate and swap in
   `webui/server.go`; `Handler.Config` as a function.
3. **Wire types.** `NodeProjection` and `NodeInstance` in the DSL and the
   proto, tag reservations, protocol bump, regen.
4. **Fold cache.** Seed, per-mutation apply, membership rule, deployment
   version retention, watermarks, ingress plan recompute; the cache oracle
   against the tables over random commits.
5. **Publisher.** Dirty set, derivation, per-node map state and stamps moved
   in, ACME scoping with the hostname recorded per challenge, queues and
   overflow, subscribe with snapshot, barrier moved in, applied seq and
   wait; the dirty-set oracle against a full render.
6. **Primary rewiring.** Session head and loop, enrollment, the in-process
   applier and netstate writer adapters, scheduler and enrollment reading
   the publisher instead of calling `Refresh`; delete `netmappublisher`.
7. **Secondary.** Atomic snapshot and update apply, watermark replay, ACME
   from the projection, enrollment bootstrap and startup.
8. **Docs.** `docs/engineering/networking.md` map distribution and barrier,
   `docs/engineering/api.md` cluster stream, this plan's status, CLAUDE.md.

Steps 3 through 7 land together. The work stays on `main` and is squashed to
one commit at the end.

## Verification

- Unit: the set-semantics evaluator (containment, families, literals,
  reservations, port forwards); the cache oracle; the
  dirty-set oracle; one update per commit containing both rows and map;
  status writes producing no update; queue overflow dropping the session;
  snapshot at seq S followed by updates from S+1 across a concurrent
  commit; stamps adopted across restart; the barrier tests.
- Secondary: a snapshot and an update each applied atomically; a failure in
  the map half leaves the rows unwritten.
- Harness: the existing cross-node, rollover, ingress, listen and policy
  cases, with `ingress-listen-primary-reservation` rewritten, and
  `netaudit` clean after each.

## Rollout

Protocol version bump; secondaries upgrade after the primary as today, and an
older secondary is refused at the version handshake. After the upgrade the
primary logs any stored ingress claims that conflict under set semantics.
A Web UI listen change no longer needs a restart.

## Open items

- Sending deployment records once per snapshot.
- Deployment-scoped policy peers narrowing the closure and the dirty set.
- The move outside the lock, with the serial queue and the seq-bounded read
  layer.
- Whether Nix store resets join the projection.

## Status and deviations

Implemented on 2026-10-07, after the per-node map of 2026-10-06, as one
change on `main`. What landed per step:

1. Set semantics for ingress claims in `lib/ingressplan` (collisions on
   address sets, `Result.Excluded` for stored deployments overlapping a
   reservation), the validation messages, the rewritten e2e case, and the
   two docs.
2. `webui.Manager` with prepare, release, and swap on a listen settings
   save; `webuihandler.Handler.Config` as a function reading the current
   settings.
3. `NodeProjection`, `NodeInstance` with its `status_watermark`, and the
   hostname on `AcmeHttpChallenge` in the DSL and the proto; the old
   snapshot, update, map, and ACME messages removed with their tags
   reserved; protocol version 15; the documentation types under
   `datamodel/materialised_node` and `datamodel/materialised_node_local`.
4. The fold cache in `nodepublisher/cache.go`: seed from the tables under
   the write lock, per-mutation apply in a fixed type order (deployment,
   node, policy, instance, status), member nodes only, deployment versions
   keyed by `(id, version)` with a pin count from live instances and the
   current version held while active, watermarks as the maximum status
   time per live instance. The cache oracle compares the folded cache with
   one seeded from the tables after every commit of a random sequence.
5. The publisher in `nodepublisher/publisher.go`, `dirty.go`, and
   `acme.go`: the dirty set over the previous render context, the ingress
   plan recomputed only when a plan input changed, per-node maps and
   stamps moved in from `netmappublisher`, per-node ACME subsets, bounded
   queues (256) closed on overflow, subscribe with snapshot, the barrier
   moved in, `AppliedSeq` and `WaitForSeq`. The dirty-set oracle compares
   every held map with a full render after every commit, under four seeds.
6. The cluster session, enrollment, the in-process applier, the primary's
   netstate writer, and the scheduler read the publisher. `Refresh` and
   `nodes.FetchNetworkMapInputs` are gone; `netmappublisher` is deleted.
7. The secondary applies a snapshot or an update in one transaction
   (`MustApplyAssignments`), replays its status history above the
   watermarks, takes ACME from the projection, and bootstraps enrollment
   from the snapshot.

Deviations from the design above:

- Spaces and system config are no longer map inputs. The cluster prefix is
  fixed at construction, the Web UI reservation left the plan with the set
  semantics, and the render never reads the space table, so a write to
  either advances the applied sequence and renders nothing.
- The shared index build (`prepareRender`) still runs over the whole cache
  after every commit that touched an input. Only the per-node render is
  limited to the dirty set. An incremental index build is open.
- The render has no data-dependent failure. The checks the per-node map
  work carried (a member without a WireGuard key, a member whose underlay
  address family differs from the cluster's, a placement on a non-member,
  two serving placements of one ordinal) are removed with their diagnostics
  and error paths, because each is guaranteed by a writer: enrollment and
  the session hello reject the first two, the primary's boot loads its key
  before writing its row and checks its family against the enrolled nodes
  (refusing to start on a conflict; the installer generates the key with the
  row), eviction finalizes a node's placements in the commit that removes
  it, and the scheduler flips an ordinal atomically. The fold carries no
  runtime assertions either: the two it had (a pinned version missing from
  the cache, a map changing without a sequence advance) were removed on the
  same reasoning, and the cache and dirty-set oracles in the tests are where
  the fold is checked. The recover, poison, and reseed machinery went with
  them, together with the error returns of the cache apply functions: a
  mutation without its payload or a pin on a version the cache does not hold
  is a fold bug, and the publisher no longer survives one.
- One wire type, `NodeProjection`, serves as `node_snapshot` and
  `node_update`; the separate `NodeSnapshot`, `NodeUpdate`, and
  `StatusWatermark` messages are gone and the watermark sits on
  `NodeInstance.status_watermark`. `net_map` and `acme` are optional on the
  type and a snapshot always carries both. An update row carries the
  watermark too, since it is the same fact; the secondary reads it only
  after a snapshot. The test shadow that folds updates compares rows without
  it, because a status write produces no update.
- A snapshot always carries a map. `Subscribe` and
  `SnapshotForNode` return an error only for a node that is not a member;
  a member always has a map, since the publisher renders every member on the
  commit that admits it. The session refuses to open on the error and the
  secondary reconnects on its backoff, enrollment fails with it, and the
  primary's own map adapter panics on it, because the primary is a member
  from the first commit.
- The dirty set for a policy change is the nodes reaching either space
  (hosts of the space, hosts of the spaces adjacent to it, hosts of the
  global space), not the hosts alone; the oracle found the narrower rule
  wrong, because a rule is carried by every node whose scope touches one of
  its spaces.
- The primary's netstate writer keeps its store subscription for instance
  rows and takes only the map from the publisher, through
  `SnapshotAndSubscribeMap`; ACME for the primary stays the global holder.
  The in-process applier uses the same map adapter.
- An ACME subset carries a wall-clock sequence per node, so the secondary's
  holder accepts a subset that changed because of hosting alone, with no
  change to the global state.
- The secondary's local key-value write coerces a nil encoding to an empty
  blob, since `local_kv.value` is not nullable, so an empty ACME subset is
  written like any other value.
- A sweep on 2026-10-07 removed every check in the touched code that only
  guarded against a violated write-time invariant: nil and zero-id guards
  and clone-and-sort passes in the render and dirty set, the `DELETE` arm
  and absent-clock handling in the fold, the sequence gate on the ACME
  holder, the nil-projection guards in the enrollment and session handlers,
  the secondary's filter of instance rows by node and its skip of
  `FINALIZED` rows on load, the "newer local status" compare on apply, the
  read-side hostname, path prefix, port, and secret-reference checks in the
  netstate writer, the evaluator's stored-versus-stored collision fallback
  and host-port filter, and the dead `acceptClusterNetMap` and
  `MustWriteScheduledInstanceAssignment`. Where a reader normalised a value
  the writer now stores the canonical form: deployment validation writes the
  canonical hostname back into the spec, and a listen setting with a 4in6
  host is rejected so the evaluator needs no 4in6 branch. Kept, each for a
  stated reason: `validateClusterNetMap` on the secondary, the hello
  re-validation of key and family, the evaluator's host-mode warning, the
  empty-listen error in the Web UI manager (the installer does not validate
  an empty `--web-listen`), and the TLS passthrough port defaulting to 443
  in the netstate writer (the model's meaning of an absent host port, not a
  check).
- A snapshot whose map fails validation is rejected whole; the session ends
  and reconnects rather than applying the rows without the map.
- The session no longer echoes the primary's status rows. Each snapshot
  instance carries its watermark and the secondary replays the rows above
  it.

Open items, in addition to the list above:

- The incremental index build, so a commit costs the dirty nodes alone.
- An e2e observation point for a node's accepted projection, as noted in
  the per-node plan.
