# Cross-node routing implementation plan

## Purpose

This document is the implementation plan for the remaining OpenDeploy
cross-node workload routing work. The canonical network design remains in
[`networking.md`](networking.md); the shipped implementation — addressing,
route ownership, underlay addresses, `ClusterNetMap` rendering and
distribution, worker tunnel/route reconciliation, and rollover semantics — is
documented in `docs/engineering/networking.md`.

Shipped so far: route-protocol ownership (documented value `200` with
structural ownership checks), the `ClusterNetMap`/`NetMapStatus` protocol with
`derived_from_seq` stamping and session-based acceptance, worker persistence
of accepted maps, fixed `ip6tnl`/SIT tunnel and remote routed-prefix
reconciliation on workers, and the map-application barrier used by cross-node
rollover. Routes are prefixes (`/100` per serving instance, `/120` per
placement) derived purely from scheduled-instance assignments, which removed
the earlier plan's need for runner-status-derived routes and worker
candidate-route reports.

The target is the first production-capable implementation for clusters of at
most approximately 100 nodes. It uses one fixed IP-in-IP tunnel per remote node
and direct logical workload routes. Larger-scale tunnel topology, NAT
traversal, and encrypted transport are outside this plan.

## Remaining fixed decisions

- The underlay family is inferred from the configured address. A cluster uses
  one underlay family; pairwise family selection is not part of the first
  implementation.
- IP protocol 41 must be directly routable between node underlay addresses.
  Ordinary port-based NAT traversal and relaying are not supported.
- The transport is initially unauthenticated and unencrypted.
- Routes determine location. nftables policy determines authorization.
- The primary computes and distributes state but is never on the packet path.
- Workers persist the last accepted network map and can restore forwarding
  without primary availability.

## Missing pieces

The following do not exist yet:

- Applying equivalent remote topology on the primary node. Worker-to-worker
  routing is implemented, but primary-hosted workloads do not receive remote
  routes.
- Source anti-spoofing and destination ingress policy.
- Cross-node DNS data. `netstate.pb` is derived node-locally, so `.internal`
  names resolve only for deployments on the resolving node.
- Cross-node integration tests and operational diagnostics.

## Security gate

Cross-node routing must not be enabled as a generally available feature until
the logical policy boundary exists.

### Source anti-spoofing

At each local veth or TAP ingress, allow only the run's assigned `I` and `O`.
During rollover both isolated attachments have the same non-preferred `I` and
their own `O`; routing determines which receives inbound `I` traffic.

Routing cannot provide this check. Without it, a workload can emit packets with
another workload's logical source identity.

### Destination ingress policy

Before delivery to a workload attachment:

```text
deny workload traffic
allow source from destination space /64
allow explicit cross-space deployment/port/protocol rules
allow explicit OpenDeploy system paths
```

The same destination-side rules apply after local routing or tunnel
decapsulation. A separate workload egress policy is not required for the first
same-space isolation boundary.

### Underlay trust

Fixed tunnel endpoints provide locator filtering but no cryptographic
authentication. An attacker able to inject protocol-41 packets using a trusted
node's underlay source can forge inner logical addresses. Deployments requiring
confidentiality or peer authentication must use application-layer security
until authenticated transport is added.

## Implementation sequence

Milestones 0–3 of the original plan (route ownership, the network-map API and
persistence, and fixed tunnel reconciliation on workers) have shipped; the
former runtime route-reporting milestone was made unnecessary by deriving
prefix routes from assignments alone.

### Milestone: primary-node topology

- Apply the primary's own targeted cluster map on the primary node so
  primary-hosted workloads reach and are reachable from remote placements.

### Milestone: logical network policy

- Add nftables source anti-spoofing per local attachment.
- Add same-space destination ingress policy.
- Add required OpenDeploy system paths.
- Ensure same-host and cross-host packets use equivalent policy.
- Add explicit cross-space policy only after the default boundary is correct.

### Milestone: DNS and product integration

- Render cluster-wide ready endpoints into node netproxy state.
- Display underlay family, tunnel readiness, applied map sequence, and errors on
  the node page.
- Add operator diagnostics for underlay reachability and MTU failures.
- Keep the primary off the data path during control-plane outages.

### Milestone: end-to-end verification

- Add two-node VM coverage for SIT over the existing IPv4-oriented VM network.
- Add IPv6-underlay VM coverage for `ip6tnl` once the harness provisions routed
  IPv6 endpoints.
- Verify TCP, UDP, and ICMPv6 workload traffic.
- Verify agent restart from cached state and continued traffic during primary
  outage.
- Verify a standby placement's outbound traffic receives remote replies through
  its `/120` route before promotion and that the route remains valid after.
- Verify stale map and tunnel state is removed without touching unrelated host
  networking.
- Verify same-space access and default cross-space denial.

## Failure behavior

### Remote node unavailable

Routes may continue selecting its tunnel and traffic fails according to normal
underlay behavior. Automatic rerouting is not possible because a stable
instance has one current placement. Health-aware DNS should stop returning DOWN
endpoints independently of route cleanup.

### Partial reconciliation

Full-state reconciliation retries. New routes are not installed before their
tunnel exists. Tunnels are not deleted while owned routes reference them.
Invalid new state does not erase the last-known-good persisted map.

### Underlay address change

Changing one node's underlay address causes every peer to recreate or update
that node's fixed tunnel. Logical workload routes and addresses remain
unchanged because they refer to node identity rather than outer addresses.

## Deferred work

- Pairwise IPv4/IPv6 family selection.
- NAT traversal, UDP encapsulation, and relay nodes.
- Encrypted and authenticated underlay transport (for example transparently
  routing each remote node's underlay address through WireGuard, which leaves
  tunnel interfaces and workload routes unchanged).
- Flow-based single-tunnel dataplanes.
- Bounded-degree gateway topology for clusters materially above 100 nodes.
- Incremental or sharded network-map distribution.
- Full workload egress policy and exfiltration controls.
- Cross-node scheduling and movement automation.

## Completion criteria

The first cross-node routing release is complete when:

- The primary node reconciles the same tunnels and remote routes as workers.
- Source spoofing is blocked at local attachments.
- Same-space traffic is allowed and cross-space traffic is denied by default on
  both same-node and cross-node paths.
- `.internal` names resolve cluster-wide.
- Node status exposes map and tunnel reconciliation failures.
- SIT and `ip6tnl` paths pass end-to-end VM coverage.
- No reconciliation operation deletes unrelated host routes or interfaces.
