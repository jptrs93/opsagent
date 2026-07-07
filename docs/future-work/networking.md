# Networking

Design for the built-in networking layer: per-workload addressing, cross-machine mesh, service discovery, ingress, network policy, and load balancing. This replaces host networking for containers and removes the need for separately installed networking components (CNI plugins, ingress controllers, cert managers, service mesh sidecars).

## Goals and principles

- Batteries included: one built-in network implementation, no plugin ecosystem. All components ship in the opendeploy binary; per machine there is the agent plus one dataplane system deployment (see Component model).
- The primary is control plane only. It computes and distributes networking state; it is never on the datapath. A primary outage degrades to "no topology changes", never "traffic stops".
- No NAT-based service translation (no kube-proxy equivalent). Workload addresses are stable and identity-bound; topology changes update routes, not NAT tables.
- Addresses are derived, not allocated. There is no IPAM state, allocator, or reuse policy.
- One logical workload network. Spaces are policy domains, not separate host-level virtual networks.
- Network policy is a default security boundary: same-space traffic is allowed, cross-space traffic is denied unless explicitly allowed, and workload source addresses are validated at the host attachment boundary.
- Configuration lives on the deployment config (a `networking` section edited in a side panel). Cluster-scoped knobs are limited to settings (ingress machines) and are validated cluster-wide.
- Boring, debuggable dataplane first (netlink, WireGuard, nftables — inspectable with `ip`, `wg`, `nft`). eBPF is a later optimization, never a prerequisite.

Reference designs: Tailscale (coordination server + cryptokey routing + netmap distribution + MagicDNS), Fly.io (containerd + WireGuard mesh + 6PN structured IPv6 addressing + built-in proxy).

## Addressing

### Virtual network

The virtual network is IPv6-only, using an RFC 4193 ULA `/48` prefix generated randomly at first primary startup and persisted. There is no install-time network configuration and no realistic collision with corporate networks, cloud VPCs, other OpenDeploy clusters, or Tailscale.

### Address layout

Addresses are pure functions of existing identifiers. Layout of the 80 host bits after the `/48` prefix:

```
<ULA /48> : <kind:16> : <deployment id:32> : <field:32>
```

| Kind | Meaning | Field |
|---|---|---|
| `0` | Instance address | instance ordinal (0-based) |
| `1` | Service address (virtual; per deployment) | `0` |
| `2` | Run-scoped temporary address (rollover candidates) | run number |
| `3` | Machine mesh address | machine id, deployment id bits zero |

Properties:

- An instance is `(deployment id, ordinal)`. Instance addresses are stable for the life of the instance and survive rescheduling to another machine.
- All addresses of one deployment share the `<ULA/48>:<kind>:<deployment id>` prefix, so policy, filtering, and capture rules match a whole deployment with one prefix.
- Service addresses (kind 1) are reserved from day one but not routed until socket-level load balancing exists (see Load balancing). Connecting to an unrouted service address fails cleanly.
- Deployment ids are never recycled, so blocks are never reused.

Instance limits are bounded only by the 32-bit fields; there are no practical caps on deployments, replicas, or daemon set size.

### Spaces and address derivation

Space id is not encoded in the v1 address layout. Same-space policy is compiled from endpoint state into nftables sets rather than inferred from address prefixes.

That keeps a deployment's logical address stable if it moves between spaces. If spaces later become hard tenant domains where address changes on space moves are acceptable, a per-space address prefix can be reconsidered for easier prefix rules and observability. Even then, prefix shape is only an optimization: attachment anti-spoofing and policy filters remain the security boundary.

### IPv4 egress

Containers keep a machine-local IPv4 path for internet egress: a fixed private range reused identically on every machine (not routable off-host), masqueraded to the host's default interface. IPv4 is never used for mesh, service, or ingress-to-backend traffic.

### Application requirements

- Workloads must listen on the IPv6 wildcard (`::`). Binding `0.0.0.0` only makes an app unreachable at its instance address (from peers and from ingress alike).
- Workloads must not bind to a specific address; rollover candidates can start with both the run address and the stable instance address configured, and promotion changes which attachment receives the stable-address route.

OpenDeploy detects IPv4-only listeners from the container netns (`/proc/net/tcp6` vs `tcp`) after readiness and surfaces a diagnostic on the status card. One failure mode, one diagnostic.

## Component model

Two distinct concerns with opposite operational profiles:

1. **Networking reconciler** (control plane): programs WireGuard peers, netns/veths, host routes, and nftables from the netmap. Privileged (NET_ADMIN), but its availability is irrelevant to traffic — the virtual network's dataplane is the kernel, and kernel state persists across restarts. There is no userspace daemon for the network itself.
2. **Userspace datapath services**: the DNS server and ingress proxy. Unprivileged, but process lifetime is traffic availability.

Layout per machine:

- **Agent** (existing opendeploy process, root): deployment operator plus the networking reconciler as an in-process subsystem. In-agent because there is no availability gain from separating it (kernel state persists; no new containers start while the agent is down), no privilege gain (the agent is already root), and veth setup / promotion route flips sequence synchronously with runner and operator code paths — a separate daemon would recreate the kubelet↔CNI RPC boundary this design avoids.
- **Dataplane system deployment**: an internal per-machine system deployment run via containerd, following the `OPENDEPLOY` internal-deployment pattern. It hosts the DNS server on every machine, the ingress proxy where the machine is ingress-designated, and later opt-in L7 east-west and TCP passthrough. DNS lives here, not in the agent, because name resolution is datapath: it must survive agent restarts (including every self-upgrade).

### Dataplane system deployment

- Auto-created per enrolled machine; internal-only spec variants (like `githubRelease`/`systemd` today), redacted from public create/update, not user-deletable.
- Image: the agent synthesizes a single-layer OCI image from its own release binary and imports it via the existing `ctrd.Import` path — no registry, no external build. The deployment version tracks the opendeploy release; agent upgrades bump it.
- Runs unprivileged in its own netns like any workload; reaches backends over the mesh like any workload. Public traffic arrives via `hostPorts` DNAT (no privileged ports; `net.ipv4.ip_unprivileged_port_start` is per-netns, so binding `:53` for DNS inside its namespace is a sysctl, not a capability).
- Upgrades use ROLLOVER with a refinement: `hostPorts` DNAT targets the container's kind-2 run address rather than the stable instance address. Rewriting the DNAT rule affects only new flows (established connections keep translating via their conntrack entries), so public connections drain gracefully on the old container while new ones land on the new — a cleaner handoff than listener-FD passing.
- System deployments use a tight crash-backoff curve (not the standard 1–60s exponential), and the agent reattaches/respawns the dataplane deployment first on boot.
- Configuration interface: a single protobuf netstate file (routes, endpoint states, exposed domains, certificate material, ACME challenge tokens — defined in `api-contract`), written by the agent with atomic write-rename into the deployment's data volume and watched by the proxy via inotify. Full snapshots, no deltas or streams. The dataplane process is a pure state consumer: it self-configures from the file at start (agent may be down) and reacts to updates in milliseconds. Direct read access to the node's main database was rejected: it would expose secrets and cluster state to the internet-facing process, and would freeze internal storage schemas as a cross-version API between independently-upgrading processes; the netstate message uses the same protobuf evolution discipline as the rest of the API contract.
- ACME issuance runs centrally in the primary's agent (account keys, issuance locks, and certificates in primary storage). Challenge tokens and issued certificates are distributed to ingress machines as netstate content, so any ingress node can answer an HTTP-01 challenge and the proxy never writes state.
- Product visibility for free: status card, restart counts, standard log pipeline, deployment history for upgrades.

Failure behavior: agent down → routing, DNS, and ingress unaffected (containerd shims outlive both agent and containerd restarts); no topology changes until it returns. Dataplane container down → routing unaffected; DNS/ingress out on that machine until respawn. Accepted trade-off versus a systemd unit: after a machine reboot, or if the dataplane container crashes while the agent is hard-down, DNS/ingress on that machine wait for the agent to respawn them. A systemd-unit fallback remains the escape hatch if these windows prove painful in practice.

## Dataplane

### Per-machine composition

- One `wg0` WireGuard device per machine holding the mesh (kernel WireGuard via `wgctrl`).
- One netns + veth pair per container (pure Go via `vishvananda/netlink`; no CNI).
- No bridge. Plain host routes glue the two: local instance addresses route `/128 → veth`; remote instance addresses are covered by `wg0` peer `allowed_ips`.
- MTU on veth and `wg0` accounts for WireGuard overhead (1420 on a 1500 underlay).

### Cryptokey routing is the service routing table

WireGuard `allowed_ips` maps each remote instance address to the hosting machine's public key. This table is the inter-machine routing table and a cross-machine source-ownership check: a remote machine cannot originate traffic from addresses not mapped to its key. It is not the workload network policy boundary and does not protect same-host traffic. Host attachment anti-spoofing and receiver-side nftables policy are still required. All cross-machine traffic is encrypted and machine-authenticated with zero per-service configuration — this covers the mTLS-in-transit value of a service mesh without sidecars.

### Cross-machine packet walk

Instance on machine A sends to an instance address hosted on machine B:

1. Container netns: default route via the host-side veth (link-local next hop); the packet crosses the veth pair.
2. Host A (IPv6 forwarding enabled): local instances match `/128 → veth` routes; everything else in the ULA space matches one route, `<ULA>/48 dev wg0`.
3. Kernel WireGuard looks the destination up in its `allowed_ips` trie. Machine B's peer entry contains the `/128`s of every instance B hosts plus B's kind-3 machine address. Match → encrypt to B's key → UDP to B's netmap endpoint. There is no separate inter-node routing protocol: `allowed_ips` is the inter-node routing table, programmed only by the netmap.
4. Machine B accepts the packet only if its source address is inside A's `allowed_ips` entry (cryptokey anti-spoofing), then routes it via the local `/128 → veth` route into the destination netns.

Host processes (ingress proxy, DNS, health checks) send over the same path using the machine's kind-3 address as source. When an instance moves machines, the netmap flips its `/128` between peer entries on every machine; that is the entire routing update.

### WireGuard availability

WireGuard is not installed or bundled; it is a kernel feature (mainline since 5.6, backported to Ubuntu 20.04's 5.4). OpenDeploy creates `wg0` via netlink (auto-loading the module) and configures keys/peers/`allowed_ips` via `wgctrl-go` generic netlink. The `wg` CLI is not required; it remains a debugging convenience. Install/enrollment runs a preflight check (create the link or probe the module) and fails with a clear error on kernels without WireGuard (e.g. RHEL 8 era). Userspace `wireguard-go` fallback is out of scope; kernel WireGuard is a hard requirement for Phase 2.

### Keys and endpoints

- Workers generate their WireGuard keypair locally during enrollment; the public key rides alongside the CSR. Private keys never leave the machine (same invariant as cluster TLS keys).
- Each machine reports its reachable endpoint(s) to the primary; the netmap carries peer endpoints as data. Machines must be directly reachable at this stage — NAT traversal (STUN/DERP-style relaying) is explicitly out of scope, but the endpoint-as-data shape leaves room for a relay later without redesign.
- The primary machine is itself a mesh peer.

## Netmap distribution

The primary computes a per-machine netmap and pushes it over the existing mTLS cluster stream (same shape as secrets/config distribution).

- Full-state snapshots, not incremental mutations. Workers reconcile wg peers, `allowed_ips`, host routes, nftables filters, and DNS data idempotently against the snapshot. No deltas unless scale ever demands them.
- Filtered per machine: a machine only learns peers and addresses it legitimately needs.
- Persisted last-known-good: the worker stores the latest netmap in its local database and programs the dataplane from it on boot, before reaching the primary. Invariant: existing traffic flows do not depend on primary availability.

Netmap content (schema sketch):

```
NetMap {
  seq                      // monotonic per machine
  ula_prefix
  machines:  [{machine_id, wg_public_key, endpoints, mesh_address}]
  services:  [{deployment_id, space_id, name, environment, service_address,
                endpoints: [{ordinal, address, machine_id, state}],   // state: READY | DRAINING | DOWN
                explicit_allowed_from: [deployment_id]}]
}
```

Endpoints are modeled as sets with per-endpoint state from day one, even while every set has exactly one member. Load balancing phases change only who consumes the set, never the schema. `space_id` is included so workers can compile generated same-space allow rules without deriving policy from address shape.

Readiness generalizes the existing ROLLOVER readiness socket: signaling ready means membership in the endpoint set. Instances that are not ready do not resolve in DNS and are not routing targets. Removal from the set (`DRAINING`) happens before SIGTERM, with a drain window, so scale-downs and upgrades do not sever in-flight work.

## Service discovery (DNS)

Each machine runs a small built-in DNS server (`miekg/dns`) inside the dataplane system deployment (see Component model), serving records derived from the netmap; container resolv.conf points at that machine's dataplane instance address (replacing `WithHostResolvconf`). The dataplane container itself resolves via the host's upstream servers.

- `{name}.{environment}.internal` → AAAA records for READY instance addresses, low TTL, shuffled.
- `{ordinal}.{name}.{environment}.internal` → a specific instance.
- Names derive from `DeploymentIdentifier`; underscores normalize to hyphens with collision validation at create time.
- Discovery is always on. It is not a feature enabled in the networking panel; a deployment has a name the moment it exists.

Unmatched queries are forwarded upstream, so public DNS keeps working unchanged.

## Ingress (reverse proxy)

An L7 proxy running on machines designated as ingress machines in settings (default: all machines once the mesh exists).

### Process model

The proxy runs inside the dataplane system deployment (see Component model): a containerd workload built from the opendeploy binary, not part of the agent process. The agent programs the proxy but does not contain it — opendeploy self-upgrades (which restart the agent on every release) never interrupt ingress traffic, and the internet-facing parser is isolated from cluster CA material, decrypted secrets, and DB access.

### Behavior

- Receives public IPv4 `:80`/`:443` traffic (and `::` where the machine has public IPv6) via `hostPorts` DNAT into its netns. The proxy is the address-family boundary: accept public v4, dial the backend's ULA instance address over the mesh.
- Built on `net/http`/`httputil.ReverseProxy` with certmagic for automatic ACME issuance/renewal. v1 protocol scope: HTTPS termination, HTTP/1.1, HTTP/2, WebSockets, `X-Forwarded-*` headers.
- Routing state updates live from the netmap; no config-file reloads.
- Drain-aware: the proxy stops selecting DRAINING endpoints for new requests and finishes in-flight ones. During a single-instance promotion it holds new requests for the sub-second route flip and releases them to the new instance (zero failed requests).
- The proxy owns `:80`/`:443` on ingress machines. The OpenDeploy web UI is served as a route through the same proxy (unifying the existing web ACME config), and `hostPorts` claims on 80/443 are rejected on ingress machines.
- The same host-listener-to-mesh pattern later supports raw TCP/UDP passthrough exposure (with PROXY protocol for source addresses).

### Route configuration and collision rules

Routes are configured only on deployments: `expose: [{domain, port, pathPrefix?}]`. A claim is `(domain, pathPrefix)`.

- Exact duplicate claims across deployments are rejected at config save, naming the conflicting deployment. Validation is race-free because the primary is the single writer of all deployment configs.
- Overlapping prefixes on one domain are composition, not collision (`/` → frontend, `/api` → backend). Runtime routing is longest-prefix-match: deterministic, no priorities, no regex routes.
- Wildcard hosts (`*.example.com`) are a later increment (precedence rules, DNS-01 challenges); not v1.

### Multi-node ingress

With the mesh, every ingress machine can serve every route; public DNS holds one A record per ingress machine. DNS round-robin is the availability model: a dead node's record persists until TTL expiry, partially mitigated by client retry across A records. Floating IPs, VRRP, and managed-DNS health checks are out of scope (underlay concerns); users needing faster failover use their DNS provider's health checks.

Multi-node ingress requires shared certificate and ACME state: an HTTP-01 challenge may arrive at any node, and issuance must happen once. Issuance runs centrally in the primary's agent (certmagic with storage over primary storage; certificate material in the encrypted secrets store). Challenge tokens and issued certificates reach ingress machines through the netstate file (see Component model), so proxies serve challenges and TLS without owning any ACME state.

## Network policy

OpenDeploy has one logical workload network. Space isolation is policy, not separate VRFs, per-space WireGuard devices, or per-space ULA prefixes.

Default stance:

- Deny workload-to-workload traffic by default, then add generated allow rules.
- Allow same-space workload-to-workload traffic on all protocols and ports.
- Deny cross-space workload-to-workload traffic unless an explicit policy allows it.
- Allow internet egress by default through the machine-local egress path.
- Allow OpenDeploy system paths explicitly rather than relying on cluster-open defaults.

Enforcement has two mandatory layers:

1. **Source anti-spoofing at the workload attachment boundary.** Packets arriving from a workload veth/TAP may only use source addresses assigned to that workload. During rollover, the allowed source set includes the active stable address, the run address, and any configured deprecated stable address for the candidate. This is required because policy trusts packet source identity; the fact that replies to spoofed traffic usually return elsewhere is not enough for UDP, QUIC Initials, audit correctness, quota, or privileged-source allowlists.
2. **Destination ingress policy before delivery to a workload attachment.** Packets about to enter a workload veth/TAP are dropped unless the source workload is in the same space, an explicit cross-space policy allows the source, or the traffic is an OpenDeploy system allow.

For workload-to-workload isolation, destination ingress policy plus source anti-spoofing is sufficient. A separate workload egress policy is not required for the v1 default boundary. Egress policy remains useful later for internet/private-network controls, host-service access, control-plane protection, and exfiltration-sensitive deployments.

Policies are expressed in logical workload terms: space, deployment, labels later, protocol, and port. They compile to nftables sets/maps distributed as part of the netmap and applied with full-state atomic reconciliation. Same-host and cross-host traffic hit the same destination-side policy; WireGuard only authenticates the remote machine and source prefix for cross-host packets.

If machine-scoped locator addresses are added later, policy still evaluates logical source and destination addresses before outbound translation and after inbound reverse translation. Locator addresses are routing state, not policy identity.

## Load balancing

kube-proxy exists to translate stable virtual addresses to ephemeral endpoints. Stable instance addresses remove the need. Balancing arrives in stages; users only ever set a replica count.

1. **DNS over ready endpoint sets** (ships with the netmap). Health-aware by construction: not-ready instances do not resolve. Known limits (client caching, long-lived connection pinning) are acceptable for internal traffic.
2. **eBPF socket-level balancing** (`cgroup/connect6` hook via `cilium/ebpf`). Rewrites `connect()` calls to the kind-1 service address into a chosen READY instance address, per connection, before any packet exists. No DNAT, no conntrack; wire packets always carry real instance addresses, so cryptokey routing is undisturbed. The hook's backend map is programmed from the same netmap. This is when service addresses start routing.
3. **L7 east-west through the embedded proxy** (opt-in, per deployment): retries, traffic splitting, per-route metrics for HTTP workloads. Ingress traffic already gets endpoint-set balancing from stage 1.

Interim DNAT-based virtual IPs are explicitly rejected; before stage 2, service addresses simply do not route.

Traffic policy (future, with daemon sets): per-deployment `trafficPolicy: spread | prefer-local | local-only`, resolved by DNS answer ordering and, in stage 2, by local-preference in the socket hook. Machine locality is already in the endpoint record.

## Upgrade rollover

Promotion of a new version on the same machine is a machine-local route flip; the instance address never moves machines and the primary is not on the critical path.

### Single instance (ROLLOVER)

1. Prepare the new image (unchanged).
2. Start the candidate in its own netns with a kind-2 run-scoped temporary address routed to its veth. Also preconfigure the stable instance address as a deprecated/non-preferred address before workload start, so the candidate can receive stable-address traffic after promotion without choosing it as the warmup source address. The candidate binds its ports immediately (no host-port contention) and can warm up with outbound connections.
3. Candidate signals ready on the readiness socket.
4. Promote (machine-local, milliseconds): flip the stable-address host route to the candidate's veth. Do not mutate candidate or old workload networking on the promotion critical path.
5. Mark old DRAINING, grace period, SIGTERM (existing stop flow).
6. Failure before ready: kill the candidate, remove the temporary address. The old container never lost its address or route.

Established TCP connections to the old container break at the flip; clients reconnect to the same address and reach the new instance. Ingress HTTP sees zero failures via the proxy's hold-and-release. The current "signal ready then wait for the port to free" behavior is obsolete.

### Multiple instances (future)

Default rolling recreate, one ordinal at a time, behind the endpoint set:

1. Mark ordinal `i` DRAINING (drops out of DNS and balancing; the other n−1 instances carry load).
2. Drain window, SIGTERM old instance.
3. Start the new instance; it takes the stable ordinal address directly (no temporary-address dance — the old container is gone).
4. Wait for ready, mark READY, advance to the next ordinal.
5. A new version that never reaches ready halts the rollout with the remaining old instances serving. Halt-and-alert, no auto-rollback; rollback is a redeploy of the previous version.

Surge mode (no capacity dip) applies the single-instance candidate flow per ordinal — same primitive.

Cross-machine moves (scheduler relocating an ordinal) flip the address's `allowed_ips` mapping cluster-wide via a netmap update: new instance ready on the target machine under a temporary address, publish the netmap flip, drain/stop on the source. Propagation is eventually consistent; a sub-second stale-peer window is absorbed by TCP retransmission. This is the one rollover variant where the primary is on the critical path, which is acceptable because scheduler moves are already control-plane acts.

Daemon sets are the rolling recreate loop iterated over machines; each replacement is machine-local.

## Observability (later)

eBPF flow metrics (who talks to whom, bytes, connect failures) as the first eBPF adoption: read-only, no correctness burden, feeds the planned resource-monitoring feature and a topology UI. Precedes any eBPF on the datapath.

## Configuration surface

Deployment config gains a `networking` section (side panel in the UI):

```
networking:
  ports:       [{name: http, port: 8080}]                 # named container ports
  hostPorts:   [{host: 443, port: http}]                  # publish on the machine's host interfaces
  expose:      [{domain: api.example.com, port: http}]    # ingress; TLS automatic
  allowedFrom: [other-deployment]                         # cross-space allowlist; same-space is allowed by default
  # future: trafficPolicy, replica-related knobs
```

Settings (cluster-scoped): ingress machine designation. Install-time: nothing — the ULA prefix is auto-generated.

## Migration from host networking

Existing deployments keep host networking; each deployment adopts the virtual network on its next deploy. Deployments that were reachable via host ports must declare `hostPorts` (Phase 1) or `expose` (Phase 3) at that point. The internal `OPENDEPLOY` self-deployment is unaffected (systemd runner, host process).

The networking mode is pinned to the config version, not the container lifetime: the primary stamps virtual-network mode into the spec at config-write time, and spec versions predating Phase 1 (no `networking` message) run host-network mode. Because the runner re-reads the immutable spec version on every respawn, a restart, crash respawn, or machine reboot never changes an existing deployment's networking — only writing a new config version does. Both launch paths therefore coexist in the runner; host-port collision validation and DNS records apply only to virtual-mode deployments. Host-mode deployments get a status-card nudge rather than a forced cutover.

### Host-network mode as a permanent option

Host networking remains a supported explicit opt-out (`networking: {mode: host}`), not just a legacy state. The launch path must exist indefinitely for never-redeployed deployments, so exposing it costs only validation and documentation. Genuine use cases: LAN discovery protocols that DNAT cannot forward (mDNS/SSDP — Home Assistant, Plex), network-infrastructure workloads that must see host interfaces (VPN/DHCP servers, monitoring agents, packet capture), and very wide or dynamic port ranges. A host-mode deployment forfeits the virtual-network guarantees: no stable instance address, no `.internal` DNS record, no `allowedFrom` policy, no mesh reachability, and no ROLLOVER (port contention returns; RECREATE only). Expected to be a niche opt-out, dominated by the multicast case.

## Implementation phases

Each phase is independently shippable and usable; later phases can wait.

### Phase 1 — Machine-local virtual network

The dataplane on each machine, no cross-machine mesh yet.

- ULA prefix generation and persistence on the primary; derived address functions (kinds 0–3).
- Netns/veth per container, host routes, IPv4 egress NAT, MTU handling.
- Attachment source anti-spoofing and generated default ingress policy for machine-local traffic: same-space allow, cross-space deny.
- Endpoint-set-shaped state (n=1) with READY/DRAINING, readiness generalized to set membership.
- Internal system deployment machinery: self-image synthesis from the release binary, auto-created per-machine dataplane deployment, tight crash backoff, agent socket interface, disk-persisted state.
- Per-machine DNS in the dataplane deployment: serves deployments local to that machine, forwards upstream.
- `hostPorts` publishing (nftables DNAT on the machine's host interfaces) to preserve external reachability.
- New ROLLOVER promotion (temporary candidate address, route flip); wildcard-bind requirement and IPv4-only-listener diagnostic.
- `networking` section in the deployment config, proto schema, and UI side panel (`ports`, `hostPorts`).
- Migration per the section above.

Usable outcome: containers are isolated with stable addresses, same-machine deployments in the same space discover and reach each other by name, cross-space traffic is denied by default, external reachability works via `hostPorts`, and rollover is genuinely zero-downtime. Cross-machine traffic continues via host addresses and published ports, as today.

### Phase 2 — Cluster mesh

- WireGuard keypairs in enrollment (existing machines: key exchange over the established mTLS cluster channel); `wg0` full mesh; machine endpoint reporting.
- Netmap computation, per-machine filtering, distribution over the cluster stream, worker-side persistence and idempotent reconciliation.
- Cross-machine instance routing via `allowed_ips`; cluster-wide DNS; the same default policy applies across machines.

Usable outcome: same-space instances reach same-space instances by name across machines, encrypted, with the primary off the datapath. Cross-space traffic remains denied unless explicitly allowed.

### Phase 3 — Ingress

Incremental within the phase:

- 3a: ingress role enabled in the existing dataplane system deployment (hostPorts 80/443 DNAT to its run address), certmagic ACME (certmagic `Storage` over primary storage), `expose` config with collision validation, longest-prefix routing to local-machine backends. Works with Phase 1 alone on single-machine clusters.
- 3b: cross-machine backends over the mesh (requires Phase 2); multi-node ingress with per-node public DNS records.
- 3c: rollover integration — drain awareness and hold-and-release during promotions; web UI served through the proxy.

Usable outcome: `expose: {domain}` gives a deployment a public HTTPS endpoint with automatic certificates; `hostPorts` becomes the exception rather than the norm.

### Phase 4 — Explicit policy and flow observability

- Explicit cross-space `allowedFrom` rules compiled to receiver-side nftables filters, distributed in the netmap.
- eBPF flow metrics (first eBPF adoption; read-only).

Usable outcome: deployment-to-deployment cross-space access control from the side panel; traffic visibility in the UI.

### Phase 5 — Multi-instance and load balancing

Coupled to the scheduler/replicas backlog item; networking consumes placements as `(deployment, ordinal) → machine` and never cares why.

- Endpoint sets with n > 1; rolling recreate and surge upgrade strategies; per-instance runner status/history keyed by `(deployment, ordinal)`.
- DNS multi-AAAA balancing (stage 1) arrives automatically.
- eBPF `connect6` socket balancing for service addresses (stage 2); traffic policy for daemon sets.
- L7 east-west through the proxy (stage 3), opt-in.

### Forward-compatibility notes for Phase 1

Decisions Phase 1 must honor so later phases are additive:

- Address functions include kinds 1–3 even though only kind 0 and 2 route in Phase 1.
- Netmap/state schema uses endpoint sets with per-endpoint state from the start.
- The worker's reconciler is full-state and idempotent from the start, even while state is locally produced rather than primary-distributed.
- Status schema anticipates `(deployment, ordinal)` granularity (ordinal 0 today).
