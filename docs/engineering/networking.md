# Networking

## Overview

OpenDeploy has a machine-local virtual networking implementation for container deployments. It is implemented in-process in the agent, with the Linux kernel as the dataplane. The cross-machine mesh, L7 ingress proxy, and network policy design remain future work; see `docs/future-work/networking.md`.

## Current Scope

Implemented today:

- A cluster-wide RFC 4193 ULA `/48` prefix is generated once on the primary and persisted in config.
- Workers receive the ULA prefix over the existing primary-to-worker cluster stream and cache it locally.
- Virtual-mode containers run in a dedicated Linux network namespace with a veth pair.
- Each virtual-mode container receives a derived stable IPv6 instance address and a machine-local IPv4 address for egress.
- The host installs `/128` routes to local virtual-mode containers.
- IPv4 egress is masqueraded from a fixed machine-local private range.
- `portForwarding` publishes virtual-mode container TCP/UDP ports through nftables DNAT on the machine's host interfaces.
- ROLLOVER in virtual mode starts a candidate on a run-scoped IPv6 address and promotes it by flipping the stable-address host route.
- Runner status publishes READY/DOWN endpoint state for virtual-mode deployments.
- A per-machine internal netproxy deployment runs the built-in DNS process and answers `.internal` AAAA records from endpoint state.
- Virtual-mode deployments can declare `TLS_PASSTHROUGH` ingress routes. The local agent renders them and their READY IPv6 backends into `netstate.pb`, derives netproxy TCP forwarding from their host-port set, and netproxy forwards TLS streams by SNI without termination.

Not implemented yet:

- WireGuard mesh and cross-machine workload routing.
- Distributed netmap containing machine peers, keys, endpoints, routes, and policy state.
- Source anti-spoofing and destination ingress policy.
- Embedded public L7 ingress proxy, including TLS termination and ACME certificate distribution.
- Service virtual addresses and socket-level load balancing.
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

Addresses are pure functions of the ULA prefix, space, deployment, kind, and field. There is no workload IPAM allocator.

The IPv6 layout after the `/48` prefix is:

```text
<node:17> : <space:17> : <deployment:26> : <kind:4> : <field:16>
```

Node `0` identifies the logical address applications, DNS, endpoint status, and policy use. Nonzero node values are reserved for the future cross-machine locator translation described in `docs/future-work/workload-addressing-routing.md`; the current machine-local implementation only creates node-zero logical addresses.

The resulting logical prefix hierarchy is:

- Cluster: `/48`.
- Logical address root (`node = 0`): `/65`.
- Space: `/82`.
- Deployment across every kind and field: `/108`.
- Kind: `/112`.
- Individual address: `/128`.

Current kinds:

- Kind `0`: stable instance address, field is the instance ordinal. Only ordinal `0` exists today.
- Kind `1`: service address, currently unrouted.
- Kind `2`: run-scoped address used as a rollover candidate's preferred outbound source during warmup.

Kinds `3` through `15` are reserved. The hard address-layout limits are 131,071 nonzero node ids, 131,072 spaces, 67,108,864 deployment ids, 16 kinds, and 65,536 field values per kind. Run fields may wrap because only concurrently live temporary runs must be distinct; stable instance ordinals do not wrap.

Space is part of logical network identity. Moving a deployment to another space changes its instance, service, and run addresses and is therefore a connection-breaking security-domain migration. Moving an instance between nodes does not change its logical address.

## Runtime Wiring

For virtual-mode containers, the runner asks `backend/lib/network` to create network state before starting the containerd task. The network manager creates the named netns, veth pair, container-side addresses, default routes, host-side gateway addresses, and host route. The containerd wrapper joins the pre-created network namespace through the OCI spec.

When an agent restarts, containerd tasks and their network namespaces can remain running. Reattachment reconstructs network metadata from the surviving named netns and host veth. A task on the current config republishes its host-port state; the internal netproxy also republishes across its version-only upgrades. Older application tasks retain recovered metadata for safe teardown but wait for their prepared replacement before using a newer networking config. If required reconstruction fails, the adopted task is replaced rather than left running without its forwarding rules.

The container receives a generated `resolv.conf` pointing at the machine-local netproxy DNS address once the netproxy deployment id is known. The netproxy deployment itself uses the host resolver to avoid a DNS dependency cycle.

## Netproxy Services

The agent writes full node-local `NetState` protobuf snapshots to `/var/lib/opendeploy/netproxy/netstate.pb`. It atomically takes its initial deployment snapshot and subscribes before writing, so a concurrent route or endpoint update cannot be lost during startup. The snapshot contains DNS records and rendered ingress routes. Netproxy watches its directory for the atomic write-rename updates, answers `.internal` AAAA records for READY virtual endpoints, and forwards unmatched queries to the host's upstream resolvers. Resolver discovery ignores loopback stubs that are unreachable from the netproxy namespace and falls back to the systemd-resolved or NetworkManager upstream resolver files. Forwarding is capped at 256 concurrent queries; overload and upstream failure return `SERVFAIL` rather than a cacheable negative answer.

For TLS passthrough, netproxy listens on each rendered ingress TCP host port. It
reads a bounded TLS ClientHello to select an exact SNI route, dials a READY
backend's virtual IPv6 address, and relays the bytes unchanged. Connections
without usable SNI, unknown names, malformed ClientHellos, or routes without a
READY backend are closed. Backends see netproxy as the peer; PROXY protocol is
not used in v1. The internal netproxy deployment has a 65,536 file-descriptor
limit because every routed connection holds one client and one backend socket.

DNS names are derived from deployment and space identity:

- `{name}.{environment}.internal` returns READY instance addresses.
- `{ordinal}.{name}.{environment}.internal` returns one READY instance address.

Names are normalized to lowercase DNS labels, with underscores converted to dashes.

## Rollover

Virtual-mode ROLLOVER uses the route-flip model:

1. The old container keeps the stable instance address route.
2. The candidate starts in a new netns with a run-scoped address as its preferred outbound source, plus the stable address configured as deprecated/non-preferred.
3. The candidate signals readiness through the existing Unix readiness socket.
4. The agent replaces the host route for the stable address so new traffic reaches the candidate.
5. The old container is stopped and its network state is removed.

This avoids binding conflicts on published ports. Existing TCP connections to the old container can still break on promotion; ingress-level graceful handoff is part of future work.

The run-scoped address is not the address clients use after promotion. It exists so candidate warmup calls do not appear to come from the stable service identity before the candidate is active. Preassigning the stable address as non-preferred lets promotion avoid mutating the container netns; OpenDeploy only flips the host route for the stable address.

## Address Layout Migration

The node/space/deployment/kind/field layout replaced the earlier kind/deployment/field layout before user virtual-mode deployments existed. The persisted cluster ULA `/48` does not change. Existing host-mode deployments are unaffected.

The only pre-existing virtual-mode workload is the per-machine `opendeploy-net` internal deployment in space `0`. An agent can reattach its old container and network namespace across an upgrade, so each `opendeploy-net` deployment must be redeployed to the new OpenDeploy version after upgrading. The normal deployment replacement tears down the old namespace and recreates it with the newly derived address; no database or prefix migration is required.

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
