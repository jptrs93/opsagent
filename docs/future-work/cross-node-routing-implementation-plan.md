# Cross-node routing implementation plan

## Purpose

This document is the implementation plan for OpenDeploy cross-node workload
routing. The canonical network design remains in
[`networking.md`](networking.md) and
[`workload-addressing-routing.md`](workload-addressing-routing.md). This plan
records the current code state, the remaining implementation sequence, control
plane and dataplane boundaries, failure behavior, and completion criteria.

The target is the first production-capable implementation for clusters of at
most approximately 100 nodes. It uses one fixed IP-in-IP tunnel per remote node
and direct logical workload `/128` routes. Larger-scale tunnel topology, NAT
traversal, and encrypted transport are outside this plan.

## Fixed decisions

- Every virtual workload has a stable logical RFC 4193 ULA IPv6 `/128`.
- The logical address contains space and deployment identity but not node
  placement. Its 17 reserved bits remain zero.
- A local logical `/128` route selects the workload's host-side veth or TAP.
- A remote logical `/128` route selects the fixed tunnel for the node currently
  hosting the workload.
- Every node has one fixed tunnel interface per remote node.
- An IPv6 underlay uses Linux `ip6tnl` and carries IPv6 inside IPv6.
- An IPv4 underlay uses Linux SIT and carries IPv6 inside IPv4.
- The underlay family is inferred from the configured address. It is not stored
  as a separate enum.
- A cluster initially uses one underlay family. Pairwise family selection is not
  part of the first implementation.
- The initial workload and tunnel MTU is 1420 for either underlay family.
- IP protocol 41 must be directly routable between node underlay addresses.
- Ordinary port-based NAT traversal and relaying are not supported.
- The transport is initially unauthenticated and unencrypted.
- Routes determine location. nftables policy determines authorization.
- The primary computes and distributes state but is never on the packet path.
- Workers persist the last accepted network map and can restore forwarding
  without primary availability.

## Current implementation state

### Workload attachments

The machine-local dataplane exists in `backend/lib/network`:

- One named network namespace and veth pair is created per virtual container.
- The workload side is `eth0`; the host side is `od<deployment-id>s<slot>`.
- The workload receives its logical IPv6 address and a machine-local IPv4
  address used for masqueraded internet egress.
- The host installs local logical `/128` routes to host-side veth interfaces.
- ROLLOVER candidates receive a run-scoped address plus the deprecated stable
  address. Promotion replaces only the stable `/128` host route.
- IPv6 and IPv4 forwarding are enabled by `Manager.EnsureBase`.
- The workload MTU is 1420.

### Route ownership

OpenDeploy sets route-protocol value `200` on local `/128` routes and the cluster
ULA `/48` `unreachable` fallback. Existing untagged local routes are replaced
when workloads are created or recovered. Legacy protocol-`99` routes are removed
only when the destination, table, route type, and proven attachment or exact
fallback all match.

The fallback route ensures an unknown cluster logical address cannot fall
through to the host's default route. More-specific local and future remote
`/128` routes win by longest-prefix matching.

### Underlay addresses

`--underlay-address` is available for primary and secondary installation and is
stored as `OPENDEPLOY_UNDERLAY_ADDRESS`.

- A primary without an explicit value derives an address from its cluster
  listener. A wildcard listener falls back to the first non-loopback global
  interface address.
- A secondary without an explicit value performs a UDP route lookup to the
  primary cluster address and uses the selected local source address. The UDP
  connect does not send a packet.
- A worker includes its underlay address in `EnrollmentHello`.
- The primary persists it on `enrollment_requests.underlay_address`.
- Accepting the enrollment copies it into the existing `nodes.addresses` JSON
  array.
- Primary startup writes the primary's underlay address to its node row.
- The frontend node table displays the address.
- New enrollment addresses are canonical IP literals and must match the
  existing cluster underlay family. Empty addresses remain accepted for legacy
  workers.

Existing nodes enrolled before this field was introduced do not gain an address
automatically; they require reenrollment or a later authenticated node-report
path.

### Network-map distribution

The primary renders and persists a deterministic full `ClusterNetMap` with an
opaque generation and monotonic sequence. It contains every known node, its
underlay address when available, and stable routes derived from running virtual
deployment identity and placement. Target node IDs are added only when serving
an authenticated session or enrollment response.

Workers validate and atomically persist maps before publishing their prefix to
runtime networking. Same-generation stale or conflicting snapshots are
rejected. A new generation retires the previous generation durably so an old
control-plane history cannot later become current again. Reconnects report the
cached accepted generation and sequence. Kernel applied sequence remains zero
until the tunnel reconciler is implemented.

### Missing pieces

The following do not exist yet:

- Post-enrollment underlay-address updates from connected workers.
- Fixed `ip6tnl` or SIT interface reconciliation.
- Remote logical `/128` route reconciliation.
- Complete reporting and distribution of temporary run-scoped routes.
- Tunnel state derived from the persisted worker map.
- Source anti-spoofing and destination ingress policy.
- Cross-node DNS data.
- Cross-node integration tests and operational diagnostics.

## Route protocol ownership

### Why protocol 99 must change

Linux routes contain an 8-bit `rtm_protocol` metadata field. The kernel does
not interpret values at or above `RTPROT_STATIC` for forwarding decisions; the
field identifies the software that installed a route so userspace can inspect
and reconcile its own state.

Protocol `99` is not private. Linux defines it as `RTPROT_OPENR`, and iproute2
maps it to the name `openr`. Consequences of retaining `99` are:

- `ip route` presents OpenDeploy routes as `proto openr`.
- An Open/R installation and OpenDeploy become indistinguishable by protocol.
- A future OpenDeploy garbage collector filtering only on protocol `99` could
  remove legitimate Open/R routes.
- Open/R could replace routes that OpenDeploy assumes it owns.

References:

- Linux `include/uapi/linux/rtnetlink.h` assigns `RTPROT_OPENR = 99`.
- iproute2 `etc/iproute2/rt_protos` maps `99` to `openr`.

### Replacement strategy

Use `200` as OpenDeploy's documented local route-protocol value unless it gains
an upstream assignment before implementation. It is unassigned in the current
Linux and iproute2 protocol lists, but Linux does not reserve a private range,
so the numeric value alone is not a sufficient ownership proof.

Route cleanup must match all applicable ownership properties:

- protocol `200`;
- destination inside the configured cluster ULA `/48`;
- expected route type: unicast `/128` or unreachable `/48`;
- expected table: main;
- for local routes, a verified OpenDeploy host veth or TAP;
- for remote routes, an OpenDeploy-owned tunnel name and alias.

The source constant must explain this choice. OpenDeploy does not need to edit
the host's `/etc/iproute2/rt_protos`; tools may display `proto 200` instead of a
name.

## Target packet paths

### Same-node traffic

```text
source workload eth0
  -> source host veth
  -> source anti-spoof check
  -> destination ingress policy
  -> destination /128 route
  -> destination host veth
  -> destination workload eth0
```

### Cross-node traffic

```text
source workload eth0
  -> source host veth
  -> source anti-spoof check
  -> remote workload /128 route
  -> fixed tunnel for destination node
  -> IPv4 or IPv6 protocol-41 underlay packet
  -> matching fixed tunnel on destination node
  -> unchanged logical IPv6 packet
  -> destination ingress policy
  -> local workload /128 route
  -> destination workload eth0
```

The inner source and destination addresses never change. Transport checksums do
not require adjustment. Same-node and cross-node packets are evaluated against
the same logical policy identities.

## Control-plane model

### Authority

The primary is authoritative for:

- cluster ULA prefix;
- canonical node IDs;
- node underlay addresses;
- deployment identity and desired node placement;
- stable instance address derivation;
- policy intent.

Workers are authoritative only for machine-local runtime facts that the primary
cannot derive, especially currently routed temporary run addresses.

Worker-supplied addresses must always be associated with the node authenticated
by the mTLS session. A payload must never be allowed to select another node ID.

### Network map contract

Add a full-snapshot protobuf carried by `MsgToWorker`. A concrete initial shape
is:

```text
ClusterNetMap {
  generation
  sequence
  target_node_id
  ula_prefix
  nodes: [
    {node_id, underlay_address}
  ]
  routes: [
    {logical_address, hosting_node_id}
  ]
}
```

Properties:

- `generation` distinguishes a restored or deliberately reset primary from an
  earlier control-plane history.
- `sequence` is monotonic within a generation.
- `target_node_id` prevents applying a snapshot intended for another node.
- Address family is inferred from `underlay_address`.
- Routes contain logical addresses and node IDs, never tunnel names or outer
  addresses.
- The worker derives local-versus-remote behavior from `target_node_id`.
- The first implementation distributes a complete cluster map. Filtering is a
  later scale optimization.

The primary must derive stable addresses from deployment identity rather than
trust endpoint addresses received in worker status.

### Worker local-route report

Stable instance placement is available from primary configuration, but a
ROLLOVER candidate's temporary kind-2 run address exists before promotion and
is not represented by current endpoint status. Remote replies to candidate
warmup connections need a route back to that temporary address.

Add an authenticated full local-route report to `MsgToMaster`:

```text
LocalRouteReport {
  revision
  logical_addresses
}
```

Requirements:

- The report is a complete replacement, not a delta.
- The worker sends it when a local attachment is created, recovered, promoted,
  or removed and at the start of every cluster session.
- The primary binds it to the authenticated session node.
- The primary includes reported temporary routes in subsequent network maps.
- Stable addresses in the report are checked against primary-derived
  placement; temporary addresses are checked against the cluster prefix and
  address layout.
- A disconnected worker's last report remains usable while its workloads are
  expected to survive agent restarts.

### Worker application status

Add a small `NetMapStatus` worker message containing accepted generation,
persisted sequence, applied sequence, and the latest reconciliation error. This
is needed for diagnostics and later safe placement movement. Initial routing
does not block all publication on acknowledgements, but cross-node moves must
eventually use them.

## Persistence and distribution

### Primary

The primary network-map publisher subscribes atomically to the
inputs used to render maps:

- node changes;
- deployment configuration and placement changes;
- deployment/runtime route reports;
- policy changes when policy is added.

The renderer produces deterministic ordering and content so unchanged
state does not increment the sequence. Publication state and the latest
rendered snapshot should be persisted so reconnect and primary restart preserve
sequence semantics.

Full maps use a latest-value/coalescing channel rather than queueing many
obsolete snapshots behind a slow worker.

### Worker

The last accepted map and retired generations are persisted atomically in the
existing `local_kv` table. Applying a map follows this order:

1. Decode and validate generation, sequence, target node, prefix, references,
   address family, and bounds.
2. Reject an older sequence or conflicting content at the same sequence.
3. Persist the accepted full snapshot.
4. Publish it to the local network reconciler.
5. Record and report reconciliation success or failure.

Persist-before-reconcile prevents a crash from leaving newer kernel state with
only older durable control-plane state.

### Startup order

Worker startup should become:

1. Load TLS identity and the cached cluster network map.
2. Initialize the networking manager with local node identity.
3. Reconcile cached tunnels, remote routes, and the fallback route.
4. Recover local workload attachments and local routes.
5. Start normal deployment operation.
6. Connect to the primary and apply newer snapshots.

A strict global route cleanup must not run before local workload recovery. The
remote reconciler should initially garbage-collect only routes that point to
OpenDeploy-owned tunnels plus the cluster fallback. Local veth routes remain
owned by workload lifecycle and recovery.

## Tunnel and route reconciler

### Common desired state

Add family-neutral types under `backend/lib/network`:

```text
NodeTunnel {
  node_id
  local_underlay
  remote_underlay
}

RemoteRoute {
  logical_address
  node_id
}

TopologySnapshot {
  generation
  sequence
  local_node_id
  tunnels
  remote_routes
}
```

Common code should validate and diff desired state. Linux-specific files should
perform netlink operations. Non-Linux files retain explicit unsupported stubs.

### Interface identity

Use deterministic names derived from immutable remote node IDs and constrained
to Linux's 15-character interface-name limit, for example
`odt<base36-node-id>`. Set a link alias such as
`opendeploy:tunnel:<node-id>` as an additional ownership marker.

Do not rely on names alone before deleting an interface. Verify the concrete
link type, alias, local endpoint, remote endpoint, and expected node identity.

### IPv6 underlay

Create `netlink.Ip6tnl` with:

- fixed local and remote IPv6 endpoints;
- `Proto = IPPROTO_IPV6`;
- flow-based mode disabled;
- encapsulation-limit behavior configured consistently with the MTU model;
- MTU 1420;
- link state up.

### IPv4 underlay

Create `netlink.Sittun` with:

- fixed local and remote IPv4 endpoints;
- `Proto = IPPROTO_IPV6`;
- path-MTU discovery enabled;
- MTU 1420;
- link state up.

SIT is protocol 41, not UDP. It works over directly routed public or private
IPv4 but not ordinary port-address translation or CGNAT.

### Route merge and precedence

The complete desired route set on a node is:

```text
local workload /128  -> local host veth or TAP
remote workload /128 -> fixed tunnel for hosting node
cluster ULA /48      -> unreachable
```

Local attachment state always wins if a stale map also describes the same
logical `/128` as remote. Reconciliation must not replace a proven local route
with a remote route.

### Reconciliation order

For each accepted topology snapshot:

1. Validate that all participating node addresses use one underlay family.
2. Create missing tunnels.
3. Recreate tunnels whose type or fixed endpoints changed.
4. Bring desired tunnels up.
5. Replace desired remote `/128` routes.
6. Remove stale owned remote routes.
7. Remove stale owned tunnel interfaces after no route references them.
8. Retain or replace the cluster `/48` unreachable fallback.

Every operation must be idempotent. A partial failure leaves the last usable
kernel state where possible and is retried from the complete desired snapshot.

## Placement and rollover behavior

### Stable placements

For the current single-instance model, the primary derives ordinal-zero stable
addresses from deployment space and deployment IDs and maps them to
`DeploymentConfig.NodeID`.

### Same-node rollover

Candidate creation adds the run-scoped local route and reports it. Promotion
replaces the stable local route to the candidate veth. The run route remains
until old candidate cleanup removes it. The stable address does not require a
cluster placement update because its node does not change.

### Cross-node movement

Cross-node movement is not implemented by merely changing `NodeID`. The later
movement sequence must be:

1. Prepare the target attachment and temporary address.
2. Confirm target readiness.
3. Publish a versioned stable placement change.
4. Wait for the required map application acknowledgements.
5. Drain the source workload.
6. Remove the source attachment and temporary routes.

During convergence, a stale sender may reach the old node. If that node has the
new placement route, it may forward the unchanged inner packet one additional
tunnel hop. Placement generations and update ordering must prevent loops.

## Security gate

Cross-node routing must not be enabled as a generally available feature until
the logical policy boundary exists.

### Source anti-spoofing

At each local veth or TAP ingress, allow only addresses assigned to that
attachment. During rollover this set may include the run address, stable
address, and configured deprecated stable address.

Routing cannot provide this check. Without it, a workload can emit packets with
another workload's logical source identity.

### Destination ingress policy

Before delivery to a workload attachment:

```text
deny workload traffic
allow source from destination space /82
allow explicit cross-space deployment/port/protocol rules
allow explicit OpenDeploy system paths
```

The same destination-side rules apply after local routing or tunnel
decapsulation. A separate workload egress policy is not required for the first
same-space isolation boundary.

### Underlay trust

Fixed tunnel endpoints provide locator filtering but no cryptographic
authentication. An attacker able to inject protocol-41 packets using a trusted
node's underlay source can forge inner logical addresses. Deployments requiring
confidentiality or peer authentication must use application-layer security
until authenticated transport is added.

## Implementation sequence

### Milestone 0: correct route ownership

- [x] Replace route protocol `99` with documented local value `200`.
- [x] Centralize constructors for local `/128`, remote `/128`, and fallback routes.
- [x] Add route listing and deletion helpers that require protocol plus structural
  ownership checks.
- [x] Keep local route cleanup scoped to proven workload attachments.

The standalone privileged Linux route test previously considered for this
milestone is intentionally omitted from this plan.

### Milestone 1: network-map API and persistence

- [x] Add `ClusterNetMap`, node, route, local-route-report, and application-status
  protobuf messages.
- [x] Regenerate Go and JavaScript API bindings.
- [x] Add primary publication persistence with generation and sequence.
- [x] Add worker `local_kv` persistence and stale-snapshot rejection.
- [x] Include the initial map in enrollment bootstrap when available.
- [x] Add reconnect tests proving the latest map is resent and older maps are
  rejected.

### Milestone 2: local runtime route reporting

- Expose a complete local routed-address snapshot from `network.Manager`.
- Publish it at session start and after setup, recovery, promotion, and teardown.
- Bind reports to authenticated node identity on the primary.
- Merge temporary addresses into rendered maps.
- Ensure report replacement removes routes no longer present.

### Milestone 3: fixed tunnel reconciliation

- Add common topology desired-state and diff code.
- Implement Linux `ip6tnl` and SIT creation, inspection, update, and deletion.
- Implement remote `/128` route reconciliation.
- Integrate cached-map reconciliation into primary and worker startup.
- Surface address-family mismatch, missing address, protocol-41 reachability,
  and reconciliation errors in node status.

### Milestone 4: logical network policy

- Add nftables source anti-spoofing per local attachment.
- Add same-space destination ingress policy.
- Add required OpenDeploy system paths.
- Ensure same-host and cross-host packets use equivalent policy.
- Add explicit cross-space policy only after the default boundary is correct.

### Milestone 5: DNS and product integration

- Render cluster-wide ready endpoints into node netproxy state.
- Display underlay family, tunnel readiness, applied map sequence, and errors on
  the node page.
- Add operator diagnostics for underlay reachability and MTU failures.
- Keep the primary off the data path during control-plane outages.

### Milestone 6: end-to-end verification

- Add pure unit tests for map rendering, sequencing, route merge precedence,
  tunnel naming, family selection, MTU, and reconciliation diffs.
- Add storage migration and persistence tests.
- Add two-node VM coverage for SIT over the existing IPv4-oriented VM network.
- Add IPv6-underlay VM coverage for `ip6tnl` once the harness provisions routed
  IPv6 endpoints.
- Verify TCP, UDP, and ICMPv6 workload traffic.
- Verify agent restart from cached state and continued traffic during primary
  outage.
- Verify candidate warmup traffic receives remote replies through temporary
  run-address routes.
- Verify stale map and tunnel state is removed without touching unrelated host
  networking.
- Verify same-space access and default cross-space denial.

## Failure behavior

### Primary unavailable

Existing kernel tunnels, routes, and nftables rules continue forwarding. The
worker uses its persisted map after restart. Placement, policy, and DNS changes
pause until the primary returns.

### Agent unavailable

Kernel forwarding continues. No local attachments or topology changes are
reconciled until the agent returns.

### Remote node unavailable

Routes may continue selecting its tunnel and traffic fails according to normal
underlay behavior. Automatic rerouting is not possible because a stable
instance has one current placement. Health-aware DNS should stop returning DOWN
endpoints independently of route cleanup.

### Stale map

A stale source may send to the old hosting node. It either takes one additional
routed tunnel hop if the old node knows the new placement or fails. The cluster
fallback prevents unknown logical destinations from escaping through the host
default route.

### Partial reconciliation

Full-state reconciliation retries. New routes are not installed before their
tunnel exists. Tunnels are not deleted while owned routes reference them.
Invalid new state does not erase the last-known-good persisted map.

### Underlay address change

Changing one node's underlay address causes every peer to recreate or update
that node's fixed tunnel. Logical workload routes and addresses remain
unchanged because they refer to node identity rather than outer addresses.

## Deferred work

- Strict underlay-address validation beyond the parsing required to create a
  tunnel.
- Pairwise IPv4/IPv6 family selection.
- NAT traversal, UDP encapsulation, and relay nodes.
- Encrypted and authenticated underlay transport.
- Flow-based single-tunnel dataplanes.
- Bounded-degree gateway topology for clusters materially above 100 nodes.
- Incremental or sharded network-map distribution.
- Full workload egress policy and exfiltration controls.
- Cross-node scheduling and movement automation.

## Completion criteria

The first cross-node routing release is complete when:

- Every participating node has one valid same-family underlay address.
- Each node reconciles one fixed tunnel per remote participating node.
- Stable and temporary remote logical `/128` routes select the correct tunnel.
- Cached maps restore forwarding without primary availability.
- Local routes always take precedence over stale remote placement state.
- Unknown cluster destinations terminate at the `/48` unreachable fallback.
- Source spoofing is blocked at local attachments.
- Same-space traffic is allowed and cross-space traffic is denied by default on
  both same-node and cross-node paths.
- Node status exposes map and tunnel reconciliation failures.
- SIT and `ip6tnl` paths pass end-to-end VM coverage.
- No reconciliation operation deletes unrelated host routes or interfaces.
