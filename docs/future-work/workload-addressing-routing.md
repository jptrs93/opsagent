# Workload addressing and routing

## Decision

OpenDeploy separates stable logical workload identity from cross-node routing location without allocating a second address. One cluster RFC 4193 ULA `/48` uses this fixed layout:

```text
<ULA /48> : <node:17> : <space:17> : <deployment:26> : <kind:4> : <field:16>
```

Node `0` means logical identity. A nonzero node value means a routing locator. Cross-node translation fills the source and destination node fields before WireGuard and zeros both fields after WireGuard. Space, deployment, kind, and field remain bit-for-bit unchanged.

This replaces the earlier plan to route stable logical `/128`s directly through WireGuard. The direct model made WireGuard configuration and reconciliation proportional to workload count and caused every workload placement change to update peer `AllowedIPs`. The selected model keeps WireGuard routing proportional to node membership or bounded routing next hops.

## Bit allocation

| Field | Bits | Values | Meaning |
|---|---:|---:|---|
| Node | 17 | 131,072 | `0` is logical; `1..131071` are routing nodes |
| Space | 17 | 131,072 | durable tenant/security domain |
| Deployment | 26 | 67,108,864 | stable deployment id; never recycled |
| Kind | 4 | 16 | deployment address type |
| Field | 16 | 65,536 | kind-specific ordinal or token |

Kinds:

| Kind | Meaning | Field |
|---|---|---|
| `0` | Stable instance | instance ordinal |
| `1` | Virtual service | `0` |
| `2` | Temporary rollover run | low 16 bits of run number |
| `3`-`15` | Reserved | undefined |

Run values may wrap because only concurrently live temporary runs must be distinct. Instance ordinals do not wrap. Space and deployment ids are part of the address ABI and must remain in range.

## Prefix hierarchy

Because node precedes space, WireGuard can aggregate every locator on one node while logical policy can aggregate every workload in one space:

```text
Cluster ULA                                      /48
├── Logical root (node = 0)                      /65
│   └── Space                                    /82
│       └── Deployment                           /108
│           └── Kind                             /112
│               └── Address                      /128
└── Locator root for each nonzero node           /65
    └── Space and unchanged workload identity
```

WireGuard needs one locator prefix per direct node peer or routed next hop:

```text
peer node-A AllowedIPs = <ULA + node-A>/65
peer node-B AllowedIPs = <ULA + node-B>/65
```

It does not need one `/128` per workload or one prefix per `(node, space)` pair.

## Identity semantics

A logical instance address is derived from:

```text
(cluster prefix, node=0, space, deployment, kind=instance, ordinal)
```

It survives process restarts, deployment upgrades, and rescheduling to another node. Space is deliberately part of identity: a space is a security/tenant domain, not a folder. Moving a deployment between spaces changes all of its logical addresses and terminates existing connections. The old address must not forward into the new space.

DNS, endpoint status, policy, logs, and the product API use logical addresses only. Locator addresses are internal dataplane values and never appear to workloads.

## Translation

Given logical source `L_A`, logical destination `L_B`, source node `A`, and placement `L_B -> B`:

```text
outbound source:      fill node(L_A) with A
outbound destination: fill node(L_B) with B
inbound source:       zero node(locator_A)
inbound destination:  zero node(locator_B)
```

Example:

```text
workload emits:       src=(0, space 10, deployment 5, instance 0)
                      dst=(0, space 20, deployment 9, instance 0)

WireGuard transports: src=(node 100, space 10, deployment 5, instance 0)
                      dst=(node 200, space 20, deployment 9, instance 0)

workload receives:    src=(0, space 10, deployment 5, instance 0)
                      dst=(0, space 20, deployment 9, instance 0)
```

The source host still needs placement state mapping a logical destination to its current node. The address design removes reverse identity reconstruction state: space and workload identity are retained in the locator, so inbound translation only zeros the node fields.

Translation is stateless but not checksum-free. Replacing IPv6 addresses requires incremental updates for TCP, UDP, and ICMPv6 pseudo-header checksums. The implementation must also handle fragments and ICMPv6 errors that embed translated packet headers. Encapsulation remains a fallback if rewriting proves too complex, but it adds another IPv6 header and lowers effective MTU.

## Packet walk

Cross-node workload traffic follows this order:

1. The packet enters the source host from a workload veth or TAP with logical node-zero addresses.
2. Attachment anti-spoofing verifies that the logical source belongs to that workload.
3. Optional egress policy evaluates logical source and destination identity.
4. The placement map supplies the destination network node id.
5. The host fills source and destination node fields and adjusts transport checksums.
6. WireGuard routes the destination locator by node `/65` and authenticates the source node `/65`.
7. The destination host verifies that the destination node field names itself and that the endpoint is locally placed.
8. The host zeros both node fields and restores affected checksums and embedded headers.
9. Destination ingress policy evaluates the restored logical addresses.
10. A local `/128` route delivers the packet to the destination workload.

Same-node traffic remains logical throughout and must hit equivalent attachment and destination policy.

WireGuard proves which node sent a locator, not which workload on that node originated it. Attachment anti-spoofing protects against untrusted workloads. If compromised-node impersonation is in scope, receivers additionally need authoritative `(node, logical workload)` placement validation for source identities.

## Policy

The default destination policy is:

```text
deny workload traffic
allow source from destination space /82
allow explicit cross-space deployment/port/protocol rules
allow explicit OpenDeploy system paths
```

The `/82` makes same-space membership independent of workload churn. Explicit policy can match a deployment's `/108` across instance, service, and run kinds. Prefix structure is an optimization and observability aid; attachment anti-spoofing and destination policy remain the security boundary.

Space moves are security-domain migrations:

1. Remove the old endpoint from readiness and DNS.
2. Drain and stop the old logical identity.
3. Remove old routes and policy state.
4. Start the deployment with the new space-derived address.
5. Publish the new endpoint and DNS state.

The migration must not preserve old-address forwarding because that would retain reachability granted by the old space.

## Control-plane state

WireGuard state changes for node membership, key rotation, endpoint changes, or routing-topology changes. It does not change for workload creation, deletion, restart, space move, or placement move.

Placement state still changes with workload topology:

```text
(logical address or deployment/ordinal) -> network node id
```

At 100,000 deployments, globally replicating placement state to 20,000 nodes can itself be expensive. State should be filtered by spaces and explicit policies relevant to local workloads, distributed incrementally, and sharded when required. A single very large space remains the worst case because every member may legitimately contact every other member.

## Node scale

The 17-bit field supports 131,071 nonzero routing node ids, but address capacity does not make a 20,000-node full WireGuard mesh viable:

```text
20,000 * 19,999 ~= 400 million directed peer relationships
```

The routing layer must permit bounded-degree topology at that scale, such as redundant site/region gateways, while retaining the same destination node `/65`. The next WireGuard peer may be the destination node in a small cluster or a gateway in a large cluster; the workload and locator address ABI does not change.

Network node ids are stable numeric routing identities. Node `0` is permanently reserved for logical addresses. If persisted database node ids are used directly, enrollment must reject values above 131,071. If ids are recycled later, reuse requires complete removal of the old peer and a quarantine/convergence rule; stale packets reaching a reused node must still fail destination-placement validation.

## Current implementation boundary

Implemented now:

- The fixed address packing and bounds.
- Logical node-zero instance, service, and run derivation.
- Space-dependent logical addresses.
- Node `/65`, space `/82`, and deployment `/108` prefix helpers.
- A node-field fill/zero helper that preserves all lower identity bits.

Not implemented yet:

- Network node-id enrollment/distribution.
- WireGuard peer and next-hop reconciliation.
- Placement-map distribution.
- Packet translation and checksum handling.
- Cross-node source/destination validation.
- Same-space and explicit cross-space policy enforcement.

## Migration

The cluster ULA `/48` remains unchanged. The layout replaces the earlier kind/deployment/field format before any user deployment used virtual mode. Host-mode deployments are unaffected.

Existing per-machine `opendeploy-net` deployments are the only virtual-mode workloads. They may remain attached to an old-address network namespace immediately after the agent upgrade. Redeploy each one to the new OpenDeploy version; normal container replacement tears down the old namespace and derives the correct space-0 logical address. No automatic address migration or database rewrite is required.
