# Per-Node Network Map Implementation Plan

Phase one of the scale direction agreed on 2026-10-05 and 2026-10-06 and
recorded in the open items of [networking.md](networking.md): one cluster
network map per node, scoped by policy-derived reachability, rendered and
stamped per node on the primary. This plan covers the map only. The node
address, readiness-gated inbound addresses, and sender-side peer probes from
"Endpoint selection and health" are later phases and change nothing here.

**Status: implemented (2026-10-06), unreleased.** All eight steps have
landed; the deviations are listed at the end.

## Goal

A change to a placement, node, policy, space, or networking spec re-renders
and sends only the maps of the nodes whose reachable set it touches, and each
node holds routes, WireGuard peers, and DNS catalog entries only for what it
can reach. The map type is unchanged. A node that did not change keeps its
stamp across a primary restart.

## Non-goals for this phase

- Delta distribution. Whole per-node maps are sent.
- A node-level dirty set derived from mutations. Every node is re-rendered on
  every relevant commit; the content hash gates what is sent. The dirty set
  is phase two and needs the indexes this phase builds.
- The hello-time hash that lets a node skip the session-head map.
- The node address, readiness gating, peer probes, and the rollover barrier
  refinements beyond the per-node form of the existing rule.
- Any change to the netproxy wire format or `netstate.pb`.

## Behaviour this replaced

- `netmappublisher.Publisher` subscribes to every write through
  `state.Service.SubscribeUpdates`, so a status write triggers a full fetch
  of nodes, instances, policies, and deployments and a full render.
- `render` produces one map; `mapForNode` copies it per node changing only
  `target_node_id`. Every node receives every route, peer, policy rule, and
  catalog entry.
- `derived_from_seq` is the global seq at render time and is shared by every
  node. The barrier (`DecisionInForce`) waits on every reporting node.
- `render` returns an error for any member node without a WireGuard key or
  with an invalid underlay address. A later refresh then keeps the previous
  map for everyone, and an enrollment during that window omits the map.
- `RenderNetState` treats a nil map or an empty catalog as "no map" and falls
  back to deriving DNS from local placements.
- `ClusterNetworkInfo` carries the ULA prefix separately at enrollment and at
  the session head because the map is optional in both places.

## Design

### Reachability closure

Reachability is a "may initiate" relation between placements, evaluated over
the same inputs the render uses today. Placement `p` may initiate toward
placement `q` when any of:

- `p` and `q` are in the same space and that space is not space 0;
- a network policy allows `p`'s space or deployment toward `q`'s space or
  deployment;
- `q` is in the global space (space 1);
- `p` is the netproxy placement of node `m` and `q` is a placement of a
  deployment whose ingress route `m` publishes through a listen selector.

Only virtual-mode placements that want to run participate. Space 0 has no
same-space edge: the agent self-deployment is host-mode and never enters the
map, each node's netproxy serves only its own node, and the build container is
egress-only.

For node `n`, with `P(n)` its own participating placements:

- **forward set** `F(n)`: every `q` such that some `p` in `P(n)` may initiate
  toward `q`;
- **reverse set** `R(n)`: every `p` such that `p` may initiate toward some
  `q` in `P(n)`;
- **routes**: the routes of every placement in `F(n)`, `R(n)`, and `P(n)`,
  the `/120` of each and the `/100` of each serving one;
- **peers**: every node other than `n` hosting a placement in `F(n)` or
  `R(n)`, with its underlay, key, port, and `ingress_publish`;
- **catalog**: the deployments of `F(n)` and `P(n)`, with their established
  ordinals;
- **policy rules**: the resolved rules whose source or destination space is
  the space of any placement in `F(n)`, `R(n)`, or `P(n)`.

The reverse set is what gives a destination node the routes and peer entries
for replies and keeps cryptokey routing and anti-spoofing consistent on both
ends. The ingress term places the publishing node's netproxy placement in the
backend node's reverse set without a special case.

A node with no participating placements and no published ingress gets a map
with no routes, no peers, and an empty catalog. That is a valid map.

### Render

`render` becomes two steps. The first builds, once per refresh, the shared
inputs: the ingress plan, the resolved policy rules, placements indexed by
space, by node, and by deployment, nodes by id, and the space adjacency from
the policy table. The second renders one node from those indexes by filtering,
with the per-node output sorted as today so canonical bytes are stable. The
ingress plan and its diagnostics stay global.

A member node without a WireGuard key or with an invalid underlay address no
longer fails the render. It is left out of every peer list, its placements are
left out of every route and catalog, and an `IngressDiagnostic`-style entry
names it. Only maps whose closure includes that node change.

### Publisher state

The publisher holds, per node id: the current map, its canonical bytes, its
content hash, and its stamp. On each refresh it renders every member node,
compares the canonical bytes with the held ones, and for each changed node
sets the stamp to the refresh's global seq, stores the new map, and publishes
it to that node's subscriber. Unchanged nodes keep map and stamp and receive
nothing. `lastRenderedSeq` advances as today.

The existing check that content changed without a seq advance applies per
node.

`SnapshotAndSubscribe(nodeID)` and `SnapshotForNode(nodeID)` return the
node's own map. `mapForNode` and the shared `current` map go away.

### Persisted stamps

A primary table `node_netmap` with `node_id`, `derived_seq`, and
`content_hash`, written by the publisher outside the write log after each
per-node change, in the same position as `asset_store`: a table that is not a
view of the log and is excluded from the fold oracle and `RebuildFromLog`.
On start the publisher loads it, renders every node, and for each node whose
fresh hash equals the stored hash adopts the stored stamp; otherwise it stamps
with the current seq. A rebuild or a restore from backup may leave stamps that
no longer match any history, which is harmless because a node accepts the
first map of a session unconditionally and the stamp only needs to be
monotone per node.

Rows for nodes that are no longer members are deleted on refresh.

### Barrier

`DecisionInForce(seq)` becomes: a render has seen `seq`, and every node whose
stamp is at or above `seq` has reported an applied stamp at or above its own
stamp. Nodes whose maps did not change at or after `seq` do not enter the
condition. `RecordApplied` and `ForgetNode` are unchanged. This is the
reachability-scoped membership from the networking open items, obtained
without separate bookkeeping.

### Subscription filter

The publisher subscribes through `state.Subscribe` with a projection that
returns true only when a mutation's entity type is one of scheduled instance,
node, network policy, deployment, space, or system config. Status mutations
never wake the refresh loop. The projection runs inside `notifyLocked` under
the write mutex and does nothing beyond type inspection.

### Map always present

- Enrollment refreshes and includes the node's map; an enrollment whose render
  cannot produce one fails with an error instead of omitting it.
- The session head always sends the node's map before the instance snapshot.
- `ClusterNetworkInfo` is removed from `MsgToSecondary` and from
  `EnrollmentAccepted`; the secondary reads the ULA prefix from the map. The
  secondary's startup path already prefers the cached map's prefix and only
  falls back to the cached `ClusterNetworkInfo`; the fallback goes with the
  message. Field tags are reserved.
- The netstate writer waits for the first map before its first write, the way
  the operator's boot sync gate waits for the first assignment snapshot.
  `RenderNetState` takes a non-nil map, and the local-placement DNS fallback
  and the empty-catalog check are removed. The catalog is authoritative and
  may be empty.

### Secondary

No change to acceptance, persistence, reconciliation, or `NetMapStatus`. The
secondary's validation that every route names a peer present in `nodes` still
holds because routes and peers are pruned by the same closure. An older
secondary receiving a pruned map sees a smaller valid map.

### Data model

`ClusterNetMap` in `datamodel/materialised_node/node_projection.dm` is unchanged. Its
documentation comment is updated to say content is per node.

## Steps

1. **Subscription filter.** `netmappublisher.New` subscribes through
   `state.Subscribe` with the entity-type projection. Independent of the rest
   and shippable alone.
2. **Render split.** Extract the shared index build from `render` in
   `backend/app/primary/netmappublisher/publisher.go`, add the closure
   computation, and add a per-node render over the indexes. Keep the global
   render for the oracle test. Replace the render error for a bad node with
   exclusion and a diagnostic.
3. **Property test.** Random clusters of nodes, spaces, placements in every
   target state, policies with space and deployment peers, and ingress listen
   selectors. Assert: every per-node route and peer exists in the global
   render; for every pair allowed by the relation, the source node has the
   destination's routes and peer and the destination node has the source's;
   no node carries a route, peer, or catalog entry outside its closure; the
   union over nodes of routes equals the global routes.
4. **Publisher per-node state.** Replace `current` with the per-node table,
   per-node compare and stamp, per-node publish, and the per-node forms of
   `SnapshotAndSubscribe`, `SnapshotForNode`, and `canonicalContent`.
5. **Persisted stamps.** `node_netmap` schema under
   `backend/storage/primarydb/pq/sql/`, queries in `pq`, exclusion from the
   fold oracle, load on `New`, write on change, delete on membership loss.
6. **Barrier.** Per-node `DecisionInForce` in `barrier.go` and its tests.
7. **Map always present.** Enrollment and session head in
   `backend/app/primary/enrollmenthandler/handler.go` and
   `backend/app/primary/clusterhandler/session.go`; remove
   `ClusterNetworkInfo` from the two protos with reserved tags, from
   `backend/app/secondary/cluster_session.go`, `enrollment.go`, and
   `startup.go`, and the `cluster_network` local KV; gate the netstate writer
   in `backend/app/netproxy/netstate.go` and remove its two fallbacks.
8. **Docs.** Update `docs/engineering/networking.md` (map rendering and
   distribution, the barrier, netproxy services) to describe the shipped
   behaviour, move the per-node item in `networking.md` from open to shipped,
   and record deviations here.

Steps 2 through 6 land together. Step 7 can follow in a second commit.

## Verification

- Unit: the property test from step 3; barrier tests for the per-node rule,
  including a node whose map did not change at the decision seq not holding
  it; publisher tests for unchanged nodes keeping stamps across a refresh and
  across a restart with a matching hash; a bad node excluded with a
  diagnostic instead of an error.
- Harness: the existing cross-node, rollover, and network policy e2e cases in
  `testing-vms/e2e/cases` (`network-policy-cross-node.js`,
  `rollover-networking.js`, `rollover-ingress.js`,
  `network-policy-interactions.js`) must pass unchanged; they exercise same
  space, cross space by policy, ingress across nodes, and the barrier.
- A new e2e case: two spaces on disjoint nodes with no policy between them;
  assert each node's accepted map carries no route or peer for the other
  space, that a placement change in one space does not change the other
  nodes' `persisted_seq`, and that adding a policy between the spaces adds
  the routes and peers on both sides.
- `netaudit` stays clean on every node after each case.

## Rollout

No protocol version bump for the pruned content. Removing `ClusterNetworkInfo`
is a breaking change for a secondary older than the primary only at
enrollment and at startup without a cached map, both of which already require
the primary to be reachable; the release notes name it. Secondaries upgrade
after the primary as today.

## Status and deviations

Shipped on 2026-10-06 in `backend/app/primary/netmappublisher` (`render.go`
holds the shared index build and the per-node render, `scope.go` the closure,
`publisher.go` the per-node state and the entity-type subscription,
`barrier.go` the per-node rule; on 2026-10-07 the package moved into
`backend/app/primary/nodepublisher`, where the maps are one part of the node
projection and phase two's dirty set is implemented, see
`node-publisher-implementation-plan.md`), `backend/storage/primarydb/pq/node_netmap.go`
with its schema file, `ingressplan.Result.PublishedDeployments` for the
ingress term of the closure, and the step 7 changes to the protos, the
cluster session, enrollment on both sides, the secondary startup, and the
netstate writer. Deviations from the plan above:

- A node excluded from the render (no WireGuard key, or an underlay address
  family different from the cluster's) had no map rather than an empty one,
  and `SnapshotForNode` returned nil for it. Superseded on 2026-10-07: both
  conditions are refused at write time (enrollment, the session hello, and
  the primary's own boot, which loads its key before writing its row and
  checks its underlay family against the enrolled nodes), the render copies
  the rows without checking them, and the snapshot map is a required field.
- Two serving placements of one ordinal were a render error that kept every
  node's previous map. Superseded on 2026-10-07 with the same reasoning: the
  scheduler's atomic flip guarantees one serving placement per ordinal, and
  the render no longer counts them.
- The new e2e case was not written. The Playwright harness has no surface
  that exposes a node's accepted map or its `persisted_seq`, so the case
  cannot observe what it would assert. The closure is covered by
  `TestScopeClosureProperty` against a brute-force form of the relation plus
  three fixed-topology tests (partitioned spaces, global space and policies,
  ingress publisher and backends), and the existing cross-node, rollover,
  and policy e2e cases are unchanged and still due to run before release.
- `RenderNetState` tolerates a nil map as an empty catalog instead of
  requiring one. The writer never passes nil: with a map source it waits for
  the first map before its first write, and without one (the primary-less
  test path) it renders from an empty map.
- The policy rules a node carries are those touching a space in its forward
  or reverse set; a node hosting a global-space placement carries every
  placement's routes but not every rule.
- `canonicalContent` keeps `target_node_id` in the compared bytes, since the
  comparison is per node now and the field never changes for one node.

## Open items

- Space-level policy adjacency is the first version. Deployment-scoped peers
  could narrow the closure further; measure before adding.
- Whether `ingress_publish` for nodes outside a node's closure should be
  omitted. It is carried on the peer entry today, so pruning peers prunes it.
- Phase two, the node-level dirty set from mutations through the indexes,
  shipped on 2026-10-07 in `nodepublisher` (`dirty.go`).
- Phase three: the hello-time hash and skipping the session-head map.
