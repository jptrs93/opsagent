# Workload addressing and cross-node routing

## Decision

OpenDeploy gives every virtual run a stable inbound logical IPv6 address `I`
and a run-scoped preferred outbound logical IPv6 address `O`, and routes both
directly. Cross-node traffic uses fixed IP-in-IP tunnel interfaces:

```text
logical workload /128 -> local veth/TAP
logical workload /128 -> fixed tunnel interface for the hosting node
```

The inner packet always carries the logical workload source and destination.
The tunnel interface adds an outer IPv4 or IPv6 header containing the source
and destination nodes' reachable underlay addresses. There is no logical/locator
address translation, transport-checksum adjustment, per-flow NAT state, or
userspace packet forwarding.

The initial design is a direct full mesh of fixed tunnel interfaces. It targets
clusters of at most approximately 100 nodes. Larger-scale routing is deferred;
the workload-to-node placement model remains usable if fixed tunnels are later
replaced by a flow-based tunnel or bounded-degree gateway topology.

## Logical address layout

The implemented RFC 4193 ULA `/48` address ABI is:

```text
<ULA:48><space:16><deployment:24><ordinal:12><versionSlot:20><runSlot:8>
```

| Field | Bits | Values | Meaning |
|---|---:|---:|---|
| Space | 16 | 65,536 | durable tenant/security domain |
| Deployment | 24 | 16,777,216 | deployment field value; `0` is invalid |
| Ordinal | 12 | 4,096 | stable instance ordinal |
| Version slot | 20 | 1,048,575 nonzero | normalized deployment config version |
| Run slot | 8 | 255 nonzero | normalized run number |

For an instance `(space, deployment, ordinal)`, the stable inbound address is
`I = Address(prefix, space, deployment, ordinal, 0, 0)`. Every virtual run also
has `O = Address(prefix, space, deployment, ordinal, versionSlot, runSlot)`,
with both slots nonzero. Positive full-width config versions and run numbers
are normalized only for address derivation as `((n - 1) % max) + 1`, where
`max = 2^bits - 1`; their raw values remain unchanged in desired state, status,
logs, and container IDs. Ordinals do not wrap.

Both addresses are assigned before a run starts. `I` has `preferred_lft=0` and
is routed to whichever run is current. `O` is preferred and routed for the full
life of its run, including after rollover promotion. DNS, endpoint state,
ingress, typed Address refs, and direct clients use `I`; `O` is a routable
source identity, not a discovery address.

The prefix hierarchy is:

```text
Cluster ULA                                      /48
└── Space                                        /64
    └── Deployment                               /88
        └── Instance                             /100
            └── Version slot                     /120
                └── Address                      /128
```

`I` survives process restarts, deployment upgrades, and moves between nodes.
Space is deliberately part of identity: moving a deployment between spaces
changes all of its logical addresses and terminates existing connections. DNS,
endpoint status, ingress, and the product API expose `I`; routing and policy
also recognize each live run's `O`.

## Underlay requirements

Every node publishes one underlay address that is directly reachable from every
other node. A cluster initially uses one underlay family: all addresses are
either IPv4 or IPv6. The family is inferred from the address rather than stored
separately. The addresses may be globally routed or belong to a private network
shared by the cluster, but intermediate networks must permit IP protocol 41.

The initial transport is intentionally unauthenticated and unencrypted. Source
underlay-address filtering is useful but is not cryptographic node identity.
Applications requiring confidentiality or peer authentication must provide it
at the application layer until an optional secure underlay transport exists.

## Fixed tunnel interfaces

For each remote node, the agent creates one fixed tunnel interface in the host
network namespace. An IPv6 underlay uses `ip6tnl`:

```text
type:       ip6tnl
mode:       IPv6-in-IPv6
local:      this node's underlay IPv6 address
remote:     remote node's underlay IPv6 address
inner MTU:  underlay MTU - 40
```

An IPv4 underlay uses Linux SIT with the same IPv6 inner packet:

```text
type:       sit
mode:       IPv6-in-IPv4
local:      this node's underlay IPv4 address
remote:     remote node's underlay IPv4 address
inner MTU:  underlay MTU - 20
```

Tunnel names are deterministic from immutable node identity and must fit
Linux's 15-character interface-name limit. The initial conservative workload
MTU is 1420 for either family on a normal 1500-byte underlay.

The corresponding node creates the reverse fixed interface. The interface is
stateless per flow: it stores endpoint and device configuration, but it has no
connection establishment, stream, retransmission, NAT mapping, or per-workload
session. Every packet is independently encapsulated and decapsulated by the
kernel.

A fixed interface is the shared forwarding object for every workload currently
on that remote node:

```text
workload B1 /128 -> tunnel-to-node-B
workload B2 /128 -> tunnel-to-node-B
workload C1 /128 -> tunnel-to-node-C
```

Workload routes do not contain outer addresses. They only select the fixed
tunnel interface, whose configuration contains the outer source and
destination.

## Host routing

Each node's main IPv6 table contains three classes of OpenDeploy route:

```text
local workload /128  -> host-side veth/TAP
remote workload /128 -> fixed tunnel for its hosting node
cluster ULA /48      -> unreachable
```

The fallback `unreachable` route prevents an unknown ULA destination from
falling through to the host's default underlay route. The more-specific `/128`
routes win through normal longest-prefix matching and all OpenDeploy-owned
routes use a dedicated route-protocol tag for reconciliation.

The placement map is:

```text
logical workload address -> node id
node id -> reachable underlay address and fixed tunnel interface
```

There is no separate allocator or routing address. The underlay address is the
node's location and exists independently of workload identity.

## Packet walk

For workload A on node A sending to workload B on node B:

1. The packet enters node A from A's veth or TAP with logical source and
   destination addresses.
2. Attachment anti-spoofing verifies that the logical source belongs to A.
3. Optional egress policy evaluates the logical identities.
4. The `/128` route selects the fixed tunnel to node B.
5. The kernel prepends an outer IPv4 or IPv6 header with node A and B's underlay
   addresses and routes it through the physical network.
6. Node B's matching fixed tunnel removes the outer header and reinjects the
   unchanged logical packet.
7. Destination ingress policy evaluates the logical source and destination.
8. Node B's local `/128` route delivers the packet through B's veth or TAP.

Same-node packets never enter a tunnel. Their destination `/128` route sends
them directly from the host router to the destination attachment, with the same
attachment and destination policy semantics.

TCP, UDP, and ICMPv6 checksums remain valid because the inner IPv6 addresses
never change. Inner fragments and ICMPv6 errors are transported unchanged.
Path-MTU handling belongs to the standard kernel tunnel implementation.

## Placement changes

Moving a workload from node B to node C changes one route on every node that
holds the placement:

```text
before: workload /128 -> tunnel-to-node-B
after:  workload /128 -> tunnel-to-node-C
```

The node B and node C tunnel interfaces do not change. On node C the remote
route is replaced by the local attachment route; on node B the local attachment
route is replaced by the remote route or by `unreachable` while placement
converges.

The target attachment and local route must exist before the placement flip is
published. Updates are versioned and applied incrementally. A stale source may
temporarily send to node B; if B already knows the new placement, ordinary
routing can forward the inner packet through node C's tunnel. Otherwise the
fallback route rejects it. IPv6 hop limits bound accidental forwarding loops,
but placement generations and update ordering must prevent them rather than
relying on the hop limit.

Space moves remain security-domain migrations:

1. Remove the old endpoint from readiness and DNS.
2. Drain and stop the old logical identity.
3. Remove old routes and policy state.
4. Start the deployment with the new space-derived address.
5. Publish the new endpoint, route, and DNS state.

The old address must not forward into the new space.

## Node changes

Node enrollment distributes the new node's underlay address and creates one
fixed tunnel on every existing node plus the reverse tunnels on the new node.
Node removal deletes those interfaces after all workloads have moved or
stopped. Changing a node's underlay address updates or recreates its fixed
tunnel on every node and preserves workload placement routes where the kernel
interface identity can be retained.

This is quadratic node-level state:

```text
per node:      N - 1 fixed tunnel interfaces
cluster-wide:  N * (N - 1) interface objects
```

That is accepted for the initial target of at most approximately 100 nodes.
Before materially exceeding that target, replace the full mesh with a single
flow-based tunnel per node or a bounded-degree gateway topology. The logical
address and placement APIs do not need to change.

## Policy

The default destination policy is:

```text
deny workload traffic
allow source from destination space /64
allow explicit cross-space deployment/port/protocol rules
allow explicit OpenDeploy system paths
```

Attachment anti-spoofing remains mandatory for local workloads. A fixed inbound
tunnel interface also identifies the underlay address from which the packet was
expected, allowing node-aware source validation before destination delivery.
Because the underlay is unauthenticated, that check does not protect against an
attacker capable of spoofing node underlay addresses.

Policies are expressed and evaluated only in logical workload terms. Same-host
and cross-host packets hit equivalent destination-side policy after any tunnel
decapsulation.

## Control-plane state

The primary distributes full or incremental state shaped as:

```text
Node {
  id
  underlay_ipv6
}

Placement {
  logical_address
  node_id
  endpoint_state
}
```

Workers persist the latest accepted state and reconcile tunnel interfaces,
workload routes, policy, and DNS idempotently. Existing kernel forwarding does
not depend on primary or agent availability.

At the initial node limit, distributing every relevant placement to every node
is acceptable. Filtering by spaces and explicit policy remains useful and
preserves a path to later scale. A very large space remains the worst case
because every member may legitimately contact every other member.

## Current implementation boundary

Implemented now:

- The fixed address packing and bounds for space, deployment, ordinal, version
  slot, and run slot.
- Stable inbound `I` and run-scoped outbound `O` derivation, including nonzero
  slot normalization while retaining full-width versions and run numbers.
- Space `/64`, deployment `/88`, instance `/100`, and version-slot `/120`
  prefix helpers.
- Local netns/veth attachments and local `/128` routes.
- Dedicated route-protocol ownership and the cluster `/48` unreachable fallback.
- Optional underlay-address configuration and worker enrollment reporting into
  the node registry.
- Primary network maps containing observed `I` routes and derived `O` routes for
  published `STARTING` and `RUNNING` runners.
- Worker reconciliation of fixed `ip6tnl` or SIT interfaces and remote workload
  `/128` routes from persisted network maps.

Not implemented yet:

- Applying equivalent remote topology on the primary node.
- Cross-node source validation and destination policy.
- Reporting and distribution of unpublished candidate `O` routes, which are
  already routed locally but are not represented by published runner status.

Future service virtual addresses require a separate allocation and design.
The workload address ABI allocates only `I` and `O` and has no service range.

## Optional secure transport

An optional future secure underlay can transparently route each remote node's
underlay address through WireGuard. The IP-in-IP interfaces and workload routes
continue targeting the same underlay addresses, so enabling secure transport
does not change workload placement state. Secure transport is not part of the
base cross-node design.
