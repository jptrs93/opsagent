# Service balancing and the attachment NAT boundary

Design note (2026-08-30) recording the agreed direction for service virtual
addresses, load balancing beyond DNS, workload-source-address ownership, and a
runtime-uniform attachment boundary. Extends [networking.md](networking.md) and
[kata-networking.md](kata-networking.md) and revises their load-balancing
stances. Nothing here is scheduled; prerequisites are listed at the end.

## Summary of decisions

- Balancing is a ladder of binding points, each rung fixing the failure mode of
  the one below, taken only where the rung above cannot operate:
  resolution-time (DNS), connection-time (host `connect` hook), packet-time
  (sender-side service DNAT), request-time (opt-in L7 through netproxy). All
  rungs consume one distributed ready-endpoint set.
- A service virtual address is a new derived range: a pure function of
  `(space, deployment)`, in a region of the deployment `/88` reserved by a
  future allocation design. It is never routed, never appears in `src_ok`, and
  never leaves a host: on a node missing the balancing machinery it falls to
  the `unreachable` cluster `/48` route — fail closed.
- Instance inbound addresses `I` are never balance-translated. DNS answers,
  `Address` env refs, and `{ordinal}.{name}` names must keep meaning the exact
  instance they name.
- All translation is sender-side, at the source attachment boundary. The wire,
  the tunnels, and destination-side policy always see real instance addresses;
  the service address never transits a link.
- The attachment boundary gains a conditional conntrack SNAT: a flow initiated
  from a managed attachment with source `I` is SNATed to that attachment's `O`
  at flow birth. Cooperation is an optimization, not a requirement — a
  workload that sources `O` correctly (runc source selection, or a cooperative
  Kata guest) never matches the rule and stays as stateless as today, per
  flow, automatically.
- runc and Kata attachments get the identical boundary program. Runtime
  differences reduce to attachment plumbing (netns join vs TC redirect to
  TAP). Guest-side dual-address configuration (Kata netns mirroring of `I`
  with `preferred_lft=0` plus preferred `O`) is downgraded from a correctness
  requirement to a performance optimization.

## The load-balancing ladder

| Rung | Decision binds at | State left behind | Covers |
|---|---|---|---|
| DNS over established endpoints (shipped: health-free, target-state only) | resolution | client caches (uncontrolled) | everything, weakly |
| `cgroup/connect6` hook | `connect()` | the socket itself (zero marginal state) | host-visible syscalls: runc, connected sockets |
| Sender-side service DNAT | first packet | conntrack NAT binding per flow | universal: Kata guests, unconnected UDP |
| L7 via netproxy | per request | proxy connection state | HTTP semantics: retries, splits, drain |

The rungs are self-dispatching by destination address alone. A connect-hook
rewrite means the packets already carry a real instance address and the DNAT
rule never matches; a Kata guest's packets still carry the service address and
fall through to the DNAT rung. No per-runtime configuration exists on the
datapath. This is the same layering Cilium runs (socket LB above a per-packet
tc service path), where VM runtimes land on the packet path by construction.

The connect hook rewrites the sockaddr before any packet exists: zero
per-packet cost, zero translation state, and the decision dies with the socket
that holds it. It is the preferred rung wherever the syscall is host-visible.
It requires maintaining the `getpeername` illusion (or an explicit decision
that leaking real instance addresses is acceptable) and would be the first
load-bearing eBPF in the dataplane. Unconnected UDP senders need the
`sendmsg6`/`recvmsg6` hook pair, or a declared connected-sockets-only contract
for the hook rung (unconnected traffic then uses the DNAT rung).

The DNAT rung is one nftables rule at the source attachment
(`ip6 daddr <service-range> dnat to <hash> map @backends_<dep>`), with backend
maps updated by the agent as readiness changes. Conntrack records the choice at
flow birth and applies it, both directions, for the flow's life — map updates
steer only new flows. Selection policy is uniform random (or consistent hash)
over the ready set — the same policy Cilium ships; anything smarter belongs to
the L7 rung. Because the chosen backend address is a meaningful stable `I`, a
captured packet or stuck flow is self-describing even on this stateful path.

Reconciliation with the standing "no NAT-based service translation" principle:
what that principle rejects is translation as the universal or interim
mechanism — wire addresses that lie, and conntrack as a cluster-wide
correctness dependency (the kube-proxy shape). Sender-side DNAT scoped to an
opt-in service range keeps wire addresses true and confines flow-state
criticality to flows that chose balancing. It is the "deliberate performance
and semantics tradeoff" the Kata note already reserved.

## Source-address ownership: the conditional SNAT hybrid

`O` exists so that no run's initiated traffic can claim the stable inbound
identity `I` — during rollover, replies to a candidate's warmup traffic must
not be routed by the `I` route to the old run. Today this invariant is held by
convention: the guest kernel's source selection (`preferred_lft=0` on `I`)
plus workload cooperation. Two findings change the picture:

**A purely stateless `I→O` rewrite at the boundary is unsound.** A packet with
source `I` leaving an attachment must be rewritten iff its flow was *initiated*
by the workload; replies on flows the workload *accepted* must keep source `I`
(the client dialed `I`). Flow role is per-flow information no stateless rule
can recover — the NPTv6/ILA analogy fails because those translate by address,
not by flow role. Someone must hold the role bit: the guest's socket table
(dual-address cooperation, free) or host conntrack (SNAT, taxed). There is no
third option.

**Conntrack NAT expresses the hybrid exactly.** NAT chains are consulted only
for a flow's first packet, whose direction is known:

```text
postrouting: iifname <veth> ip6 saddr <I> snat to <O_attachment>
```

- Initiated flow sourcing `O`: rule does not match; no binding; the flow is as
  stateless as today.
- Initiated flow sourcing `I`: binding recorded at birth; that flow — and only
  that flow — is conntrack-dependent; replies to `O` reverse-translate
  automatically, ICMPv6 embedded packets included.
- Accepted inbound flow: created by an external packet, so the outbound NAT
  chain was never consulted; replies keep source `I`.

Degradation is per-flow and automatic; the packet's own source address at flow
birth is the dispatch. Anti-spoofing runs pre-NAT and `(veth, I)` is already in
`src_ok`. Destination policy sees post-NAT `O`, and because `I` and `O` share
the space `/64` and deployment `/88`, every prefix-compiled policy verdict is
identical either way — the address ABI makes this NAT policy-transparent.

The SNAT also closes a latent hole that exists today on runc: nothing stops a
workload from explicitly binding `I` and initiating outbound connections, and
during a rollover a candidate doing so has its replies misdelivered to the old
run. The boundary rule turns the `O` invariant from convention into
enforcement for every runtime.

A workload dialing a service address from a lazy guest needs both translations;
conntrack merges them into one flow entry (original `(src=I, dst=service)`,
reply `(src=instance, dst=O)`) — one lookup per packet covers both.

**Residual collision and its plug.** If the old run and a candidate are *both*
lazy, both can initiate flows from the shared `I` with colliding ephemeral
ports; host conntrack keys on tuples, not ingress interface, so the second
flow would inherit the first's binding and cross-wire. Fix: per-attachment
conntrack zones (`ct zone set` keyed on `iifname` for workload-originated
packets, and on a `daddr→zone` map over each `O` for return traffic). Zones
are only exercised by lazy-guest rollover overlap and can be deferred until
Kata attachments exist.

## Runtime uniformity

Every attachment gets the same boundary program: conditional SNAT, service
DNAT, zones, anti-spoofing, routes, and both addresses offered to the guest.
The cost of the unified program on cooperative flows is nil: conntrack already
tracks every east-west flow (the `ct established,related` fast path), NAT
chains are evaluated at flow birth only, and dormant rules add nothing per
packet. The optimization layers above — kernel source selection producing `O`,
and the connect hook — are not configured per runtime; each catches what it
can see, and what falls through is caught correctly below. A Kata guest is not
a special case, merely a workload for which both optimizations are blind.

Consequences for [kata-networking.md](kata-networking.md): the open question
"whether `I` and `O`, including `preferred_lft=0`, can be reliably
preconfigured through Kata's netns mirroring" stops being load-bearing —
mirroring fidelity buys statelessness, its absence costs one SNAT binding per
initiated flow. Host `cgroup/connect6` remains runc-only; the DNAT rung is the
Kata-compatible balancing mechanism that note asked for.

## Cost calibration (vs Cilium)

- Per-packet cost of the stateful paths is one flow-table lookup plus rewrite
  and incremental checksum fixup — the same order as Cilium's tc service path,
  which runs at production packet rates without this being the dominant cost.
  Checksum fixup on locally generated `CHECKSUM_PARTIAL` packets is a
  pseudo-header seed adjustment; GSO batches the work per super-packet.
- Cilium + Kata pays per-packet DNAT on service flows only; design here
  additionally SNATs lazy-guest direct flows — the price of carrying the I/O
  identity split (which Cilium does not have, because it has no stable-address
  route-flip rollover) across a VM boundary while permitting single-address
  guests.
- Where Cilium reconstructs identity via a distributed ipcache, every stateful
  flow here still carries meaningful derived addresses end to end; no
  IP→identity table exists to lag or desynchronize.
- Because kernel conntrack (not a private clone) owns all flow state here, the
  nftables flowtable fastpath (see Dataplane acceleration in `networking.md`)
  accelerates these stateful flows as-is, NAT fixups included, with no change
  to state ownership.

## Prerequisites and open questions

Prerequisites, in order:

1. Cluster-wide ready-endpoint distribution to every node (shared prerequisite
   with cross-node DNS; same input data, second consumer — feeds backend maps
   and the connect hook's BPF map).
2. The service-address allocation design (range, encoding, validation).
3. Multi-instance deployments (n > 1) for balancing to have something to do.

Open questions:

- Conntrack scrubbing on backend removal (delete CT entries pinning flows to a
  dead backend via ctnetlink, as Cilium does) vs accepting flow-timeout death.
- Whether the connect-hook rung ships before or with the DNAT rung, and the
  netaudit extension for auditing eBPF map state alongside nftables/routes.
- Unconnected UDP: `sendmsg6`/`recvmsg6` hooks vs connected-only contract.
- Relationship to the possible DNAT-backed graceful-rollover mode
  ([kata-networking.md](kata-networking.md) open question): same machinery
  (sender-invisible per-flow DNAT), potentially shared implementation.
- Zone lifecycle details (assignment map hygiene across attachment reuse).
