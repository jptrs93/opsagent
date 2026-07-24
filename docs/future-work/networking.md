# Networking

Design for the built-in networking layer: per-workload addressing, cross-machine routing, service discovery, ingress, network policy, and load balancing. Machine-local virtual networking and TLS passthrough ingress are partially implemented; cross-machine routing, L7 ingress, policy, and load balancing remain future work. See `docs/engineering/networking.md` for the current implementation and `docs/future-work/cross-node-routing-implementation-plan.md` for the ordered cross-node implementation plan.

## Goals and principles

- Batteries included: one built-in network implementation, no plugin ecosystem. All components ship in the opendeploy binary; per machine there is the agent plus one netproxy system deployment (see Component model).
- The primary is control plane only. It computes and distributes networking state; it is never on the datapath. A primary outage degrades to "no topology changes", never "traffic stops".
- No NAT-based service translation (no kube-proxy equivalent). Workload logical addresses are stable and identity-bound. Cross-node transport preserves the complete logical packet inside a stateless IPv6-in-IPv6 or IPv6-in-IPv4 envelope.
- Addresses are derived, not allocated. There is no IPAM state, allocator, or reuse policy.
- One logical workload network. Every space is a durable tenant/security domain with its own logical prefix, not a separate host-level virtual network or tunnel set.
- Network policy is a default security boundary: same-space traffic is allowed, cross-space traffic is denied unless explicitly allowed, and workload source addresses are validated at the host attachment boundary.
- Configuration lives on the deployment config (a `networking` section edited in a side panel). Cluster-scoped knobs are limited to settings (ingress machines) and are validated cluster-wide.
- Boring, debuggable dataplane first (netlink, fixed `ip6tnl` or SIT interfaces, nftables — inspectable with `ip` and `nft`). eBPF is a later optimization, never a prerequisite.

The initial fixed-tunnel full mesh targets clusters of at most approximately 100 nodes. Larger topologies require a later flow-based tunnel or bounded-degree routing design, without changing logical workload addresses.

## Addressing

### Virtual network

The virtual network is IPv6-only, using an RFC 4193 ULA `/48` prefix generated randomly by primary installation and persisted. There is no operator-supplied network configuration and no realistic collision with corporate networks, cloud VPCs, or other OpenDeploy clusters.

### Address layout

Addresses are pure functions of existing identifiers. Layout of the 80 host bits after the `/48` prefix:

```
<ULA:48><space:16><deployment:24><ordinal:12><versionSlot:20><runSlot:8>
```

| Field | Bits | Capacity | Meaning |
|---|---:|---:|---|
| Space | 16 | 65,536 | tenant/security domain |
| Deployment | 24 | 16,777,216 values | deployment identity; `0` is invalid |
| Ordinal | 12 | 4,096 | stable instance ordinal |
| Version slot | 20 | 1,048,575 nonzero | normalized config version |
| Run slot | 8 | 255 nonzero | normalized run number |

Properties:

- `I = Address(prefix, space, deployment, ordinal, 0, 0)` is the stable inbound address. It survives restarts, upgrades, and node rescheduling. Moving the instance to another space changes it.
- `O = Address(prefix, space, deployment, ordinal, versionSlot, runSlot)` is the preferred outbound source for one complete virtual run. Both slots must be nonzero.
- Full config versions and run numbers remain full-width. Address derivation normalizes each positive value with `((n - 1) % max) + 1`, where `max = 2^bits - 1`.
- Every virtual run is preassigned both `I` and `O`. `I` has `preferred_lft=0`; `O` remains preferred and routed throughout setup, warmup, activation, promotion, and the rest of the run.
- Setup routes `O`. Activation or promotion routes `I` to the current attachment. DNS, endpoints, ingress, and Address refs use `I`.
- Every logical space is one `/64`, making same-space policy a prefix rule. All addresses of one deployment share one `/88`; one instance is a `/100`, and one version slot is a `/120`.
- Space and deployment ids are never recycled, so logical prefixes are never reused.
- Ordinals do not wrap. Version and run slots wrap only through normalization; live `O` collisions must be rejected.

Future service virtual addresses require a separate allocation and address
design. The workload ABI allocates only `I` and `O`, with no compatibility
range for a service virtual address.

### Spaces and address derivation

Space is part of logical network identity because a space is a tenant/security domain, not an organizational folder. Moving a deployment to another space is an explicit address-changing redeployment: existing connections terminate, DNS and endpoint state change, and the old address is not forwarded into the new space. Prefix shape makes the default same-space rule compact, but attachment anti-spoofing and destination policy remain the security boundary.

### IPv4 egress

Containers keep a machine-local IPv4 path for internet egress: a fixed private range reused identically on every machine (not routable off-host), masqueraded to the host's default interface. IPv4 is never used for cross-node workload, service, or ingress-to-backend traffic.

### Application requirements

- Workloads must listen on the IPv6 wildcard (`::`). Binding `0.0.0.0` only makes an app unreachable at its instance address (from peers and from ingress alike).
- Workloads must not bind to a specific address. Every virtual run starts with both `I` and `O`; `O` is preferred for the run's full life and `I` remains non-preferred even after activation or rollover promotion.

OpenDeploy detects IPv4-only listeners from the container netns (`/proc/net/tcp6` vs `tcp`) after readiness and surfaces a diagnostic on the status card. One failure mode, one diagnostic.

## Component model

Two distinct concerns with opposite operational profiles:

1. **Networking reconciler** (control plane): programs fixed node tunnels, netns/veths, host routes, and nftables from the netmap. Privileged (NET_ADMIN), but its availability is irrelevant to traffic — the virtual network's dataplane is the kernel, and kernel state persists across restarts. There is no userspace daemon for the network itself.
2. **Userspace datapath services**: the DNS server and ingress proxy. Unprivileged, but process lifetime is traffic availability.

Layout per machine:

- **Agent** (existing opendeploy process, root): deployment operator plus the networking reconciler as an in-process subsystem. In-agent because there is no availability gain from separating it (kernel state persists; no new containers start while the agent is down), no privilege gain (the agent is already root), and veth setup / promotion route flips sequence synchronously with runner and operator code paths — a separate daemon would recreate the kubelet↔CNI RPC boundary this design avoids.
- **Netproxy system deployment**: an internal per-machine system deployment run via containerd, following the `OPENDEPLOY` internal-deployment pattern. It hosts the DNS server and current TLS-passthrough ingress on every machine, and later opt-in L7 east-west routing and HTTPS termination. DNS lives here, not in the agent, because name resolution is datapath: it must survive agent restarts (including every self-upgrade).

### Netproxy system deployment

- Auto-created per enrolled machine; internal-only spec variants (like `githubRelease`/`systemd` today), redacted from public create/update, not user-deletable.
- Image: the agent synthesizes a single-layer OCI image from its own release binary and imports it via the existing `ctrd.Import` path — no registry, no external build. The deployment version tracks the opendeploy release; agent upgrades bump it.
- Runs unprivileged in its own netns like any workload; reaches backends through ordinary logical routes like any workload. Public traffic arrives via `portForwarding` DNAT (no privileged ports; `net.ipv4.ip_unprivileged_port_start` is per-netns, so binding `:53` for DNS inside its namespace is a sysctl, not a capability).
- Upgrades use the same ROLLOVER route-flip lifecycle as application deployments. `portForwarding` continues to use stable inbound `I` for IPv6 and switches the current attachment's machine-local IPv4 target at promotion; `O` is not an ingress or discovery address.
- System deployments use a tight crash-backoff curve (not the standard 1–60s exponential), and the agent reattaches/respawns the netproxy deployment first on boot.
- Configuration interface: a single protobuf netstate file (routes, endpoint states, exposed domains, certificate material, ACME challenge tokens — defined in `api-contract`), written by the agent with atomic write-rename into the deployment's data volume and watched by the proxy via inotify. Full snapshots, no deltas or streams. The netproxy process is a pure state consumer: it self-configures from the file at start (agent may be down) and reacts to updates in milliseconds. Direct read access to the node's main database was rejected: it would expose secrets and cluster state to the internet-facing process, and would freeze internal storage schemas as a cross-version API between independently-upgrading processes; the netstate message uses the same protobuf evolution discipline as the rest of the API contract.
- ACME issuance runs centrally in the primary's agent (account keys, issuance locks, and certificates in primary storage). Challenge tokens and issued certificates are distributed to ingress machines as netstate content, so any ingress node can answer an HTTP-01 challenge and the proxy never writes state.
- Product visibility for free: status card, restart counts, standard log pipeline, deployment history for upgrades.

Failure behavior: agent down → routing, DNS, and ingress unaffected (containerd shims outlive both agent and containerd restarts); no topology changes until it returns. Netproxy container down → routing unaffected; DNS/ingress out on that machine until respawn. Accepted trade-off versus a systemd unit: after a machine reboot, or if the netproxy container crashes while the agent is hard-down, DNS/ingress on that machine wait for the agent to respawn them. A systemd-unit fallback remains the escape hatch if these windows prove painful in practice.

## Dataplane

### Per-machine composition

- One netns + veth pair per container (pure Go via `vishvananda/netlink`; no CNI).
- One fixed tunnel interface per remote node, configured from the local and remote nodes' reachable underlay addresses: `ip6tnl` for IPv6 or SIT for IPv4.
- No bridge. Plain host routes glue local logical `/128`s to veths and remote logical `/128`s to fixed node tunnels.
- An `unreachable` route for the cluster ULA `/48` prevents unknown workload addresses from escaping through the host's default route.
- The initial 1420 workload MTU leaves room for either a 40-byte outer IPv6 header or a 20-byte outer IPv4 header on a normal 1500-byte underlay.

### Fixed node tunnels

Each pair of nodes has matching fixed IPv6-in-IPv6 or IPv6-in-IPv4 interfaces. A tunnel stores endpoint and device configuration but no per-flow connection state. The kernel independently prepends or removes an outer IP header for every packet; the inner logical packet and its transport checksums never change.

The initial transport is unauthenticated and unencrypted. The underlay must provide mutually reachable same-family addresses and permit protocol 41. Source-address filtering does not provide cryptographic node identity. This tradeoff keeps the base dataplane minimal and is explicit in the product design.

### Cross-machine packet walk

Instance on machine A sends to an instance address hosted on machine B:

1. Container netns: default route via the host-side veth (link-local next hop); the packet crosses the veth pair.
2. Host A validates the logical source at the workload attachment. Local logical destinations route directly by `/128 → veth`.
3. For a remote destination, the logical `/128` route selects node B's fixed tunnel interface.
4. The kernel prepends an outer IPv4 or IPv6 header addressed from node A's underlay address to node B's and sends protocol 41 traffic through the physical network.
5. Node B's matching tunnel removes the outer header and reinjects the unchanged logical packet.
6. Host B verifies that the logical destination is locally placed, applies logical destination ingress policy, and routes it via the local `/128 → veth` route.

When an instance moves machines, its logical address does not change. Every informed node replaces that workload's `/128` route to select the new node tunnel. The tunnel interfaces themselves do not change. A stale source may temporarily send through the old node; if that node has the new route it can forward the unchanged inner packet one additional hop, otherwise the cluster-prefix fallback rejects it.

### Underlay endpoints and scale

Each machine reports one reachable underlay address to the primary. Address family is inferred from the address, and all nodes initially use the same family. NAT traversal and relaying are out of scope. The primary machine participates like every other node.

A cluster with `N` nodes creates `N - 1` fixed tunnel interfaces per node and `N * (N - 1)` interface objects cluster-wide. This is accepted for the initial limit of approximately 100 nodes. Before materially exceeding that limit, the transport must move to one flow-based tunnel per node or a bounded-degree gateway topology while retaining the same logical-address and placement APIs.

## Netmap distribution

The primary computes a per-machine netmap and pushes it over the existing mTLS cluster stream (same shape as secrets/config distribution).

- Workers reconcile node tunnels, placement routes, nftables filters, and DNS data idempotently. Full snapshots are acceptable for the initial approximately 100-node target; later scale requires incremental and sharded distribution.
- Filtered per machine: a machine only learns placements and addresses it legitimately needs.
- Persisted last-known-good: the worker stores the latest netmap in its local database and programs the dataplane from it on boot, before reaching the primary. Invariant: existing traffic flows do not depend on primary availability.

Netmap content (schema sketch):

```
NetMap {
  seq                      // monotonic per machine
  ula_prefix
  machines:  [{machine_id, underlay_ipv6}]
  deployments: [{deployment_id, space_id, name, environment,
                  endpoints: [{ordinal, inbound_address, machine_id, state}], // READY | DRAINING | DOWN
                  explicit_allowed_from: [deployment_id]}]
}
```

Endpoints are modeled as sets with per-endpoint state from day one, even while every set has exactly one member. Load balancing phases change only who consumes the set, never the schema. Space remains explicit metadata for validation and product display even though it is also encoded in the logical address.

Readiness generalizes the existing ROLLOVER readiness socket: signaling ready means membership in the endpoint set. Instances that are not ready do not resolve in DNS and are not routing targets. Removal from the set (`DRAINING`) happens before SIGTERM, with a drain window, so scale-downs and upgrades do not sever in-flight work.

## Service discovery (DNS)

Each machine runs a small built-in DNS server (`miekg/dns`) inside the netproxy system deployment (see Component model), serving records derived from the netmap; container resolv.conf points at that machine's netproxy instance address (replacing `WithHostResolvconf`). The netproxy container itself resolves via the host's upstream servers.

- `{name}.{environment}.internal` → AAAA records for READY instance addresses, low TTL, shuffled.
- `{ordinal}.{name}.{environment}.internal` → a specific instance.
- Names derive from `DeploymentIdentity`; underscores normalize to hyphens with collision validation at create time.
- Discovery is always on. It is not a feature enabled in the networking panel; a deployment has a name the moment it exists.

Unmatched queries are forwarded upstream, so public DNS keeps working unchanged.

## Ingress (reverse proxy)

An L7 proxy running on machines designated as ingress machines in settings (default: all machines once cross-node routing exists).

### Process model

The proxy runs inside the netproxy system deployment (see Component model): a containerd workload built from the opendeploy binary, not part of the agent process. The agent programs the proxy but does not contain it — opendeploy self-upgrades (which restart the agent on every release) never interrupt ingress traffic, and the internet-facing parser is isolated from cluster CA material, decrypted secrets, and DB access.

### Behavior

- Receives public IPv4 `:80`/`:443` traffic (and `::` where the machine has public IPv6) via `portForwarding` DNAT into its netns. The proxy is the address-family boundary: accept public v4, dial the backend's ULA instance address through logical host routing.
- Built on `net/http`/`httputil.ReverseProxy` with certmagic for automatic ACME issuance/renewal. v1 protocol scope: HTTPS termination, HTTP/1.1, HTTP/2, WebSockets, `X-Forwarded-*` headers.
- Routing state updates live from the netmap; no config-file reloads.
- Drain-aware: the proxy stops selecting DRAINING endpoints for new requests and finishes in-flight ones. During a single-instance promotion it holds new requests for the sub-second route flip and releases them to the new instance (zero failed requests).
- The proxy owns `:80`/`:443` on ingress machines. The OpenDeploy web UI is served as a route through the same proxy (unifying the existing web ACME config), and raw `portForwarding` claims on 80/443 are rejected on ingress machines.
- The same host-listener-to-logical-route pattern later supports raw TCP/UDP passthrough exposure (with PROXY protocol for source addresses).

### Route configuration and collision rules

Routes are configured only on deployments. Every kind owns a dedicated nested
configuration message. The initial implemented declaration is
`ingress: [{kind: TLS_PASSTHROUGH, hostname, tlsPassthroughConfig: {hostPort?, containerPort}}]`.
`hostPort` defaults to `443`; a passthrough claim is `(hostPort, hostname)`.
Future HTTPS and HTTP/3 kinds receive their own configuration messages rather
than sharing passthrough fields. HTTPS route claims will be `(hostname, pathPrefix)`.

- Exact duplicate claims across deployments are rejected at config save, naming the conflicting deployment. Validation is race-free because the primary is the single writer of all deployment configs.
- Overlapping prefixes on one domain are composition, not collision (`/` → frontend, `/api` → backend). Runtime routing is longest-prefix-match: deterministic, no priorities, no regex routes.
- Wildcard hosts (`*.example.com`) are a later increment (precedence rules, DNS-01 challenges); not v1.

### Multi-node ingress

With cross-node routing, every ingress machine can serve every route; public DNS holds one A record per ingress machine. DNS round-robin is the availability model: a dead node's record persists until TTL expiry, partially mitigated by client retry across A records. Floating IPs, VRRP, and managed-DNS health checks are out of scope (underlay concerns); users needing faster failover use their DNS provider's health checks.

Multi-node ingress requires shared certificate and ACME state: an HTTP-01 challenge may arrive at any node, and issuance must happen once. Issuance runs centrally in the primary's agent (certmagic with storage over primary storage; certificate material in the encrypted secrets store). Challenge tokens and issued certificates reach ingress machines through the netstate file (see Component model), so proxies serve challenges and TLS without owning any ACME state.

## Network policy

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

1. **DNS over ready endpoint sets** (ships with the netmap). Health-aware by construction: not-ready instances do not resolve. Known limits (client caching, long-lived connection pinning) are acceptable for internal traffic.
2. **Possible eBPF socket-level balancing** (`cgroup/connect6` hook via `cilium/ebpf`). A future service virtual address could be rewritten at `connect()` to a chosen READY `I`, per connection, before any packet exists. No service DNAT or conntrack would be required; ordinary logical routing would then select a local attachment or fixed node tunnel. This depends on a separate future service-address allocation and design because the current workload ABI allocates only `I` and `O`. Kata guests also require a different interception design.
3. **L7 east-west through the embedded proxy** (opt-in, per deployment): retries, traffic splitting, per-route metrics for HTTP workloads. Ingress traffic already gets endpoint-set balancing from stage 1.

Interim DNAT-based virtual IPs are explicitly rejected. No service virtual address exists until a separate allocation and implementation is designed.

Traffic policy (future, with daemon sets): per-deployment `trafficPolicy: spread | prefer-local | local-only`, resolved by DNS answer ordering and, in stage 2, by local-preference in the socket hook. Machine locality is already in the endpoint record.

## Upgrade rollover

Promotion of a new version on the same machine is a machine-local `I` route flip; the instance does not move machines and the primary is not on the critical path.

### Single instance (ROLLOVER)

1. Prepare the new image (unchanged).
2. Start the candidate in its own netns with preferred `O` routed to its veth and `I` preconfigured with `preferred_lft=0`. This is the same two-address setup used by every virtual run. The candidate binds its ports immediately (no host-port contention) and can warm up with outbound connections.
3. Candidate signals ready on the readiness socket.
4. Promote (machine-local, milliseconds): flip the `I` host route to the candidate's veth. Do not mutate candidate or old workload networking on the promotion critical path.
5. Mark old DRAINING, grace period, SIGTERM (existing stop flow).
6. Failure before ready: kill the candidate and remove its `I`/`O` attachment state. The old container never lost its `I` route.

Promotion does not alter either address or source preference. The promoted run
continues to prefer `O` for its complete lifetime; clients continue to use `I`.
Established TCP connections to the old container break at the flip; clients
reconnect to `I` and reach the new instance. Ingress HTTP sees zero failures via
the proxy's future hold-and-release behavior.

### Multiple instances (future)

Default rolling recreate, one ordinal at a time, behind the endpoint set:

1. Mark ordinal `i` DRAINING (drops out of DNS and balancing; the other n−1 instances carry load).
2. Drain window, SIGTERM old instance.
3. Start the new instance with both `I` and its run-scoped `O`, then activate `I`; the old container is already gone.
4. Wait for ready, mark READY, advance to the next ordinal.
5. A new version that never reaches ready halts the rollout with the remaining old instances serving. Halt-and-alert, no auto-rollback; rollback is a redeploy of the previous version.

Surge mode (no capacity dip) applies the single-instance candidate flow per ordinal — same primitive.

Cross-machine moves (scheduler relocating an ordinal) update the `I`-to-node placement map: start the new instance on the target with its preassigned `I` and preferred `O`, publish the `I` placement flip, then drain and stop the source. Every informed node replaces the `I` `/128` route to select the target's fixed tunnel; tunnel configuration does not change. The target `O` also needs publication so remote replies can route during startup. Propagation is eventually consistent. A stale route may reach the old node and take one additional routed tunnel hop after that node learns the new placement, or fail until the source receives the update. This is the one rollover variant where the primary is on the critical path, which is acceptable because scheduler moves are already control-plane acts.

Daemon sets are the rolling recreate loop iterated over machines; each replacement is machine-local.

## Observability (later)

eBPF flow metrics (who talks to whom, bytes, connect failures) as the first eBPF adoption: read-only, no correctness burden, feeds the planned resource-monitoring feature and a topology UI. Precedes any eBPF on the datapath.

## Configuration surface

Deployment config gains a `networking` section (side panel in the UI):

```
networking:
  portForwarding: [{protocol: TCP, hostPort: 443, containerPort: 443}] # raw host-interface DNAT
  ingress: [{
    kind: TLS_PASSTHROUGH,
    hostname: db.example.com,
    tlsPassthroughConfig: {hostPort: 443, containerPort: 5432},
  }]
  allowedFrom:    [other-deployment]                                      # cross-space allowlist; same-space is allowed by default
  # future: trafficPolicy, replica-related knobs
```

Settings (cluster-scoped): ingress machine designation. Install-time: nothing — the ULA prefix is auto-generated.

## Migration from host networking

Existing deployments keep host networking; each deployment adopts the virtual network on its next deploy. Deployments that were reachable via host ports must declare `portForwarding` (Phase 1) or `ingress` (Phase 3) at that point. The internal `OPENDEPLOY` self-deployment is unaffected (systemd runner, host process).

The networking mode is pinned to the config version, not the container lifetime: the primary stamps virtual-network mode into the spec at config-write time, and spec versions predating Phase 1 (no `networking` message) run host-network mode. Because the runner re-reads the immutable spec version on every respawn, a restart, crash respawn, or machine reboot never changes an existing deployment's networking — only writing a new config version does. Both launch paths therefore coexist in the runner; host-port collision validation and DNS records apply only to virtual-mode deployments. Host-mode deployments get a status-card nudge rather than a forced cutover.

### Host-network mode as a permanent option

Host networking remains a supported explicit opt-out (`networking: {mode: host}`), not just a legacy state. The launch path must exist indefinitely for never-redeployed deployments, so exposing it costs only validation and documentation. Genuine use cases: LAN discovery protocols that DNAT cannot forward (mDNS/SSDP — Home Assistant, Plex), network-infrastructure workloads that must see host interfaces (VPN/DHCP servers, monitoring agents, packet capture), and very wide or dynamic port ranges. A host-mode deployment forfeits the virtual-network guarantees: no stable instance address, no `.internal` DNS record, no `allowedFrom` policy, and no cross-node logical reachability. Host-mode ROLLOVER remains cooperative: the candidate must defer binding conflicting host ports until after readiness. Expected to be a niche opt-out, dominated by the multicast case.

## Implementation phases

Each phase is independently shippable and usable; later phases can wait.

### Phase 1 — Machine-local virtual network

The dataplane on each machine, without cross-machine routing yet.

- ULA prefix generation and persistence on the primary; derived `I` and `O` functions using space, deployment, ordinal, version slot, and run slot, with nonzero slot normalization.
- Netns/veth per container, host routes, IPv4 egress NAT, MTU handling.
- Attachment source anti-spoofing and generated default ingress policy for machine-local traffic: same-space allow, cross-space deny.
- Endpoint-set-shaped state (n=1) with READY/DRAINING, readiness generalized to set membership.
- Internal system deployment machinery: self-image synthesis from the release binary, auto-created per-machine netproxy deployment, tight crash backoff, agent socket interface, disk-persisted state.
- Per-machine DNS in the netproxy deployment: serves deployments local to that machine, forwards upstream.
- `portForwarding` publishing (nftables DNAT on the machine's host interfaces) to preserve external reachability.
- ROLLOVER promotion with preassigned `I` and `O`, an `I` route flip, and no source-preference mutation; wildcard-bind requirement and IPv4-only-listener diagnostic.
- `networking` section in the deployment config, proto schema, and UI side panel (`portForwarding`).
- Migration per the section above.

Usable outcome: containers are isolated with stable addresses, same-machine deployments in the same space discover and reach each other by name, cross-space traffic is denied by default, external reachability works via `portForwarding`, and rollover is genuinely zero-downtime. Cross-machine traffic continues via host addresses and published ports, as today.

### Phase 2 — Cross-node routing

- Reachable IPv4 or IPv6 underlay-address reporting and distribution through enrollment and the existing mTLS cluster channel.
- Fixed `ip6tnl` or SIT interface reconciliation for every remote node, with an initial cluster limit of approximately 100 nodes.
- Netmap computation, per-machine filtering, distribution over the cluster stream, worker-side persistence and idempotent reconciliation.
- Remote logical `/128` routes selecting fixed node tunnels, cluster-ULA `unreachable` fallback routes, cluster-wide DNS, and the same logical policy across machines.

Usable outcome: same-space instances reach same-space instances by name across machines, with the primary off the datapath. Cross-space traffic remains denied unless explicitly allowed. Base cross-node transport is not encrypted or cryptographically authenticated.

### Phase 3 — Ingress

Incremental within the phase:

- 3a: ingress role enabled in the existing netproxy system deployment (`portForwarding` 80/443 DNAT to its inbound address `I`), certmagic ACME (certmagic `Storage` over primary storage), `ingress` config with collision validation, longest-prefix routing to local-machine backends. Works with Phase 1 alone on single-machine clusters.
- 3b: cross-machine backends over logical tunnel routes (requires Phase 2); multi-node ingress with per-node public DNS records.
- 3c: rollover integration — drain awareness and hold-and-release during promotions; web UI served through the proxy.

Usable outcome: `ingress: {hostname}` gives a deployment a public HTTPS endpoint with automatic certificates; raw `portForwarding` becomes the exception rather than the norm.

### Phase 4 — Explicit policy and flow observability

- Explicit cross-space `allowedFrom` rules compiled to receiver-side nftables filters, distributed in the netmap.
- eBPF flow metrics (first eBPF adoption; read-only).

Usable outcome: deployment-to-deployment cross-space access control from the side panel; traffic visibility in the UI.

### Phase 5 — Multi-instance and load balancing

Coupled to the scheduler/replicas backlog item; networking consumes placements as `(deployment, ordinal) → machine` and never cares why.

- Endpoint sets with n > 1; rolling recreate and surge upgrade strategies; per-instance runner status/history keyed by `(deployment, ordinal)`.
- DNS multi-AAAA balancing (stage 1) arrives automatically.
- A separately allocated service virtual address design and a Kata-compatible balancing mechanism; eBPF `connect6` is one runc-specific option. Traffic policy for daemon sets remains part of this phase.
- L7 east-west through the proxy (stage 3), opt-in.

### Forward-compatibility notes for Phase 1

Decisions Phase 1 must honor so later phases are additive:

- Address functions implement only `I` and `O` in the clean workload ABI. Any future service virtual address requires a separate allocation and design rather than a compatibility encoding.
- Netmap/state schema uses endpoint sets with per-endpoint state from the start.
- The worker's reconciler is full-state and idempotent from the start, even while state is locally produced rather than primary-distributed.
- Status schema anticipates `(deployment, ordinal)` granularity (ordinal 0 today).
