# Networking

Design for the built-in networking layer: per-workload addressing, cross-machine routing, service discovery, ingress, network policy, and load balancing. Machine-local virtual networking, worker-to-worker and primary fixed-tunnel routing, node-local DNS, node-local ingress (TLS passthrough and HTTPS termination with central ACME issuance), and the network policy boundary (anti-spoofing, default same-space/global/system rules, and global override policies) are implemented — see `docs/engineering/networking.md`. Cluster-wide DNS from the map's catalog is implemented; remaining work covers multi-node ingress, load balancing, and multi-instance deployments; see `docs/future-work/cross-node-routing-implementation-plan.md` for the ordered cross-node implementation plan.

## Goals and principles

- Batteries included: one built-in network implementation, no plugin ecosystem. All components ship in the opendeploy binary; per machine there is the agent plus one netproxy system deployment.
- The primary is control plane only. It computes and distributes networking state; it is never on the datapath. A primary outage degrades to "no topology changes", never "traffic stops".
- No NAT-based service translation (no kube-proxy equivalent). Workload logical addresses are stable and identity-bound. Cross-node transport preserves the complete logical packet inside a stateless IPv6-in-IPv6 or IPv6-in-IPv4 envelope.
- Addresses are derived, not allocated. There is no IPAM state, allocator, or reuse policy.
- One logical workload network. Every space is a durable tenant/security domain with its own logical prefix, not a separate host-level virtual network or tunnel set.
- Network policy is a default security boundary: same-space traffic is allowed, cross-space traffic is denied unless explicitly allowed, and workload source addresses are validated at the host attachment boundary.
- Configuration lives on the deployment config (a `networking` section edited in a side panel). Cluster-scoped knobs are limited to settings (ingress machines) and are validated cluster-wide.
- Boring, debuggable dataplane first (netlink, fixed `ip6tnl` or SIT interfaces, nftables — inspectable with `ip` and `nft`). eBPF is a later optimization, never a prerequisite.

The scalability target is parity with Kubernetes: clusters of thousands of nodes. The shipped fixed-tunnel full mesh and whole-map distribution do not reach that, and a lower ceiling is accepted in the medium term while the product is being developed. Reaching the target requires a flow-based tunnel or bounded-degree routing design and per-node map distribution, without changing logical workload addresses.

## Addressing, dataplane, and netmap distribution

Implemented; see `docs/engineering/networking.md` for the address ABI (`I`/`O`
derivation with placement and run slots), netns/veth attachments, fixed node
tunnels, route ownership, `ClusterNetMap` rendering and distribution, and
rollover semantics.

Still open here:

- The service virtual address layout is decided (the deployment `/88` with
  the reserved top ordinal 4095, zero discriminator; see
  `service-balancing-and-attachment-nat.md`), but the address code does not
  yet reserve the ordinal and the balancing rungs that consume it are not
  built. The workload ABI allocates only `I` and `O`.
- A node address inside the cluster prefix and readiness-gated inbound
  addresses, so that connections to unready instances and to stale
  placements fail fast across nodes; see Endpoint selection and health. The
  exact derivation of the node address is undecided.
- Sender-side peer liveness probes feeding the balancing rungs' selection
  set; see Endpoint selection and health. Cadence and recovery window are
  undecided.
- Per-node maps scoped by reachability. Shipped 2026-10-06 (phase one of
  `per-node-netmap-implementation-plan.md`): the closure, the per-node render
  and stamps, the persisted `node_netmap` stamps, the per-node barrier rule,
  and the map as the only carrier of the cluster prefix. Still open from the
  design below: the per-commit dirty set (phase one re-renders every node
  from shared indexes and compares bytes), the hello-time hash, and deltas.
  Identity addressing cannot aggregate
  routes by node, so without pruning a node's route table is the cluster's
  placement count. The only sound basis for pruning is policy-derived
  reachability; pruning by observed traffic is rejected because it
  reintroduces a runtime lookup on the path. Agreed design:
  - A node's reachable set is: the placements in every space one of its own
    workloads occupies; the placements in every space joined to one of those
    by an explicit policy, in either direction, because replies and
    anti-spoofing need the reverse routes and peer entries; the backend
    placements of every ingress route the node publishes through a listen
    selector; and every placement in the global space (space 1), whose
    destinations accept every cluster source by definition.
  - Space 0 contributes nothing. The agent self-deployment is host-mode and
    never enters the map. Each node's `opendeploy-net` is a distinct
    deployment serving only clients on its own node, DNS is same-node by
    construction, and netproxies never need to reach each other, so the
    same-space rule does not apply to space 0. The build container is
    egress-only. A netproxy's only cross-node need is an ingress route whose
    listen selector names another node or `any_node()`, which the ingress
    term above covers; the blanket "any space 0 source" accept in the
    destination filter can then tighten to the local netproxy plus the
    netproxies of the nodes publishing a route to that deployment.
  - The map's `routes`, `nodes` (WireGuard peers), and DNS catalog are all
    pruned by the same set. Pruning the node list is what shrinks the tunnel
    mesh from `N * (N - 1)` to the reachable set; a workload resolving a name
    it cannot reach gets an upstream miss, which is accepted.
  - Each node's map is stamped with the global write seq of the last render
    that changed that node's content, held beside the cached per-node map.
    The global seq is monotonic across primary restarts, so a node's
    acceptance rule is unchanged. The per-node stamps are persisted with a
    content hash (`node_netmap`) so a primary restart keeps the stamp of an
    unchanged map; the table is a cache, not a fold of the log.
  - On each commit the primary derives the affected nodes from the mutations
    (a placement, node row, policy, space, or networking spec change) through
    a reverse index from space to hosting nodes and the policy adjacency,
    re-renders only those nodes' maps, and sends only the ones whose content
    changed. Whole per-node maps are sent first; deltas keyed by route prefix
    and node id are a later layer if per-node maps grow large.
  - Scalability therefore depends on cluster layout, and this is deliberate.
    With spaces partitioned onto dedicated nodes, a change in a space fans
    out to that space's nodes and each map is the size of that space.
    Cross-space policies, space-wide rather than deployment-scoped peers, a
    hub space many spaces talk to, `any_node()` ingress, and anything placed
    in the global space are escape hatches with a fan-out cost proportional
    to the reach they grant. The global space is kept as the one implicit
    broadcast set.
  - Rejected alternatives: a single global map shipped as deltas (fixes
    bandwidth but leaves every node holding cluster-wide routes and
    tunnels); shipping each node the filtered entities and rendering the map
    on the node (loses the single renderer and primary-side validation
    during mixed-version rollouts); relay or regional tiers in front of the
    primary (not a capacity need: one Go process serves tens of thousands of
    idle mTLS sessions, and the real per-session costs are the session-head
    snapshot and the per-commit subscriber work inside the write lock, both
    primary-internal and fixed by indexed per-node reads and per-node
    dispatch outside the lock).
- The rollover barrier at scale. Today a drained placement is terminated only
  once every connected node has applied the map that replaced it, with a
  fixed 30 s backstop for a node that is connected but wedged
  (`drainTimeout`). The barrier gates only the kill of the old container, not
  the route flip, and with incremental distribution it completes in one round
  trip; the scale problem is that at thousands of nodes some node is nearly
  always slow, so drains routinely run to the backstop. Decided refinements,
  in order:
  1. Membership by progress, not by a fixed clock. Extend the existing rule
     that a disconnected node does not hold the barrier: a connected node that
     has not applied a map within a short liveness window (a few heartbeats)
     drops out of the barrier and is flagged unhealthy. The fixed backstop
     stays as the last resort and is expected never to fire in practice.
  2. Membership by reachability. Shipped with the per-node maps: the barrier
     waits only on nodes whose map is stamped at or after the decision's
     write sequence, which is the set the pruning rule changed.
  3. Balanced traffic never depends on the barrier: a draining instance leaves
     the ready set, so clients dialing the service address are steered away
     as soon as their own node sees the readiness change. The barrier matters
     only to clients that dial an inbound address directly.
  Rejected: a percentage-of-nodes quorum (the denominator is wrong — the node
  still sending traffic can be in the excluded fraction — and the threshold
  has no principled value), and a forwarding stub on the old node after the
  container dies (WireGuard cryptokey routing drops a decrypted packet whose
  source is not in the sending peer's allowed prefixes, and the source is a
  client on a third node; making it pass would require source translation).
- NAT traversal and relaying are out of scope for the current transport. The
  base transport remains unauthenticated and unencrypted; the planned
  replacement is WireGuard with static node keys — cryptographic node-level
  source attribution via cryptokey routing, one interface with peer entries
  in place of the per-peer netdev mesh, and UDP underlay transport — since
  shipped; see `docs/engineering/networking.md`.

## Service discovery (DNS)

Cluster-wide DNS is implemented: the cluster map carries a DNS catalog (one
entry per virtual deployment, listing ordinals with a serving placement), each
node renders it into its netproxy state, and every node resolves every
`.internal` name. Resolution is health-free by design — records follow target
state, and health belongs to the load-balancing layer.

Discovery is always on. It is not a feature enabled in the networking panel; a deployment has a name the moment it exists.

## Ingress (reverse proxy)

Node-local ingress is implemented: TLS passthrough by SNI, HTTPS termination
with central ACME issuance and route/collision validation. Remaining work:

- **Multi-node ingress and cross-machine backends.** With cross-node routing, every ingress machine can serve every route; public DNS holds one A record per ingress machine. DNS round-robin is the availability model: a dead node's record persists until TTL expiry, partially mitigated by client retry across A records. Floating IPs, VRRP, and managed-DNS health checks are out of scope (underlay concerns); users needing faster failover use their DNS provider's health checks. Certificate and challenge distribution already works cluster-wide; the remaining piece is dialling backends on other machines over logical tunnel routes and designating ingress machines in settings (default: all machines once cross-node routing exists).
- **Drain-aware rollover integration.** The proxy stops selecting DRAINING endpoints for new requests and finishes in-flight ones. During a single-instance promotion it holds new requests for the sub-second route flip and releases them to the new instance (zero failed requests).
- **Web UI through the proxy.** The OpenDeploy web UI is served as a route through the same proxy (unifying the existing web ACME config). Today the web UI listener is a reserved claim in the ingress `listen` evaluation (`docs/engineering/networking.md`, Listen selectors): routes publish beside it on other addresses, and the blanket primary `:443` reservation is gone. Raw `portForwarding` claims on 80/443 are rejected on ingress machines.
- **Wildcard hosts** (`*.example.com`): precedence rules, DNS-01 challenges.
- **Raw TCP/UDP passthrough exposure** using the same host-listener-to-logical-route pattern, with PROXY protocol for source addresses.

## Network policy

Implemented — see the Network Policy section of `docs/engineering/networking.md`.
Override policies shipped as
global first-class entities (space or deployment peers) rather than the
deployment-config `allowedFrom` list sketched below; the design intent stands.

OpenDeploy has one cluster logical workload network. Each space has a derived `/64` logical subprefix, but isolation is enforced by policy rather than separate VRFs, tunnel fabrics, or independently generated ULA networks.

Default stance:

- Deny workload-to-workload traffic by default, then add generated allow rules.
- Allow same-space workload-to-workload traffic on all protocols and ports.
- Deny cross-space workload-to-workload traffic unless an explicit policy allows it.
- Allow internet egress by default through the machine-local egress path.
- Allow OpenDeploy system paths explicitly rather than relying on cluster-open defaults.

Enforcement has two mandatory layers:

1. **Source anti-spoofing at the workload attachment boundary.** Packets arriving from a workload veth/TAP may only use that run's assigned `I` or `O`. Both old and candidate attachments have their own `O` and the same non-preferred `I`; host routing determines which attachment receives inbound `I` traffic. This is required because policy trusts packet source identity; the fact that replies to spoofed traffic usually return elsewhere is not enough for UDP, QUIC Initials, audit correctness, quota, or privileged-source allowlists.
2. **Destination ingress policy before delivery to a workload attachment.** Packets about to enter a workload veth/TAP are dropped unless the source workload is in the same space, an explicit cross-space policy allows the source, or the traffic is an OpenDeploy system allow.

For workload-to-workload isolation, destination ingress policy plus source anti-spoofing is sufficient. A separate workload egress policy is not required for the v1 default boundary. Egress policy remains useful later for internet/private-network controls, host-service access, control-plane protection, and exfiltration-sensitive deployments.

Policies are expressed in logical workload terms: space, deployment, labels later, protocol, and port. The default same-space allow compiles to the source space's `/64`; explicit cross-space rules compile to deployment `/88` prefixes and port/protocol matches. Same-host and cross-host traffic hit the same destination-side policy after tunnel decapsulation. Fixed tunnel endpoints identify the expected sending underlay address but do not cryptographically authenticate it.

Policy always evaluates the unchanged logical source and destination addresses. Underlay addresses and tunnel interfaces are routing state, not workload policy identity.

## Endpoint selection and health

Agreed direction (2026-10-06). The overall approach is fixed; the address
scheme, probe cadence, and rejection mechanics are still to be refined.

- **The cluster state distributes no instance status or health.** The map
  carries target state only. Runner status flows node to primary for the
  scheduler and the UI and goes no further. There is no distributed ready
  set.
- **Node reachability is a concern of the connecting node, decided by its own
  probes.** The agent keeps a local set of unreachable peers from WireGuard
  handshake age for peers with traffic and periodic ICMPv6 echo over the
  tunnel for the rest, with a short recovery window. Nothing is reported
  upward. A global view would be wrong under an asymmetric partition and
  would lag both transitions.
- **Selecting an instance to connect to is three filters, in order:**
  instances whose target state is established, from the map; of those, the
  instances whose hosting node the connecting node can reach, from the
  local probe set; of those, the instances that are themselves reachable,
  which is decided lazily by attempting the connection.
- **An unready instance rejects connections immediately.** The hosting node
  makes `I` reachable only after the readiness signal and withdraws it when
  the task exits, for every upgrade strategy and for the first start, so a
  connection during warmup, after a crash, or during crash backoff fails
  fast with an ICMPv6 error instead of a timeout. Deployments without a
  `readinessSignal` are ready at task start. The connecting side retries
  another instance on failure. A crash loop therefore costs only the
  requests in flight during each live window, and crash backoff makes those
  windows rare; a distributed readiness signal could not do better because
  it lags both transitions.
- **The workload is responsible for its own health.** An unhealthy workload
  exits. An overloaded workload sheds load and stays healthy from the
  cluster's view; clients back off and retry. There are no probes of
  workloads and no "unhealthy but alive" state. Load-aware selection, if
  ever wanted, belongs to the L7 rung and is not health.
- **Fast failure across nodes requires a node address.** The host has no
  address inside the cluster prefix: the WireGuard device carries none and
  the veths carry only `fe80::1`. An ICMPv6 error the hosting node generates
  for a forwarded packet therefore uses a public or link-local source, which
  the sender's cryptokey routing drops, so today the error reaches only
  same-node senders. Each node gets an address inside the cluster prefix,
  derived rather than allocated (one option is a reserved ordinal of the
  node's own space 0 netproxy deployment), assigned to the WireGuard device,
  added to the peer's allowed prefixes on every other node, and carried in
  the map. Source selection then prefers it for every error toward a remote
  sender, and the error is matched as related to the original flow by the
  receiving node's conntrack. The underlay family is irrelevant: the error is
  an inner IPv6 packet.

## Load balancing

kube-proxy exists to translate stable virtual addresses to ephemeral endpoints. Stable inbound instance addresses remove that need for direct instance traffic. Balancing arrives in stages; users only ever set a replica count. The agreed rung-by-rung design — service virtual addresses, the connect hook, the sender-side DNAT fallback for Kata, and the attachment NAT boundary — is recorded in `service-balancing-and-attachment-nat.md`.

1. **DNS over established endpoint sets** (shipped). Resolution is health-free by design: a record follows target state and is published exactly when the ordinal is established, so a crashed instance keeps resolving. Health belongs to the rungs below, never to DNS answer content. Known limits (client caching, long-lived connection pinning, no health) are acceptable for internal traffic.
2. **eBPF socket-level balancing** (`cgroup/connect6` hook via `cilium/ebpf`). A future service virtual address is rewritten at `connect()` to an `I` chosen from the selection set (see Endpoint selection and health), per connection, before any packet exists — no translation state, the socket itself holds the decision. The service address is the deployment's reserved ordinal 4095 (see `service-balancing-and-attachment-nat.md`); the workload ABI allocates only `I` and `O`. Host-visible syscalls only: Kata guests fall through to the next rung.
3. **Sender-side service DNAT** at the source attachment boundary, for workloads the connect hook cannot see (Kata guests, unconnected UDP). The wire still carries real instance addresses; conntrack pins each flow's backend at flow birth. Confined to flows addressed to the service range — direct-address traffic keeps the stateless guarantee.
4. **L7 east-west through the embedded proxy** (opt-in, per deployment): retries, traffic splitting, per-route metrics for HTTP workloads. Ingress backends are rendered from the same established endpoint set as stage 1, with the same absence of health.

Interim DNAT-based virtual IPs remain rejected: no VIP ships before the balancing rungs, translation is never the universal east-west path, and a service address never transits a link. The scoped sender-side DNAT rung above is the deliberate exception, not a reversal — see `service-balancing-and-attachment-nat.md` for the reconciliation.

Traffic policy (future, with daemon sets): per-deployment `trafficPolicy: spread | prefer-local | local-only`, resolved in the balancing rungs (local-preference in the socket hook and below) — never by DNS answer content or ordering; DNS stays locality-free. Machine locality is derivable from the cluster map's routes.

## Multi-instance upgrades (future)

Single-instance ROLLOVER (same-node route flip) and cross-node replacement are
implemented; see `docs/engineering/networking.md`. Multiple instances default
to rolling recreate, one ordinal at a time, behind the endpoint set:

1. Move ordinal `i` to target `RUN_DRAINING` (its DNS record and ready-set membership follow target state, so it drops out of both; the other n−1 instances carry load).
2. Drain window, SIGTERM old instance.
3. Start the new instance with both `I` and its run-scoped `O`, then activate `I`; the old container is already gone.
4. Wait for the readiness report, promote the ordinal to target `RUN_SERVING`, advance to the next ordinal.
5. A new version that never reaches ready halts the rollout with the remaining old instances serving. Halt-and-alert, no auto-rollback; rollback is a redeploy of the previous version.

Surge mode (no capacity dip) applies the single-instance candidate flow per ordinal — same primitive.

Daemon sets are the rolling recreate loop iterated over machines; each replacement is machine-local.

## Dataplane acceleration (later)

The candidate fastpath, if forwarding CPU or packets-per-second ever matter, is
an nftables **flowtable**: a kernel-resident per-flow cache hooked at device
ingress. A flow's first packets traverse the full slow path (conntrack birth,
policy chains, NAT decision, route lookup); a `flow add @ft` statement on the
established-accept rule then snapshots the complete forwarding decision —
output device, next hop, conntrack NAT fixups, MTU — and subsequent packets
are matched by hash at ingress and transmitted directly, skipping conntrack
hooks, the forward chain, and the FIB lookup. Expiry or anything non-trivial
(TCP FIN/RST, PMTU events) falls back to the classic path.

Why it fits: policy already evaluates at flow birth and rides conntrack
thereafter, so the flowtable accelerates exactly and only packets the
established-accept rule would pass — semantics are unchanged by construction.
Conntrack remains the sole owner of flow state; the flowtable is a revocable
cache, and disabling it changes performance, never behavior. Flows carrying
conntrack NAT bindings (port forwards, IPv4 egress, and the conditional-SNAT /
service-DNAT flows of `service-balancing-and-attachment-nat.md`) are
accelerated with their rewrites applied in the fastpath. Offload is per-flow
opt-in via the `flow add` rule, so it can be scoped (e.g. tunnel-crossing
flows only).

Costs: offloaded packets no longer traverse the forward chain, so its
counters go dark for them and packet-path debugging must include
`nft list flowtables` / flow dumps; netaudit must audit the flowtable as one
more desired-state object; software flowtables work over veths and ip6tnl
devices (the `offload` hardware flag needs supporting physical NICs and does
not apply to those hops). Expected software-fastpath gain is roughly 2–3×
forwarding pps.

An XDP accelerator (per-device program using the conntrack kfuncs, redirecting
established flows before skb allocation) is the same architecture — netfilter
owns state, eBPF is the established-flow cache — with larger wins but a
hand-written second dataplane (route caching, NAT fixup, tunnel encap in BPF).
Ordering: flowtables first; XDP only if profiling demands it. Both preserve
the principle that kernel-native accelerators built against conntrack
accelerate this design without changing state ownership.

## Observability (later)

eBPF flow metrics (who talks to whom, bytes, connect failures) as the first eBPF adoption: read-only, no correctness burden, feeds the planned resource-monitoring feature and a topology UI. Precedes any eBPF on the datapath.

## Configuration surface additions

`portForwarding` and `ingress` are implemented on the deployment `networking`
section. Cross-space allow rules shipped as global network policy entities
(their own page and storage), not as a deployment-config `allowedFrom` list.
Future additions: trafficPolicy and replica-related knobs.

Settings (cluster-scoped): ingress machine designation.

## Remaining implementation phases

Phases 1 and 2 of the original plan (the machine-local virtual network, and
cross-node fixed-tunnel routing with netmap distribution, applied on workers
and the primary alike) have shipped. Phase 3 ingress has shipped node-locally
(TLS passthrough, HTTPS termination, ACME).

### Ingress completion

- Cross-machine backends over logical tunnel routes; multi-node ingress with per-node public DNS records and ingress-machine designation in settings.
- Rollover integration — drain awareness and hold-and-release during promotions; web UI served through the proxy.

Usable outcome: `ingress: {hostname}` gives a deployment a public HTTPS endpoint served from any ingress machine; raw `portForwarding` becomes the exception rather than the norm.

### Explicit policy and flow observability

- Explicit cross-space policy rules compiled to receiver-side nftables filters, distributed in the netmap — shipped as global network policy entities.
- eBPF flow metrics (first eBPF adoption; read-only) — remaining.

Usable outcome: cross-space access control from the Network page (shipped); traffic visibility in the UI (remaining).

### Multi-instance and load balancing

Coupled to the scheduler/replicas backlog item; networking consumes placements as `(deployment, ordinal) → machine` and never cares why.

- Endpoint sets with n > 1; rolling recreate and surge upgrade strategies; per-instance runner status/history keyed by `(deployment, ordinal)`.
- DNS multi-AAAA balancing (stage 1) arrives automatically.
- The service virtual address at reserved ordinal 4095; eBPF `connect6` for runc with sender-side service DNAT as the Kata-compatible fallback (see `service-balancing-and-attachment-nat.md`). Traffic policy for daemon sets remains part of this phase.
- L7 east-west through the proxy (stage 3), opt-in.
