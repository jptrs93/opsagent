# Network policy E2E coverage

## Status

Implemented and wired into the default flow. This document records what the
suite asserts about the policy boundary, and why each case is shaped the way it
is; the boundary itself is documented in
[`docs/engineering/networking.md`](../engineering/networking.md) and its design
in [`network-policy-implementation-plan.md`](network-policy-implementation-plan.md).

Pieces:

| piece | what it is |
|---|---|
| `testexamples/netprobe` | one binary, both roles: staged HTTP/UDP/SSE probe, and a server on several TCP ports plus UDP with `/bulk` and `/sse`. Signals readiness so the server role can be rolled over. |
| `testing-vms/e2e/helpers/netprobe.js` | the assertion vocabulary: `expectProbeAllowed`, `expectProbeDenied`, `expectProbeNotDenied`, stream-tick counting. |
| `cases/network-policy.js` | group A — same-node semantics. |
| `cases/network-policy-cross-node.js` | group B — cross-node and the primary. |
| `cases/network-policy-interactions.js` | group D — ingress, port forwarding, rollover, space move, established connections. |
| `cases/access-enforcement.js` | policy visibility and destination consent for a space admin. |
| `test-orchestrator/netpolicycheck.go` | group C — kernel-level checks, run on the second worker after the flows. |

## Why the oracle is staged

The suite's first three policy cases asserted `result=error` from a probe that
logged one line for every failure mode: NXDOMAIN, no route, refused, timeout,
TLS failure, 5xx. A regression that broke `.internal` resolution, or a server
that never started, would have kept the deny case green, and nothing proved the
same path worked when policy permitted it.

`netprobe` therefore resolves, dials and requests as separately reported
stages:

```text
netprobe probe=<label> stage=dns result=ok addr=fd… literal=false
netprobe probe=<label> stage=connect result=error err=timeout detail=…
netprobe probe=<label> result=ok status=200 bytes=…
```

A dropped packet is silent, so a policy denial can only ever look like a
connect timeout (a response timeout for UDP, which has no handshake).
`expectProbeDenied` requires fresh timeouts, no new successes in the same
window, **and** DNS still resolving — so it fails, rather than passes, when the
failure came from somewhere other than the boundary. Every denial is also
paired with a positive control on the same path: `netpol-peer` reaches the
server that `netpol-probe` cannot, and `netpol-remote-peer` reaches it across
the node tunnel that `netpol-remote` cannot.

Connections are never reused, so a policy change is observable on the next
probe interval without restarting a workload — which is the property the allow
and revoke cases assert.

## Addressing across nodes

Cross-node DNS does not exist (`netstate.pb` is derived node-locally), so a
cross-node probe must target a literal stable inbound address. The `address()`
env ref cannot supply one here: reference locality
(`validateAddressEnvRefs`) rejects a reference to a deployment in another
non-global space, which is exactly the shape every cross-space case needs.

So the workload reports its own addresses instead, classified by the address
ABI — the stable inbound address `I` is the one whose placement and run slots
(the last 28 bits) are zero:

```text
netprobe address name=netpol-server inbound=fd… outbound=fd…
```

The cases read that line out of the server's output
(`readDeploymentOutputMatch`) and compose target URLs around it. The line
repeats on the 30s heartbeat so it is always inside a recent log window.

## Group A — same-node semantics

Space `e2e-netpol` on worker-2 holds `netpol-server` (tcp/8080, tcp/8081,
udp/9000) and `netpol-peer`; `netpol-probe` and `netpol-probe-b` sit in global
on the same node.

| case | proves |
|---|---|
| `network-policy-cross-space-denied` | the cross-space default deny, as fresh connect timeouts with no successes |
| `network-policy-dns-allowed-cross-space` | DNS crosses the boundary while the payload does not — asserted from the same probe, so it cannot be satisfied by two different deployments |
| `network-policy-same-space-allowed` | the same-space default (positive control for the denial), and that a global-space destination accepts a source from another space |
| `network-policy-egress-open` | the boundary is destination-side: IPv4 egress out of an isolated space still works |
| `network-policy-allow-applied` | an override takes effect with neither workload restarted, and shows on the deployment inspector's derived view |
| `network-policy-port-scoped` | the allow is a port and protocol match, not a blanket accept: tcp/8081 and udp/9000 stay denied |
| `network-policy-port-range-widened` | the update path publishes (the publisher subscribes to policy updates, not only create and delete) and `PortEnd` compiles |
| `network-policy-protocol-scoped` | a udp-only rule opens UDP and closes both TCP ports |
| `network-policy-source-deployment-scoped` | a deployment source compiles to that deployment's prefix: a second global-space probe stays denied |
| `network-policy-space-destination` | a space destination opens every deployment in it |
| `network-policy-revoked` | revoking restores the deny for fresh connections and clears the derived view, leaving the same-space default untouched |
| `network-policy-dangling-fails-closed` | a rule whose peer was deleted is flagged dangling and opens nothing — ids are never recycled |
| `network-policy-same-space-rejected` | a rule redundant with the same-space default is rejected on write |

## Group B — cross-node and the primary

Policy binds to delivery into a local attachment, so a packet crossing a tunnel
is evaluated once, on the destination node, after decapsulation.

| case | proves |
|---|---|
| `network-policy-cross-node-same-space-allowed` | the cross-node path works with no policy (control for the whole group) |
| `network-policy-cross-node-denied` | the cross-space deny is identical across nodes |
| `network-policy-cross-node-allowed` | rules ride the cluster network map and are applied by the receiving node |
| `network-policy-cross-node-pmtu` | a 2MB response completes through the tunnel: the ICMPv6 packet-too-big path survives as related traffic. Asserted on the exact byte count, because a lost one stalls rather than errors |
| `network-policy-primary-node-enforced` | the primary enforces through its own in-process netmap applier, which nothing else in the suite exercises |
| `network-policy-cross-node-revoked` | revoking republishes and re-denies both the remote worker and the primary |

## Group C — kernel checks (orchestrator)

`netpolicycheck.go` runs on worker-2 after the flows. The Playwright container
cannot read nftables counters, enter a workload netns, or restart an agent —
and the last two checks deliberately break kernel state, which no later flow
could tolerate. Workload ids reach it through `test-results/netpolicy.env`,
written by `network-policy-kernel-check-state`: the checks address workloads
the way the kernel does (netns `opendeploy-<id>-v<version>`, veth
`od<id>s<slot>`) and cannot authenticate to the API.

| check | proves |
|---|---|
| `spoofed-source-dropped` | anti-spoofing, the root of trust for every address rule. An unassigned address in the peer's **own** `/64` is spoofed, so destination policy would accept it and only the `(veth, source)` pair check can drop it; the drop counter must advance |
| `v4-container-to-container-dropped` | the machine-local IPv4 path stays egress-only — without this, container-to-container v4 would bypass the logical boundary entirely |
| `drop-counters-attributable` | the denied probe's traffic lands on the destination deployment's own counted drop, turning "it failed" into "the boundary dropped it" |
| `skeleton-and-elements-present` | the static forward program persists with its counters (a skeleton rebuilt per reconcile would have reset them) and every attachment is in `managed`, `src_ok` and `dst_dispatch` |
| `netaudit-reports-divergence` | flushing a `wl_dst` chain is reported as divergence naming the missing filter rules, and is **not** silently repaired |
| `agent-restart-rederives-filter` | filter state is re-derived from recovered attachments after a restart, traffic recovers, and netaudit returns to in-sync. This is also what repairs the chain the previous check flushed |

The netaudit check reports itself inconclusive rather than failing if an
unrelated reconcile rebuilds the chain before the audit tick lands.

## Group D — interactions

| case | proves |
|---|---|
| `network-policy-port-forward-external-open` | externally DNATed traffic reaches a workload in a non-global space: non-cluster sources fall through the cluster-prefix drop |
| `network-policy-ingress-into-isolated-space` | the netproxy dials a backend across a space boundary — the only end-to-end exercise of the system-space default, which every other ingress case masks by living in global |
| `network-policy-rollover-continuity` | an allowed peer sees no denial across a promotion, where old and candidate attachments share `I` and the destination chain is shared per deployment |
| `network-policy-follows-space-move` | a deployment-anchored rule keeps applying after its source moves space, because the peer stores the id alone and its space resolves at render time |
| `network-policy-established-survives-revoke` | enforcement is stateful: after a revoke, an established SSE stream keeps ticking while fresh connections time out |

The space-move case asserts fresh successes after the move rather than
uninterrupted ones: a space move is a connection-breaking security-domain
migration that replaces the placement, so the claim under test is that the rule
follows, not that nothing drops.

## Authorization

`access-restricted-network-policy-consent` runs inside the access-enforcement
window, while the restricted space-admin session is live: policies with no
visible peer are hidden from it, a rule pointed at its space becomes visible,
and it may delete that rule — update access on the destination space is the
consent the model is built on.

The *denial* half is not expressible through the UI: the policy form offers
only peers the session can see, so a space admin cannot name a destination it
lacks access to. That rejection, DENY-action rejection, and peer-existence
validation stay unit-level in `webuihandler/network_policies_test.go` —
request bodies are binary protobuf, so driving them from Playwright would mean
hand-encoding requests.

## Not covered

- **DENY enforcement** — rejected on writes; nothing to observe end-to-end.
- **Mixed-version policy behaviour** — the "older" release the harness installs
  is built from the same tree, so it decodes `policy_rules` like any other
  agent. A true old-agent test needs a pinned real release and belongs with the
  upgrade-compat work.
- **Stale-map enforcement during a primary outage** — the cached map must keep
  enforcing while a new rule does not take effect until reconnect. It needs a
  primary-down window mid-flow, which the harness currently structures around
  backup/restore only.
- **User-device grants and workload egress policy** — unimplemented.
