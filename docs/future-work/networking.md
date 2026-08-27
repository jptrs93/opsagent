# Networking

Design for the built-in networking layer: per-workload addressing, cross-machine routing, service discovery, ingress, network policy, and load balancing. Machine-local virtual networking, worker-to-worker and primary fixed-tunnel routing, node-local DNS, node-local ingress (TLS passthrough and HTTPS termination with central ACME issuance), and the network policy boundary (anti-spoofing, default same-space/global/system rules, and global override policies) are implemented — see `docs/engineering/networking.md`. Remaining work covers cross-node DNS, multi-node ingress, load balancing, and multi-instance deployments; see `docs/future-work/cross-node-routing-implementation-plan.md` for the ordered cross-node implementation plan.

## Goals and principles

- Batteries included: one built-in network implementation, no plugin ecosystem. All components ship in the opendeploy binary; per machine there is the agent plus one netproxy system deployment.
- The primary is control plane only. It computes and distributes networking state; it is never on the datapath. A primary outage degrades to "no topology changes", never "traffic stops".
- No NAT-based service translation (no kube-proxy equivalent). Workload logical addresses are stable and identity-bound. Cross-node transport preserves the complete logical packet inside a stateless IPv6-in-IPv6 or IPv6-in-IPv4 envelope.
- Addresses are derived, not allocated. There is no IPAM state, allocator, or reuse policy.
- One logical workload network. Every space is a durable tenant/security domain with its own logical prefix, not a separate host-level virtual network or tunnel set.
- Network policy is a default security boundary: same-space traffic is allowed, cross-space traffic is denied unless explicitly allowed, and workload source addresses are validated at the host attachment boundary.
- Configuration lives on the deployment config (a `networking` section edited in a side panel). Cluster-scoped knobs are limited to settings (ingress machines) and are validated cluster-wide.
- Boring, debuggable dataplane first (netlink, fixed `ip6tnl` or SIT interfaces, nftables — inspectable with `ip` and `nft`). eBPF is a later optimization, never a prerequisite.

The initial fixed-tunnel full mesh targets clusters of at most approximately 100 nodes. Larger topologies require a later flow-based tunnel or bounded-degree routing design, without changing logical workload addresses.

## Addressing, dataplane, and netmap distribution

Implemented; see `docs/engineering/networking.md` for the address ABI (`I`/`O`
derivation with placement and run slots), netns/veth attachments, fixed node
tunnels, route ownership, `ClusterNetMap` rendering and distribution, and
rollover semantics.

Still open here:

- Future service virtual addresses require a separate allocation and address
  design. The workload ABI allocates only `I` and `O`, with no compatibility
  range for a service virtual address.
- Full snapshots are acceptable for the initial approximately 100-node target;
  later scale requires incremental and sharded distribution, and a flow-based
  tunnel or bounded-degree gateway topology in place of the `N * (N - 1)`
  fixed-tunnel mesh.
- NAT traversal and relaying are out of scope. The base transport remains
  unauthenticated and unencrypted until an optional secure underlay transport
  exists.

## Service discovery (DNS)

Node-local DNS in the netproxy system deployment is implemented. Remaining
work is cross-node discovery: `netstate.pb` is derived node-locally from the
placements a node holds, so `.internal` names resolve only on the node running
the deployment. Cluster-wide DNS renders every node's ready endpoints into
each node's netproxy state (part of the cross-node plan's DNS milestone).

Discovery is always on. It is not a feature enabled in the networking panel; a deployment has a name the moment it exists.

## Ingress (reverse proxy)

Node-local ingress is implemented: TLS passthrough by SNI, HTTPS termination
with central ACME issuance and route/collision validation. Remaining work:

- **Multi-node ingress and cross-machine backends.** With cross-node routing, every ingress machine can serve every route; public DNS holds one A record per ingress machine. DNS round-robin is the availability model: a dead node's record persists until TTL expiry, partially mitigated by client retry across A records. Floating IPs, VRRP, and managed-DNS health checks are out of scope (underlay concerns); users needing faster failover use their DNS provider's health checks. Certificate and challenge distribution already works cluster-wide; the remaining piece is dialling backends on other machines over logical tunnel routes and designating ingress machines in settings (default: all machines once cross-node routing exists).
- **Drain-aware rollover integration.** The proxy stops selecting DRAINING endpoints for new requests and finishes in-flight ones. During a single-instance promotion it holds new requests for the sub-second route flip and releases them to the new instance (zero failed requests).
- **Web UI through the proxy.** The OpenDeploy web UI is served as a route through the same proxy (unifying the existing web ACME config); today the primary node reserves `:443` for the web UI instead. Raw `portForwarding` claims on 80/443 are rejected on ingress machines.
- **Wildcard hosts** (`*.example.com`): precedence rules, DNS-01 challenges.
- **Raw TCP/UDP passthrough exposure** using the same host-listener-to-logical-route pattern, with PROXY protocol for source addresses.

## Network policy

Implemented — see the Network Policy section of `docs/engineering/networking.md`
and `network-policy-implementation-plan.md`. Override policies shipped as
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

## Load balancing

kube-proxy exists to translate stable virtual addresses to ephemeral endpoints. Stable inbound instance addresses remove that need for direct instance traffic. Balancing arrives in stages; users only ever set a replica count.

1. **DNS over ready endpoint sets.** Health-aware by construction: not-ready instances do not resolve. Known limits (client caching, long-lived connection pinning) are acceptable for internal traffic.
2. **Possible eBPF socket-level balancing** (`cgroup/connect6` hook via `cilium/ebpf`). A future service virtual address could be rewritten at `connect()` to a chosen READY `I`, per connection, before any packet exists. No service DNAT or conntrack would be required; ordinary logical routing would then select a local attachment or fixed node tunnel. This depends on a separate future service-address allocation and design because the current workload ABI allocates only `I` and `O`. Kata guests also require a different interception design.
3. **L7 east-west through the embedded proxy** (opt-in, per deployment): retries, traffic splitting, per-route metrics for HTTP workloads. Ingress traffic already gets endpoint-set balancing from stage 1.

Interim DNAT-based virtual IPs are explicitly rejected. No service virtual address exists until a separate allocation and implementation is designed.

Traffic policy (future, with daemon sets): per-deployment `trafficPolicy: spread | prefer-local | local-only`, resolved by DNS answer ordering and, in stage 2, by local-preference in the socket hook. Machine locality is already in the endpoint record.

## Multi-instance upgrades (future)

Single-instance ROLLOVER (same-node route flip) and cross-node replacement are
implemented; see `docs/engineering/networking.md`. Multiple instances default
to rolling recreate, one ordinal at a time, behind the endpoint set:

1. Mark ordinal `i` DRAINING (drops out of DNS and balancing; the other n−1 instances carry load).
2. Drain window, SIGTERM old instance.
3. Start the new instance with both `I` and its run-scoped `O`, then activate `I`; the old container is already gone.
4. Wait for ready, mark READY, advance to the next ordinal.
5. A new version that never reaches ready halts the rollout with the remaining old instances serving. Halt-and-alert, no auto-rollback; rollback is a redeploy of the previous version.

Surge mode (no capacity dip) applies the single-instance candidate flow per ordinal — same primitive.

Daemon sets are the rolling recreate loop iterated over machines; each replacement is machine-local.

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
- A separately allocated service virtual address design and a Kata-compatible balancing mechanism; eBPF `connect6` is one runc-specific option. Traffic policy for daemon sets remains part of this phase.
- L7 east-west through the proxy (stage 3), opt-in.
