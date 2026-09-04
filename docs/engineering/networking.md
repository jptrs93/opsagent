# Networking

## Overview

OpenDeploy implements virtual networking for container deployments in-process in the agent, with the Linux kernel as the dataplane. All nodes, including the primary, reconcile the WireGuard node transport and remote workload routes from the cluster network map, and enforce the logical network policy boundary (source anti-spoofing and destination-side default rules plus explicit override policies) through nftables filter chains. The cluster map also carries the cluster-wide DNS catalog, so every node resolves every `.internal` service.

## Current Scope

Implemented today:

- A cluster-wide RFC 4193 ULA `/48` prefix is generated once on the primary and persisted in config.
- Workers receive the ULA prefix over the existing primary-to-worker cluster stream and cache it locally.
- Virtual-mode containers run in a dedicated Linux network namespace with a veth pair.
- Each virtual-mode container run receives a stable inbound IPv6 address `I`, a run-scoped preferred outbound IPv6 address `O`, and a machine-local IPv4 address for egress.
- The host routes `O` to the run for its full lifetime and routes `I` only to the current run.
- OpenDeploy-owned host routes use a dedicated route-protocol tag, and an `unreachable` cluster ULA `/48` route prevents unknown logical destinations from reaching the host default route.
- Each node records one underlay address. The primary uses `--underlay-address` or derives it from its cluster listener; a worker uses the flag or derives the source address selected for its primary cluster connection and includes it in enrollment.
- The primary renders deterministic full network maps containing node underlay addresses and virtual-workload routes. Each published map is stamped with `derived_from_seq`, the global write sequence its inputs reflect; the map itself is derived state, never persisted, and re-rendered on every boot. Updates are coalesced for connected workers and a targeted map is included in enrollment when publication succeeds.
- Cluster routes are prefixes, not addresses, and are derived from scheduled instance assignments alone. No runner status is read, so container restarts and crashes never move a route or republish the map.
- Workers validate and persist accepted maps before exposing their prefix to runtime networking. Acceptance is session-based: the first map accepted after connect replaces the cache unconditionally, later in-session maps must carry a higher stamp. Wrong targets, mixed underlay families, and invalid route layouts are rejected; cached maps survive primary outages and agent restarts.
- Cross-node traffic rides a single managed WireGuard device per node (`odwg0`, alias `opendeploy:wg`, MTU 1420, fixed listen port 51833). Each node mints a static Curve25519 keypair at first boot (`<DataDir>/wireguard.key`, 0600); the private key never leaves the machine, and the public key is registered on the node row — carried in enrollment and re-reported in every cluster hello. A key that fails to load or parse blocks boot: WireGuard is the only cross-node transport, and every member node must be keyed (the map renderer, worker map validation, and topology conversion all hard-error on a keyless node).
- Workers reconcile the WireGuard device, one peer per remote node, and remote routed prefixes from accepted maps, then report the applied stamp. Each peer's allowed-ips are exactly the routed prefixes the map assigns to that node, so cryptokey routing enforces node-level source attribution: the kernel drops decrypted packets whose source lies outside the sending node's routed prefixes.
- The primary applies its own targeted map through an in-process applier: it subscribes directly to the publisher, reconciles the same WireGuard peers and remote routes as workers, retries failed reconciliation on a timer (there is no session reconnect to redeliver a map in-process), and records its applied stamp into the same barrier only after a successful kernel apply. It never persists maps — the primary re-renders from its database on every boot.
- The primary consumes those reports as a barrier: a superseded placement keeps running, and keeps its routes, until every node holding network state has applied the map that replaced it.
- IPv4 egress is masqueraded from a fixed machine-local private range.
- `portForwarding` publishes virtual-mode container TCP/UDP ports through nftables DNAT on the machine's host interfaces, optionally restricted to an allow list of source IPs/CIDRs.
- ROLLOVER in virtual mode starts a candidate with both addresses and promotes it by flipping the stable inbound-address host route. Promotion does not change source-address preference: `O` remains preferred for the promoted run's full lifetime.
- A placement claims the stable inbound address only when its target state is `RUN_SERVING`, so a replacement warming up on another node cannot hold the same address as the placement it is replacing.
- Endpoints are derived, not reported, and health-free. A placement's inbound address is a pure function of its space, deployment, and ordinal, and it is published exactly when the placement is target-`RUN_SERVING` — the same condition that emits the instance's `/100` route — so DNS and ingress never read runner status. A crashed workload keeps its records; health becomes a load-balancing concern (see `docs/future-work/service-balancing-and-attachment-nat.md`), not a naming one.
- The space in that function is the placement's own pin, captured at scheduling time as a space snapshot on the instance's event rows (`scheduled_instance_event_log.space_id`) and overlaid onto the config identity every derivation reads. A deployment space move therefore never mutates live network state: the scheduler replaces placements through the rollover path, and old-space and new-space placements coexist (each self-consistently addressed, named, and certified for its own space) until the flip.
- A per-machine internal netproxy deployment runs the built-in DNS process and answers `.internal` AAAA records for the whole cluster from the map's DNS catalog.
- Virtual-mode deployments can declare `TLS_PASSTHROUGH` and `HTTPS` ingress routes. The local agent renders them and their catalog-derived IPv6 backends into `netstate.pb`, derives netproxy TCP forwarding from their host-port set, and netproxy forwards TLS streams by SNI without termination or terminates HTTPS itself (see Ingress Shape).
- The logical network policy boundary: per-attachment source anti-spoofing, destination-side default connectivity rules (same-space, global-space, DNS, `_system` egress paths), explicit cross-space override policies stored as global entities and distributed via the cluster network map, and the machine-local IPv4 close (see Network Policy).

Not implemented yet:

- A separate address allocation and design for future service virtual addresses, plus socket-level load balancing. The workload address ABI allocates only `I` and `O`.
- Cross-machine `ingress` route definitions: ingress backends come from the cluster catalog and may live on any node, but the routes themselves (and their host ports) are still rendered only on nodes holding a placement of the declaring deployment.

## Configuration

Deployment networking is configured on `DeploymentSpec.networking`.

- `NETWORKING_MODE_HOST` runs the container in the host network namespace.
- `NETWORKING_MODE_VIRTUAL` runs the container in a managed network namespace.
- `NETWORKING_MODE_UNSPECIFIED` is invalid for create/update requests. Deployment specs must set an explicit mode.

`networking.portForwarding` maps one host-interface TCP or UDP port to one container port and requires virtual mode. TCP and UDP claims are independent, so the same numeric host port can be published once for TCP and once for UDP.

Each port forward can carry an `ipFilter` restricting which sources are
forwarded. `ipFilter.allow` is a list of source IPs or CIDR prefixes, IPv4 or
IPv6; entries are validated at config save (no zones, no IPv4-mapped forms, no
host bits set) and normalized to canonical form. An unset filter or empty
allow list forwards from anywhere. A non-empty allow list applies per address
family: only matching sources get the prerouting DNAT rule — one nftables rule
per allowed prefix — and a family with no allow entries publishes nothing on
that family, so an IPv4-only allow list closes the port's IPv6 side.
Non-allowed traffic is simply never DNATed and sees the port as if the forward
did not exist. The filter applies to external inbound traffic only: the
output-chain rule for host-local clients stays unconditional. Because DNAT is
flow-scoped, tightening a filter does not terminate already-established
connections, the same as removing a forward entirely. `ipFilter.deny` exists
in the schema (deny is defined to win over allow) but is not implemented and
is rejected on config writes, so a restriction can never be saved silently
unenforced. In the mixed-version window an older agent decodes the spec but
ignores `ipFilter` and publishes the port unfiltered — upgrade agents before
relying on a filter. The HCL form is
`port_forward("tcp", 5432, { allow = ["203.0.113.7", "198.51.100.0/24"] })`.

`networking.ingress` also requires virtual mode. The supported kinds are
`TLS_PASSTHROUGH` and `HTTPS` (see Ingress Shape below). A `TLS_PASSTHROUGH`
route carries a hostname and a `tlsPassthroughConfig` containing
`containerPort` plus optional `hostPort` (zero/default is `443`). The route and
raw TCP forwarding cannot claim the same host port on a node; multiple distinct
hostnames can share one ingress host port. Netproxy reads the TLS ClientHello to
match SNI, then forwards the original TCP stream to an established backend. The primary
node reserves `:443` for the Web UI until both can share one listener.

Virtual networking supports ROLLOVER without host-port bind contention because candidate containers bind inside their own network namespace. Host networking also supports ROLLOVER, but it is cooperative: the candidate must not bind conflicting host ports before signaling readiness, then waits for the old runner to stop and release the port.

## Addressing

Addresses are pure functions of the ULA prefix, space, deployment, ordinal, version slot, and run slot. There is no workload IPAM allocator.

The clean, breaking IPv6 address ABI is:

```text
<ULA:48><space:16><deployment:24><ordinal:12><placementSlot:20><runSlot:8>
```

This layout replaces the prior address ABI outright. Old-layout addresses are
invalid; there is no compatibility decoder, migration route, or dual-layout
operation.

The placement slot identifies one **scheduled instance** — the placement
incarnation that owns a node assignment — not a spec version. Two live
placements can share a spec version, which happens whenever an instance moves
node without a version change, and a version-keyed slot would give them the same
address and the same route. The scheduled instance id is exactly the identity a
route must be keyed on, so it is what the slot is derived from.

For a cluster prefix `prefix` and instance `(space, deployment, ordinal)`:

```text
I = Address(prefix, space, deployment, ordinal, 0, 0)
O = Address(prefix, space, deployment, ordinal, placementSlot, runSlot)
```

`I` is the stable inbound address. DNS, endpoint state, ingress backends, typed
Address environment references, and direct clients use `I`. `O` is the
run-scoped preferred outbound source address for the full life of every virtual
container run, including a rollover candidate after promotion. Every run has
both addresses before its process starts: `I` is assigned with
`preferred_lft=0`, while `O` is preferred. Setup installs the host route for
`O`; initial activation or rollover promotion installs or flips the host route
for `I` to the current run.

Full deployment spec versions and run numbers remain full-width in desired
state, status, logs, and container IDs. Only their address slots are normalized
to nonzero values:

```text
slot(n, max)  = ((n - 1) % max) + 1
placementSlot = slot(scheduledInstanceID, 2^20 - 1)
runSlot       = slot(runNumber, 2^8 - 1)
```

The pair `(0, 0)` is valid only for `I`; `O` requires both slots to be nonzero.
The implementation rejects a live outbound-address collision rather than
allowing two concurrent runs of one deployment to share `O` after slot wrap.

The resulting logical prefix hierarchy is:

- Cluster: `/48`.
- Space: `/64`.
- Deployment: `/88`.
- Instance `(deployment, ordinal)`: `/100`.
- Placement `(instance, scheduled instance)`: `/120`.
- Individual address: `/128`.

The hard address-layout capacities are 65,536 spaces, 16,777,216 deployment
field values with deployment `0` invalid, 4,096 ordinals, 1,048,575 nonzero
placement slots, and 255 nonzero run slots. Ordinals do not wrap. Placement and
run slots wrap only through the normalization above; the raw values do not wrap.
A placement-slot collision requires 1,048,575 intervening scheduled instances
between two placements of one ordinal that are live at the same time;
`SetupContainerNet` rejects a live outbound collision rather than allowing two
runs to share `O`.

Space is part of logical network identity. Moving a deployment to another space changes its inbound and outbound addresses and is therefore a connection-breaking security-domain migration. Moving an instance between nodes does not change `I`.

## Runtime Wiring

For virtual-mode containers, the runner asks `backend/lib/network` to create network state before starting the containerd task. The network manager creates the named netns, veth pair, container-side addresses, default routes, host-side gateway addresses, and host route. The containerd wrapper joins the pre-created network namespace through the OCI spec. Host veth names are deterministic deployment slots: `od<deploymentID>s0` for the first live network and `od<deploymentID>s1` for a concurrent rollover candidate.

When an agent restarts, containerd tasks and their network namespaces can remain running. Reattachment opens the surviving named namespace and identifies its deterministic host veth by the mutual peer indexes of namespace `eth0` and the host link. Veth aliases are not used for ownership. Recovery restores the run's `O` route and, for the current run, its `I` route, then records the host ifindex before removing unretained slots, so a current task is never deleted as stale and delayed teardown cannot delete a link whose name has since been reused. A task on the current config republishes its host-port state; the internal netproxy also republishes across its version-only upgrades. Older application tasks retain recovered metadata for safe teardown but wait for their prepared replacement before using a newer networking config. If required reconstruction fails, the adopted task is replaced rather than left running without its forwarding rules.

Kernel state is written event-wise and assumed to persist, so `backend/lib/netaudit` audits it every 60 seconds: it compares the manager's desired DNAT/masquerade rules, filter rules, set/map elements, and `/128` workload routes against the live nftables ruleset and protocol-200 route table, rechecking once after 2 seconds before reporting. It is strictly log-only (`netaudit: kernel network state in sync` / `diverged`) — divergence is evidence of a bug or external interference and must stay visible, not be silently repaired.

The map is a pure derivation and is not persisted on the primary. Every render
reads its inputs and the global write sequence in one storage critical section
and stamps the result with `derived_from_seq`; every map-input write — node
registry changes included — advances that sequence in its own transaction, so
the stamp identifies exactly which state a map reflects. A new map is published
only when deterministically rendered node or route content changes; a render
that changes nothing keeps the published map and its older stamp.

Routes are derived from scheduled instance assignments and nothing else. Every
live placement contributes the `/120` covering its runs, pointed at its own
node, for the placement's whole life. The placement whose target state is
`RUN_SERVING` additionally gives its node the `/100` covering the instance,
which is what carries the stable inbound address `I`. Longest-prefix match then
sends each placement's reply traffic to the node running it while `I` follows
the serving placement. Because run numbers live below the `/120` boundary, a
container restart is invisible cluster-wide.

Two routed prefix lengths are legal — `/100` and `/120`. Workers reject anything
else: a `/128` would pin one run, and a deployment-wide prefix would send every
ordinal to a single node. Local workload routes remain `/128` and always win
over a routed prefix, so a map that lags a placement arriving on this node
cannot divert its traffic.

Session delivery clones the map with the mTLS-authenticated node ID and uses a
capacity-one latest-value channel. Worker acceptance is session-based: the
snapshot served at connect replaces the persisted map unconditionally — a
primary restored from backup republishes with a rolled-back counter, and its
snapshot is authoritative even so — while later in-session maps must carry a
higher stamp. That unconditional acceptance is scoped by the cluster mTLS
material alone: the worker only reaches a primary holding a certificate signed
by the CA it enrolled under, and the per-install CA is the cluster's identity.
`NetMapStatus` reports durable acceptance and the stamp successfully applied to
the worker kernel; the primary tracks those reports per connected node and uses
them as the barrier described under Rollover: a drain decision is in force once
a render has seen the decision's own write sequence and every reporting node
has applied the current map's stamp. Only a clean apply counts, and a
disconnected node stops holding the barrier because it is served a complete
snapshot when it returns.

Each node gets an `opendeploy-net` deployment when it is first created or enrolled, initially using the primary release available at that time. Agent upgrades and primary restarts do not change an existing netproxy's desired version or running state. Administrators update netproxy deployments explicitly and are responsible for selecting versions compatible with the agents and rendered netstate format.

The container receives a generated `resolv.conf` pointing at the machine-local netproxy DNS address once the netproxy deployment id is known. The netproxy deployment itself uses the host resolver to avoid a DNS dependency cycle.

## Network Policy

The logical policy boundary is enforced with a fixed nftables rule program
whose per-workload variability lives entirely in named sets and a verdict map,
applied in the same atomic netlink transaction as the NAT rules — there is
never a window with DNAT or routes but no policy. The forward-chain policy is
`accept`; enforcement is explicit rules scoped to opendeploy veths and the
cluster `/48`, so host traffic, host firewalls, and non-cluster flows are
untouched.

The static skeleton (both `opendeploy` tables, their base chains, the empty
sets, the masquerade rule, and the fixed forward-chain program) is recreated on
the first reconcile after agent start and persists for the process lifetime, so
the counters on its drop rules survive steady-state reconciles. The ip6 forward
chain is: conntrack `established,related` accept; `oifname @blocked_out`
counted drop; `iifname @managed iifname . ip6 saddr != @src_ok` counted drop;
`ip6 daddr vmap @dst_dispatch`. The anti-spoofing root of trust for every
address-based rule is the `src_ok` concatenated set holding each attachment's
`(veth, I)` and `(veth, O)` pairs — a packet entering from a managed veth with
any other source drops. `dst_dispatch` maps each locally attached workload
address (`I` and `O` `/128`s, mirroring the local workload routes) to a jump
into that deployment's `wl_dst_<deploymentID>` chain.

The default rules are implicit system intent, written directly into the render
(`RenderFilterState` in `backend/lib/network/filter.go`) and never distributed.
Per locally attached destination deployment `D` in space `S`, the
`wl_dst_<deploymentID>` chain evaluates: same-space `/64` accept, `_system`
(space 0) `/64` accept, DNS `udp/53` + `tcp/53` accept on the netproxy
deployment's own chain only, explicit override rules, then a counted drop of
any remaining cluster-`/48` source. A global-space (space 1) destination
accepts every cluster source instead. Non-cluster sources fall through and are
accepted: externally DNAT'd `portForwarding` and terminated ingress traffic are
governed by their own publication mechanisms. Egress stays open. An attachment
whose address identity cannot be decoded fails closed: it joins `managed` with
no `src_ok` pairs (all its ingress drops) and joins `blocked_out` (all egress
toward it drops). The machine-local IPv4 path is closed the same way with the
ip-table `managed`/`src_ok` sets: a drop of any traffic from a workload veth
into the machine-local v4 range plus per-attachment v4 anti-spoofing, so v4
remains an egress-only path.

Destination policy binds to delivery into local attachments (the `daddr`
dispatch map only ever holds locally routed workload addresses), never to
blanket prefix pairs, so a cross-node packet transits the sender's forward
chain toward the WireGuard device unfiltered and is evaluated once, at the
destination node, after decryption — same-node and cross-node paths see
identical policy. Enforcement is stateful: an allow means "may initiate connections
toward"; replies and ICMPv6 errors ride conntrack.

Override policies are global first-class entities (`network_policies` table)
with per-row optimistic-concurrency versions, an append-only version log
carrying the acting user, and soft delete; they are not part of any deployment
spec, HCL form, or config history. Peers are single-id anchors — a whole space,
or a deployment whose space resolves at render time so space moves follow
automatically. Every write advances the global write sequence in its own
transaction because policies are netmap render inputs. Writes require update
access on the destination's space (destination consent); naming a cross-space
source requires view access on that space; rules whose source and destination
resolve to the same space are rejected as redundant with the same-space
default. The `DENY` action exists in the schema and API but is rejected on
writes until enforced, and its evaluation order is fixed in the plan document.
The FE presents policies globally on the Network tab of the Policies page (the edit surface, which
also flags dangling rules referencing deleted entities) and as a derived
read-only view on the deployment inspector.

Rules are distributed as resolved-identity tuples (`policy_rules` on
`ClusterNetMap`): the publisher subscribes to policy-table updates and renders
each stored peer to `(space, deployment)` wire form, with `deployment = 0`
meaning the whole space. Workers validate the rules during map acceptance and
apply them via `Manager.SetPolicyRules` after topology reconciliation; the
primary's in-process applier does the same. The manager merges the rules
matching each locally attached destination into that deployment's `wl_dst`
chain as source-prefix (`/64` space or `/88` deployment) accepts with optional
protocol/port matches. Old agents drop the unknown field and simply keep not
enforcing overrides; a rule anchored to a deleted entity compiles to
permanently vacant prefixes — fail-closed until cleaned up.

Filter state is re-derived, not persisted: on agent restart the first nftables
reconcile recreates the skeleton and renders only recovered attachments, so
between that reconcile and each container's recovery its set elements are
briefly absent (the same window in which its DNAT rules are republished). Every
attachment create, recover, and teardown triggers a reconcile. Steady-state
reconciles flush and refill the set/map elements, rewrite the DNAT rules only
when their rendered content changed, and rebuild only the `wl_dst` chains whose
rendered rules changed, so the netlink batch stays small under attachment
churn. Any reconcile error marks the skeleton unready — the next attempt (an
immediate caller retry or the scheduled background retry) rebuilds everything
from scratch — and a failed reconcile leaves the previous complete ruleset in
place and fails container setup rather than running an attachment unfiltered.

## Netproxy Services

Node-local network state is deliberately separate from the cluster map, but
the map is now its DNS input. The cluster map carries cross-node routing plus
the cluster-wide DNS catalog: one entry per virtual-mode deployment with a
scheduled instance (name, space, deployment id) listing its established
ordinals. An ordinal is established when it has a target-`RUN_SERVING`
placement, or when a standby+draining pair exists. The promotion commits
atomically, so no store snapshot shows that pair without a serving placement;
the pair rule is defense-in-depth that keeps the render correct against any
writer not routed through the atomic flip, and it is load-bearing in the
map-less fallback below, whose input is the re-serialized per-instance stream
rather than an atomic snapshot. The catalog is
deliberately locality-free —
DNS answers carry no node information and no per-node ordering; locality, like
health, is a load-balancing concern, not a naming one. `netstate.pb`
renders its DNS services and ingress backends from that catalog — the whole
render is a pure function of target state, with runner status not an input —
and falls back to deriving the same shape from the node's own placements when
no catalog has ever been received (an old primary, or a cached pre-catalog
map). Ingress route definitions and the cert bundle still come from the local
placements. Outside the promotion window an ordinal's endpoint is published
exactly when its `/100` route exists, so a name can never resolve to an
address that does not route; a crashed container keeps its records. The
rendered snapshot is
deterministic and duplicate-free: the DNS services and ingress routes an old
and a replacement placement both render are merged with their backends
deduplicated.

The agent writes full node-local `NetState` protobuf snapshots to `/var/lib/opendeploy/netproxy/netstate.pb`. It atomically takes its initial deployment snapshot and subscribes before writing, so a concurrent route or endpoint update cannot be lost during startup. The snapshot contains DNS records and rendered ingress routes. Writes are content-diffed: `netstate.pb` and `certbundle.pb` each carry an independent sequence and are rewritten only when their own rendered content changed, so status heartbeats that change nothing rendered trigger no writes and no nftables reconciliation. Sequences seed at `max(persisted seq, unix-millis)` because netproxy's monotonic gates live in a separate process that survives an agent state-dir wipe. Netproxy watches its directory for the atomic write-rename updates and answers authoritatively for every catalogued name: AAAA records for established virtual endpoints anywhere in the cluster, and an empty `NOERROR` answer (any record type) for a service that exists but has no established ordinal, so `.internal` names of unestablished services are neither leaked upstream nor denied with `NXDOMAIN`. Names not in the catalog are forwarded to the host's upstream resolvers. Resolver discovery ignores loopback stubs that are unreachable from the netproxy namespace and falls back to the systemd-resolved or NetworkManager upstream resolver files. Forwarding is capped at 256 concurrent queries; overload and upstream failure return `SERVFAIL` rather than a cacheable negative answer.

For TLS passthrough, netproxy listens on each rendered ingress TCP host port. It
reads a bounded TLS ClientHello to select an exact SNI route, dials an
established backend's stable inbound address `I`, and relays the bytes unchanged. Connections
without usable SNI, unknown names, malformed ClientHellos, or routes without an
established backend are closed. Backends see netproxy as the peer; PROXY protocol is
not used in v1. The internal netproxy deployment has a 65,536 file-descriptor
limit because every routed connection holds one client and one backend socket.

DNS names are derived from deployment and space identity:

- `{name}.space-{spaceId}.internal` returns the established inbound addresses `I`.
- `{ordinal}.{name}.space-{spaceId}.internal` returns one established inbound address `I`.

Names are normalized to lowercase DNS labels, with underscores converted to dashes.

## Rollover

### Target state

`ScheduledInstanceTarget` has three runnable states. The node's operator treats
all three identically — they all mean "run this placement" — and every
target-state check in the engine goes through `ScheduledInstanceTarget.WantsRunning`
rather than comparing against one constant. They differ only in what network
state derives from them:

- `RUN_SERVING` owns the instance's `/100` and its stable inbound address. Exactly one placement per `(deployment, ordinal)` holds it.
- `RUN_STANDBY` is a warming replacement. It owns its own `/120` so its outbound traffic is routable before it ever serves. A lone standby is absent from DNS and ingress backends, because the inbound address does not route to it yet; a standby beside a draining placement of the same ordinal still counts as established, so a render fed by a non-atomic input stream never drops to zero backends across promotion.
- `RUN_DRAINING` is a superseded placement that is still running. It keeps its `/120`, so replies to work already in flight still reach it after `I` has moved.

A replacement is created as `RUN_STANDBY` from its very first write whenever a
serving placement already exists. Creating it as serving, even briefly, would
point the instance's inbound route at a node whose container does not exist yet.

### Same-node rollover

Virtual-mode ROLLOVER on one node uses the route-flip model:

1. The old container keeps the `I` route.
2. The candidate starts in a new netns with `O` preferred and routed, plus `I` configured with `preferred_lft=0` but not routed to it.
3. The candidate signals readiness through the existing Unix readiness socket.
4. The agent replaces the host route for `I` so new inbound traffic reaches the candidate and switches host-port forwarding to the candidate.
5. The old container is stopped and its network state is removed.

This avoids binding conflicts on published ports. Existing TCP connections to the old container can still break on promotion; ingress-level graceful handoff is part of future work.

The cluster map does not change across a same-node promotion: both placements
already point their `/120` at this node, and the `/100` never moves. The render
is byte-identical, so no new map is published and the wait collapses as soon as
a render has seen the flip's write sequence. This needs no special case — it
falls out of deriving routes from assignments.

### Cross-node rollover

A replacement on another node is an ordinary placement running a full runner
there, not a rollover candidate. The handoff is driven centrally:

1. The standby starts on the new node. It owns its `/120` immediately, so its outbound traffic is routable, but it does **not** claim `I`.
2. It reports `RUNNING`. The scheduler moves the old placement to `RUN_DRAINING` and the standby to `RUN_SERVING` in one atomic store commit (`FlipScheduledInstanceServing`: one lock hold, one transaction, per-row sequence numbers), which re-renders the map with `/100` pointing at the new node. No snapshot fetched from the store can observe the flip half-applied, so no rendered map ever transiently drops the ordinal's `/100` or its DNS record.
3. The scheduler waits for its own decision's write sequence to be in force — rendered, and the resulting map applied by every node holding network state — then tells the drained placement to terminate. A timeout backstops a node that is connected but wedged.

Snapshot atomicity ends at the store: the cluster stream re-serializes state as
per-instance messages, so a worker's local cache still applies the flip as two
steps. The invariant that keeps this sound is that aggregate views derive from
atomic documents — a store snapshot fetched in one critical section on the
primary, the published cluster map everywhere else — while the per-instance
stream only feeds actuators, which are intermediate-tolerant by design (a
draining runner keeps its routes; a runner claims `I` only on `RUN_SERVING`).

Claiming `I` locally is gated on target state, not on local readiness: a runner
installs the host route for `I` only once its placement is `RUN_SERVING`
(`Runner.Serve`, which is idempotent). Without that gate the standby would
install a host route for `I` on its own node while the old node still had one,
and traffic originating on each node would reach a different container until the
map propagated.

If the serving placement dies for good while a standby is warming, the standby
is promoted immediately rather than waiting for a readiness signal that a dead
container will never send.

`O` is not advertised through DNS, endpoints, ingress, or Address references.
It prevents any run's outbound traffic from claiming the stable inbound identity,
not just candidate warmup traffic. Promotion changes only inbound routing and
host-port ownership; the promoted run continues to prefer and route `O` until
that run ends.

## Ingress Shape

The proxy-backed ingress config is separate from raw `portForwarding`. Each
kind owns its configuration message; the envelope carries the kind-independent
claim fields (kind, hostname):

```js
ingress: [
  {
    kind: "TLS_PASSTHROUGH",
    hostname: "db.example.com",
    tlsPassthroughConfig: {hostPort: 443, containerPort: 5432},
  },
  {
    kind: "HTTPS",
    hostname: "app.example.com",
    httpsConfig: {
      containerPort: 8080,
      pathPrefix: "/api",       // "" ≡ "/"; segment-boundary prefix match
      stripPrefix: true,        // sets X-Forwarded-Prefix; no response rewriting
      backendProtocol: "H2C",   // default HTTP/1.1
      maxRequestBodyBytes: 0,   // 0 = unlimited
      flushIntervalMs: 0,       // 0 = auto; < 0 = flush every write
      certSource: {acme: {}},   // or {secret: {secretVersionId}}; unset = acme
    },
  },
]
```

### HTTPS termination

HTTPS routes are terminated by netproxy and claim TCP 443 plus 80 (HTTP→HTTPS
redirect and ACME HTTP-01 answers). The 443 listener is shared with TLS
passthrough: the ClientHello is peeked once, the SNI hostname selects the kind,
and terminated connections replay the peeked bytes into the local TLS server.
Requests are routed per request on `Host`/`:authority` with
longest-prefix-match — deterministic, no priorities, no regex. Overlapping
prefixes are composition, not collision; exact `(hostname, pathPrefix)`
duplicates and cross-kind hostname claims are rejected at config save, and
`certSource` must match across all routes sharing a hostname on a node. A
known-SNI connection carrying an unknown `:authority` receives 421, which also
defuses HTTP/2 connection coalescing across terminated hostnames. Requests are
proxied with `X-Forwarded-For/Proto/Host` (and `X-Forwarded-Prefix` when
stripping); WebSocket upgrades pass through; SSE and unknown-length responses
flush immediately. Only non-happy responses (status >= 400, backend dial/proxy
errors) are logged.

`backend = "h2c"` routes support full-duplex streaming (e.g. cleanproto bidi
RPCs, gRPC) end to end when the client connects with HTTP/2. The h2c backend
transport pumps the inbound request body through a cancelable pipe: without
it, a backend that half-closes its response while the client keeps the
request stream open deadlocks the raw http2 transport (closing the response
body waits on the request-body write goroutine, which sits in an
uninterruptible read on the inbound body). Covered by
`TestH2CBidiServerHalfClose` and the `proto-stream` e2e cases.

Certificate private keys never enter `netstate.pb`. The agent renders a
separate `certbundle.pb` (0600, atomic write-rename) beside it, resolving
secret-arm refs and ACME bindings from locally persisted secrets; netproxy
watches and serves from the last loaded bundle, so TLS survives agent restarts.
ACME issuance runs on the primary only (`lib/acmeissue`): eager issuance on
config save plus a 12-hour renewal loop (30 days before expiry), storing issued
certs as versioned secrets named `acme.cert.<hostname>`. Hostname→secret
bindings and pending HTTP-01 tokens are distributed to workers over the cluster
stream (`AcmeState`), persisted in the worker's local KV, and rendered into
netstate — netproxy answers `/.well-known/acme-challenge/` on port 80 without
the worker ever holding ACME account credentials. `OPENDEPLOY_ACME_DIRECTORY`
overrides the directory endpoint (e.g. pebble in tests).
