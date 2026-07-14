# Networking

## Overview

OpenDeploy has a machine-local virtual networking implementation for container deployments. It is implemented in-process in the agent, with the Linux kernel as the dataplane. The cross-machine mesh, ingress proxy, and network policy design remain future work; see `docs/future-work/networking.md`.

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

Not implemented yet:

- WireGuard mesh and cross-machine workload routing.
- Distributed netmap containing machine peers, keys, endpoints, routes, and policy state.
- Source anti-spoofing and destination ingress policy.
- Embedded public ingress proxy and ACME certificate distribution.
- Service virtual addresses and socket-level load balancing.
- Cross-machine `ingress` routes (`HTTPS`, `TLS_PASSTHROUGH`, `HTTP3`) and the embedded proxy that serves them.

## Configuration

Deployment networking is configured on `DeploymentSpec.networking`.

- `NETWORKING_MODE_HOST` runs the container in the host network namespace.
- `NETWORKING_MODE_VIRTUAL` runs the container in a managed network namespace.
- `NETWORKING_MODE_UNSPECIFIED` is invalid for create/update requests. Deployment specs must set an explicit mode.

`networking.portForwarding` maps one host-interface TCP or UDP port to one container port and requires virtual mode. TCP and UDP claims are independent, so the same numeric host port can be published once for TCP and once for UDP.

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

The container receives a generated `resolv.conf` pointing at the machine-local netproxy DNS address once the netproxy deployment id is known. The netproxy deployment itself uses the host resolver to avoid a DNS dependency cycle.

## Netproxy DNS

The agent writes full `NetState` protobuf snapshots to `/var/lib/opendeploy/netproxy/netstate.pb`. The netproxy DNS process polls this file, answers `.internal` AAAA records for READY virtual endpoints, and forwards unmatched queries to the host's upstream resolvers.

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

## Future Ingress Shape

The future proxy-backed ingress config is separate from raw `portForwarding` and is not implemented yet. Planned route examples:

```js
ingress: [
  {kind: "HTTPS", hostname: "api.example.com", pathPrefix: "/api", containerPort: 8080},
  {kind: "TLS_PASSTHROUGH", hostname: "db.example.com", containerPort: 5432},
  {kind: "HTTP3", hostname: "api.example.com", pathPrefix: "/api", containerPort: 8080},
]
```
