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
- The primary renders deterministic, versioned full network maps containing node underlay addresses and virtual-workload routes. It durably preserves their generation and sequence, coalesces updates for connected workers, and includes a targeted map in enrollment when publication succeeds.
- Workers validate and persist accepted maps before exposing their prefix to runtime networking. They reject stale sequences, conflicting snapshots, wrong targets, mixed underlay families, invalid route layouts, and returns to retired generations; cached maps survive primary outages and agent restarts.
- Workers reconcile fixed IPv6-in-IPv6 or IPv6-in-IPv4 tunnels and remote `/128` routes from accepted maps, then report the applied sequence.
- IPv4 egress is masqueraded from a fixed machine-local private range.
- `portForwarding` publishes virtual-mode container TCP/UDP ports through nftables DNAT on the machine's host interfaces.
- ROLLOVER in virtual mode starts a candidate with both addresses and promotes it by flipping the stable inbound-address host route. Promotion does not change source-address preference: `O` remains preferred for the promoted run's full lifetime.
- Runner status publishes READY/DOWN endpoint state for virtual-mode deployments.
- A per-machine internal netproxy deployment runs the built-in DNS process and answers `.internal` AAAA records from endpoint state.
- Virtual-mode deployments can declare `TLS_PASSTHROUGH` ingress routes. The local agent renders them and their READY IPv6 backends into `netstate.pb`, derives netproxy TCP forwarding from their host-port set, and netproxy forwards TLS streams by SNI without termination.

Not implemented yet:

- Applying the targeted cluster map on the primary node; worker-to-worker routing is implemented, but primary-hosted workloads do not yet receive equivalent remote routes.
- Runtime reporting and distribution of unpublished candidate outbound routes. Current maps use observed inbound endpoints and derive `O` for published `STARTING` and `RUNNING` runners.
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
<ULA:48><space:16><deployment:24><ordinal:12><versionSlot:20><runSlot:8>
```

This layout replaces the prior address ABI outright. Old-layout addresses are
invalid; there is no compatibility decoder, migration route, or dual-layout
operation.

For a cluster prefix `prefix` and instance `(space, deployment, ordinal)`:

```text
I = Address(prefix, space, deployment, ordinal, 0, 0)
O = Address(prefix, space, deployment, ordinal, versionSlot, runSlot)
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
slot(n, max) = ((n - 1) % max) + 1
versionSlot  = slot(configVersion, 2^20 - 1)
runSlot      = slot(runNumber, 2^8 - 1)
```

The pair `(0, 0)` is valid only for `I`; `O` requires both slots to be nonzero.
The implementation rejects a live outbound-address collision rather than
allowing two concurrent runs of one deployment to share `O` after slot wrap.

The resulting logical prefix hierarchy is:

- Cluster: `/48`.
- Space: `/64`.
- Deployment: `/88`.
- Instance `(deployment, ordinal)`: `/100`.
- Version slot: `/120`.
- Individual address: `/128`.

The hard address-layout capacities are 65,536 spaces, 16,777,216 deployment
field values with deployment `0` invalid, 4,096 ordinals, 1,048,575 nonzero
version slots, and 255 nonzero run slots. Ordinals do not wrap. Version and run
slots wrap only through the normalization above; the raw values do not wrap.

Space is part of logical network identity. Moving a deployment to another space changes its inbound and outbound addresses and is therefore a connection-breaking security-domain migration. Moving an instance between nodes does not change `I`.

## Runtime Wiring

For virtual-mode containers, the runner asks `backend/lib/network` to create network state before starting the containerd task. The network manager creates the named netns, veth pair, container-side addresses, default routes, host-side gateway addresses, and host route. The containerd wrapper joins the pre-created network namespace through the OCI spec. Host veth names are deterministic deployment slots: `od<deploymentID>s0` for the first live network and `od<deploymentID>s1` for a concurrent rollover candidate.

When an agent restarts, containerd tasks and their network namespaces can remain running. Reattachment opens the surviving named namespace and identifies its deterministic host veth by the mutual peer indexes of namespace `eth0` and the host link. Veth aliases are not used for ownership. Recovery restores the run's `O` route and, for the current run, its `I` route, then records the host ifindex before removing unretained slots, so a current task is never deleted as stale and delayed teardown cannot delete a link whose name has since been reused. A task on the current config republishes its host-port state; the internal netproxy also republishes across its version-only upgrades. Older application tasks retain recovered metadata for safe teardown but wait for their prepared replacement before using a newer networking config. If required reconstruction fails, the adopted task is replaced rather than left running without its forwarding rules.

The primary persists one target-neutral `ClusterNetMap` in `local_kv`. Its
generation survives ordinary restarts and its sequence advances only when
deterministically rendered node or route content changes. The primary derives
`I` from each published runner's observed endpoint and derives `O` from that
observed identity plus the runner's config version and run number.
An unpublished rollover candidate is intentionally absent even though its local
`O` route already exists; reporting and distributing that candidate route for
cross-node replies remains future work. Session delivery clones the map with
the mTLS-authenticated node ID and uses a capacity-one latest-value channel.
Workers persist their targeted accepted map and retired generation IDs
atomically. `NetMapStatus` reports durable acceptance and the sequence
successfully applied to the worker kernel.

Each node gets an `opendeploy-net` deployment when it is first created or enrolled, initially using the primary release available at that time. Agent upgrades and primary restarts do not change an existing netproxy's desired version or running state. Administrators update netproxy deployments explicitly and are responsible for selecting versions compatible with the agents and rendered netstate format.

The container receives a generated `resolv.conf` pointing at the machine-local netproxy DNS address once the netproxy deployment id is known. The netproxy deployment itself uses the host resolver to avoid a DNS dependency cycle.

## Netproxy Services

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

Virtual-mode ROLLOVER uses the route-flip model:

1. The old container keeps the `I` route.
2. The candidate starts in a new netns with `O` preferred and routed, plus `I` configured with `preferred_lft=0` but not routed to it.
3. The candidate signals readiness through the existing Unix readiness socket.
4. The agent replaces the host route for `I` so new inbound traffic reaches the candidate and switches host-port forwarding to the candidate.
5. The old container is stopped and its network state is removed.

This avoids binding conflicts on published ports. Existing TCP connections to the old container can still break on promotion; ingress-level graceful handoff is part of future work.

`O` is not advertised through DNS, endpoints, ingress, or Address references.
It prevents any run's outbound traffic from claiming the stable inbound identity,
not just candidate warmup traffic. Promotion changes only inbound routing and
host-port ownership; the promoted run continues to prefer and route `O` until
that run ends.

## Ingress Shape

The proxy-backed ingress config is separate from raw `portForwarding`. Only the
TLS-passthrough configuration and node-local rendering exist today:

```js
ingress: [
  {
    kind: "TLS_PASSTHROUGH",
    hostname: "db.example.com",
    tlsPassthroughConfig: {hostPort: 443, containerPort: 5432},
  },
]
```

Future kinds will receive their own kind-specific configuration message rather
than extending `TlsPassthroughConfig` with unrelated fields.
