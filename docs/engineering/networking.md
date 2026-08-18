# Networking

## Overview

OpenDeploy implements virtual networking for container deployments in-process in the agent, with the Linux kernel as the dataplane. Worker nodes reconcile fixed IP-in-IP tunnels and remote workload routes from the cluster network map. Primary-node remote routing, complete candidate-route reporting, L7 ingress, and network policy remain future work; see `docs/future-work/networking.md`.

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
- Workers reconcile fixed IPv6-in-IPv6 or IPv6-in-IPv4 tunnels and remote routed prefixes from accepted maps, then report the applied stamp.
- The primary consumes those reports as a barrier: a superseded placement keeps running, and keeps its routes, until every node holding network state has applied the map that replaced it.
- IPv4 egress is masqueraded from a fixed machine-local private range.
- `portForwarding` publishes virtual-mode container TCP/UDP ports through nftables DNAT on the machine's host interfaces.
- ROLLOVER in virtual mode starts a candidate with both addresses and promotes it by flipping the stable inbound-address host route. Promotion does not change source-address preference: `O` remains preferred for the promoted run's full lifetime.
- A placement claims the stable inbound address only when its target state is `RUN_SERVING`, so a replacement warming up on another node cannot hold the same address as the placement it is replacing.
- Endpoints are derived, not reported. A placement's inbound address is a pure function of its space, deployment, and ordinal, so `netstate.pb` renders DNS and ingress backends from the placement itself and reads status only for whether it is running.
- The space in that function is the placement's own pin, captured at scheduling time as a `deployment_space_versions` row reference and overlaid onto the config identity every derivation reads. A deployment space move therefore never mutates live network state: the scheduler replaces placements through the rollover path, and old-space and new-space placements coexist (each self-consistently addressed, named, and certified for its own space) until the flip.
- A per-machine internal netproxy deployment runs the built-in DNS process and answers `.internal` AAAA records from endpoint state.
- Virtual-mode deployments can declare `TLS_PASSTHROUGH` ingress routes. The local agent renders them and their READY IPv6 backends into `netstate.pb`, derives netproxy TCP forwarding from their host-port set, and netproxy forwards TLS streams by SNI without termination.

Not implemented yet:

- Applying the targeted cluster map on the primary node; worker-to-worker routing is implemented, but primary-hosted workloads do not yet receive equivalent remote routes.
- Cross-node DNS. `netstate.pb` is derived node-locally from the placements a node holds, so `.internal` names resolve only for deployments running on the resolving node.
- Source anti-spoofing and destination ingress policy.
- Embedded public L7 ingress proxy, including TLS termination and ACME certificate distribution.
- A separate address allocation and design for future service virtual addresses, plus socket-level load balancing. The workload address ABI allocates only `I` and `O`.
- Cross-machine `ingress` routes (`HTTPS`, `TLS_PASSTHROUGH`, `HTTP3`) and the embedded proxy that serves them.

## Configuration

Deployment networking is configured on `DeploymentSpec.networking`.

- `NETWORKING_MODE_HOST` runs the container in the host network namespace.
- `NETWORKING_MODE_VIRTUAL` runs the container in a managed network namespace.
- `NETWORKING_MODE_UNSPECIFIED` is invalid for create/update requests. Deployment specs must set an explicit mode.

`networking.portForwarding` maps one host-interface TCP or UDP port to one container port and requires virtual mode. TCP and UDP claims are independent, so the same numeric host port can be published once for TCP and once for UDP.

`networking.ingress` also requires virtual mode. The currently supported shape is a
`TLS_PASSTHROUGH` route with a hostname and a `tlsPassthroughConfig` containing
`containerPort` plus optional `hostPort` (zero/default is `443`). The route and
raw TCP forwarding cannot claim the same host port on a node; multiple distinct
hostnames can share one ingress host port. Netproxy reads the TLS ClientHello to
match SNI, then forwards the original TCP stream to a READY backend. The primary
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
incarnation that owns a node assignment — not a config version. Two live
placements can share a config version, which happens whenever an instance moves
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

Full deployment config versions and run numbers remain full-width in desired
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

## Netproxy Services

Node-local network state is deliberately separate from the cluster map. The
cluster map carries cross-node routing; `netstate.pb` carries DNS and ingress,
derived on each node from the placements it holds. Only a `RUN_SERVING`
placement that is actually `RUNNING` contributes an endpoint, which is what
keeps a name from resolving to, or a proxy from dialling, a container that is
not up.

The agent writes full node-local `NetState` protobuf snapshots to `/var/lib/opendeploy/netproxy/netstate.pb`. It atomically takes its initial deployment snapshot and subscribes before writing, so a concurrent route or endpoint update cannot be lost during startup. The snapshot contains DNS records and rendered ingress routes. Netproxy watches its directory for the atomic write-rename updates, answers `.internal` AAAA records for READY virtual endpoints, and forwards unmatched queries to the host's upstream resolvers. Resolver discovery ignores loopback stubs that are unreachable from the netproxy namespace and falls back to the systemd-resolved or NetworkManager upstream resolver files. Forwarding is capped at 256 concurrent queries; overload and upstream failure return `SERVFAIL` rather than a cacheable negative answer.

For TLS passthrough, netproxy listens on each rendered ingress TCP host port. It
reads a bounded TLS ClientHello to select an exact SNI route, dials a READY
backend's stable inbound address `I`, and relays the bytes unchanged. Connections
without usable SNI, unknown names, malformed ClientHellos, or routes without a
READY backend are closed. Backends see netproxy as the peer; PROXY protocol is
not used in v1. The internal netproxy deployment has a 65,536 file-descriptor
limit because every routed connection holds one client and one backend socket.

DNS names are derived from deployment and space identity:

- `{name}.{environment}.internal` returns READY inbound addresses `I`.
- `{ordinal}.{name}.{environment}.internal` returns one READY inbound address `I`.

Names are normalized to lowercase DNS labels, with underscores converted to dashes.

## Rollover

### Target state

`ScheduledInstanceTarget` has three runnable states. The node's operator treats
all three identically — they all mean "run this placement" — and every
target-state check in the engine goes through `ScheduledInstanceTarget.WantsRunning`
rather than comparing against one constant. They differ only in what network
state derives from them:

- `RUN_SERVING` owns the instance's `/100` and its stable inbound address. Exactly one placement per `(deployment, ordinal)` holds it.
- `RUN_STANDBY` is a warming replacement. It owns its own `/120` so its outbound traffic is routable before it ever serves, and it is deliberately absent from DNS and ingress backends.
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
2. It reports `RUNNING`. The scheduler moves the old placement to `RUN_DRAINING` and the standby to `RUN_SERVING` in one step, which re-renders the map with `/100` pointing at the new node.
3. The scheduler waits for its own decision's write sequence to be in force — rendered, and the resulting map applied by every node holding network state — then tells the drained placement to terminate. A timeout backstops a node that is connected but wedged.

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
