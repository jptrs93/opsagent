# Ingress listen selectors implementation plan

Status: implemented 2026-09-04 across all seven phases; the shipped shape is
documented in `docs/engineering/networking.md` (Ingress Shape, Listen
selectors). Deviations from the plan below: `NodeSelector` uses two plain
fields (`any`, `node_id`) because cleanproto has no `oneof`; warnings travel on
the state stream as `ingress_diagnostics_snapshot` rather than in the update
response; there is no all-address fallback on the agent (the publish set is
authoritative, and an unknown inventory is handled by the evaluator instead);
the default node selector is the scheduled node (`scheduled_node()`) rather
than any node, so routes follow their workload and `any_node()` is the opt-in
to cluster-wide publishing; a `listen` node selector naming a node other than
the deployment's own is rejected at create and update until cross-node backend
dialling exists;
and the e2e coverage lives on worker-1 because a node's publish set is the
union over its routes, so a restriction is only observable on a node whose
port the case owns.

## Purpose

This document is the implementation plan for publishing ingress routes on a
selected set of node addresses instead of on every local address of the hosting
node. It replaces the primary-node `:443` reservation with a general collision
check, gives the HCL `network` section a block form, and adds the inventory the
check needs. The current implementation is documented in
`docs/engineering/networking.md` (Configuration, Ingress Shape); the design
context is the "Web UI through the proxy" item in `networking.md`.

## Problem

Netproxy is a virtual-mode container. Host ports reach it through nftables DNAT
rules rendered by `lib/network/hostports.go` and `nft_linux.go`. The rule for an
ingress port matches `fib daddr type local`, so it captures traffic to every
address the machine owns. On a machine where another listener holds the same
port on one specific address, the DNAT rule steals that listener's traffic.

The Web UI is such a listener: it binds `https_web.listen` on the primary,
`:443` by default. `webuihandler/deployment_validation.go` therefore rejects
every `HTTPS` route and every `TLS_PASSTHROUGH` route on host port 443 whose
deployment is placed on the primary node. Host-mode deployments that bind a
specific address on 443 have the same exposure on any node and are not
detected.

## Fixed decisions

- A route is published on a set of (node, address) pairs. The set is declared
  with selectors, not enumerated, so nodes and addresses added to the cluster
  later are captured without a config change.
- Selectors form a closed algebra: a node selector (scheduled, `any`, or one node) and an
  address selector (`any`, one family, or a list of IP or CIDR literals). There
  is no expression language.
- The port stays on the route. `host_port` is not part of a selector.
- A route without selectors publishes on every address of the scheduled node,
  which is today's behaviour. There is no `network`-level default and no cluster
  setting in this plan.
- The primary evaluates selectors against the live inventory and distributes
  concrete publish sets. Nodes never evaluate selectors.
- The Web UI listen address is a reserved claim in the same evaluation. The
  primary-node special case in validation is removed.
- Netproxy is unchanged. It binds wildcard ports inside its own namespace; the
  address restriction is applied by the agent's DNAT rules.
- HCL port attributes are `container_port` and `host_port` in every block. There
  is no bare `port`.

## Shape

### Deployment spec

`api-contract/model/deployments.proto`:

```proto
message Ingress {
  IngressKind kind = 1;
  string hostname = 2;
  TlsPassthroughConfig tls_passthrough_config = 3;
  HttpsConfig https_config = 4;
  repeated IngressListen listen = 5;  // empty = every address of the scheduled node
}

// One set of (node, host address) pairs. A route's list is the union.
message IngressListen {
  NodeSelector node = 1;
  AddressSelector address = 2;
}

message NodeSelector {
  oneof selector {
    bool any = 1;
    int32 node_id = 2;
  }
}

enum AddressFamily {
  ADDRESS_FAMILY_ANY = 0;
  ADDRESS_FAMILY_IPV4 = 1;
  ADDRESS_FAMILY_IPV6 = 2;
}

message AddressSelector {
  AddressFamily family = 1;
  repeated string prefixes = 2;  // IP or CIDR literals; empty = every address in the family
}
```

An unset `node` means `any`; an unset `address` means `ADDRESS_FAMILY_ANY`
with no prefixes. `PortForward` does not take `listen` in this plan.

### Inventory

`api-contract/model_cluster_operations.proto`:

```proto
message ClusterHello {
  string underlay_address = 1;
  int32 cluster_protocol_version = 2;
  string wg_public_key = 3;
  repeated string host_addresses = 4;  // global unicast addresses on non-OpenDeploy interfaces
}
```

`api-contract/model/nodes.proto` `NodeStatus` gains `repeated string
host_addresses = 5`. Host addresses are runtime facts, so they live in
`node_statuses` (new column `host_addresses TEXT NOT NULL DEFAULT '[]'`), not in
`node_event_log`.

Eligible addresses are global unicast addresses on interfaces the agent does not
create. Excluded: loopback, link-local, the WireGuard underlay interface, the
container bridge and veth interfaces, and any address inside the cluster ULA
prefix or the IPv4 egress range.

### Distribution

`api-contract/model/networking.proto`:

```proto
message ClusterNetMapNode {
  int32 node_id = 1;
  string underlay_address = 2;
  string wg_public_key = 3;
  int32 wg_listen_port = 4;
  repeated IngressPublish ingress_publish = 5;  // concrete DNAT set for this node
}

message IngressPublish {
  string address = 1;  // one host address
  int32 port = 2;      // TCP host port
}
```

Hostnames are not distributed. DNAT needs (address, port); hostnames matter only
for collision checks, which run on the primary.

### HCL

The `network` section moves from a call list to blocks. `listen` is a repeated
block whose attributes default to `any`.

```hcl
network {
  mode = "virtual"

  ingress {
    https {
      hostname          = "api.example.com"
      container_port    = 5001
      flush_interval_ms = -1
      cert              = acme()

      listen {
        node    = node("primary")
        address = "203.0.113.10"
      }
    }

    https {
      hostname       = "app.example.com"
      container_port = 5000
      path_prefix    = "/api"
      strip_prefix   = true
      backend        = "h2c"
      max_request_body_bytes = 20000000
      cert           = secret("global", "certs/app-cert", 3)

      listen {
        address = ipv4()
      }
    }

    tls_passthrough {
      hostname       = "db.example.com"
      container_port = 5432
      host_port      = 5433
    }

    port_forward {
      protocol       = "tcp"
      container_port = 5432
      host_port      = 5432
      allow          = ["198.51.100.0/24"]
    }
  }
}
```

| Block | Required | Optional |
|---|---|---|
| `https` | `hostname`, `container_port` | `path_prefix`, `strip_prefix`, `backend`, `max_request_body_bytes`, `flush_interval_ms`, `cert`, `listen` |
| `tls_passthrough` | `hostname`, `container_port` | `host_port` (default 443), `listen` |
| `port_forward` | `protocol`, `container_port` | `host_port` (default `container_port`), `allow` |
| `listen` | none | `node` (`scheduled_node()`, `node("name")`, `any_node()`), `address` (`any_address()`, `ipv4()`, `ipv6()`, IP or CIDR string) |

`https` rejects `host_port`: "https is always published on 443; host_port is
not configurable." The renderer omits attributes at their default and emits
`listen` blocks only when set. HCL is regenerated from the spec on open and is
not stored, so the call form is removed rather than dual-parsed; a pasted call
form produces one diagnostic naming the block form.

## Evaluation

A new package `backend/lib/ingressplan` owns selector expansion and collision
detection. Both the validation layer and the netmap publisher call it, so the
save-time answer and the distributed publish set cannot diverge.

Inputs:

- Deployments with virtual networking: id, node, ingress routes with `listen`.
- Nodes: id, host addresses from `node_statuses`.
- Reservations: the Web UI `https_web.listen` when HTTPS Web is enabled,
  resolved to (primary node, address or wildcard, port).
- Reachability: the set of nodes that can dial a route's backends. Today this is
  the hosting node only. When cross-node backend dialling lands
  (`cross-node-routing-implementation-plan.md`) it becomes every ingress node,
  and `any` selectors widen without a config change.

Expansion of one route: nodes matching the node selector, intersected with the
reachable set; for each node, its host addresses filtered by the address
selector. A prefix literal that matches no current address expands to nothing
on that node and is not an error. Port is `443` for `HTTPS` and
`tls_passthrough_config.host_port` (default 443) for passthrough.

Outputs:

- Per node, the publish set `[(address, port)]`, the union over all routes.
- Per route, the concrete claims `(node, address, port, hostname[, path_prefix])`.
- Diagnostics: errors (rejected at save) and warnings (surfaced on status).

Rules:

- A claim whose address is a literal and equals a reservation's address on the
  same node and port is an error naming the reservation.
- A claim produced by a wildcard or family selector that equals a reservation is
  dropped from the publish set, and the save response reports the exclusion.
- A reservation with a wildcard listen host reserves the port on every address
  of that node. Literal claims on that node and port are errors; wildcard claims
  are dropped with a report.
- Two deployments whose expanded claims share (node, address, port, hostname),
  or for HTTPS (node, address, hostname, path_prefix), are an error. This
  replaces the existing per-node hostname and prefix maps.
- `certSource` must match across all HTTPS routes sharing a hostname on a node,
  as today.
- A wildcard or family selector expanding onto a node that hosts host-mode
  deployments produces a warning naming those deployments. Host-mode port
  bindings are not visible to the evaluator.
- Inventory changes after save are re-evaluated by the publisher. A newly
  created overlap between two deployments resolves to the lower deployment id
  and is surfaced as a warning on both deployments. Reservations always win.

The settings path runs the same evaluator: a change to `https_web.listen` or
`https_web.enabled` that turns an existing literal claim into an error is
rejected by `validateResolvedSettings`.

## Data plane

`lib/network`:

- `HostPortRule` gains `Dest []netip.Prefix`. When non-empty, `addDNATRules`
  emits one prerouting rule and one output rule per destination prefix using
  `addrMatchExprs(prefix, false)` in place of the `fib daddr type local`
  expressions, crossed with the existing per-source loop when `Filtered`. When
  empty, rendering is unchanged.
- `netproxyIngressPorts map[uint16]struct{}` is replaced by
  `netproxyPublish []IngressPublish`. `SetNetproxyPublish(entries)` replaces
  `SetNetproxyIngress(ingress)`. `reconcileNetproxyHostPortsLocked` groups
  entries by port and emits one `HostPortRule` per port with `Dest` set to the
  addresses for that port.
- Fallback: when the agent has no publish set for its node (older primary), it
  derives ports from `NetState.Ingress` as today and publishes on all local
  addresses.

`lib/netaudit`: `collect_linux.go` parses DNAT rules into a key of (family,
chain, proto, host port, source, target, target port). The key gains the
destination match so restricted rules audit clean, and the expected-rule
renderer emits the same key.

Agent wiring:

- Secondary: `network_map.go` normalises `ingress_publish` per node, validates
  each address parses and each port is in range, and applies its own entry with
  `network.Default.SetNetproxyPublish` when a map is accepted.
- Primary: `primary/runtime.go` subscribes to its own targeted map from the
  publisher, as `RunNetStateWriter` does, and applies the entry the same way.
- `RunNetStateWriter` stops calling `SetNetproxyIngress`. `NetState.Ingress`
  keeps its shape; netproxy still derives its wildcard listener set from it.

## Phases

Each phase is independently mergeable and leaves the cluster working.

### Phase 1: protos and codegen

- Add `IngressListen`, `NodeSelector`, `AddressSelector`, `AddressFamily`, and
  `Ingress.listen` to `deployments.proto`.
- Add `ClusterHello.host_addresses`, `NodeStatus.host_addresses`,
  `ClusterNetMapNode.ingress_publish`, and `IngressPublish`.
- Regenerate `backend/apigen` and `frontend/src/capi`.
- `preLockDeploymentValidate`: each `listen` entry has at most one node selector
  set; prefixes parse with `netip.ParsePrefix` or `netip.ParseAddr`; a literal
  prefix agrees with `family` when both are set.

### Phase 2: host address inventory

- `lib/network/hostaddrs.go`: `EnumerateHostAddresses(exclude InterfaceFilter)
  []netip.Addr` with the exclusion rules above. Reuse the enumeration in
  `primary/underlay.go` where it overlaps.
- Secondary: send `host_addresses` in `ClusterHello`; re-send a hello when a
  30-second poll observes a changed set.
- Primary: `clusterhandler/session.go handleClusterHello` stores the list via a
  new `Service.SetNodeHostAddresses(identifier, addrs)` in `node_statuses`.
  The primary enumerates and stores its own set on startup and on the same poll.
- `migrations.sql`: `ALTER TABLE node_statuses ADD COLUMN host_addresses TEXT
  NOT NULL DEFAULT '[]'`.
- `NetworkMapInputs` and `LiveState` expose host addresses per node.
- Cluster page: show host addresses per machine beside the underlay address.

### Phase 3: evaluator

- `backend/lib/ingressplan`: `Evaluate(Inputs) Result` with the rules in the
  Evaluation section. Pure function, no store access.
- Reservation construction from `ClusterSettings`: parse `https_web.listen`
  with `net.SplitHostPort`; empty host is wildcard.
- Unit tests: expansion per selector kind, reservation literal versus wildcard,
  same-hostname overlap, HTTPS prefix overlap, cert-source mismatch, host-mode
  warning, inventory-change resolution order, reachability intersection.

### Phase 4: validation

- `deployment_validation.go`: delete the two primary-node rejections and the
  per-node claim maps in `validateNodeNetworkingClaims`; call the evaluator with
  the candidate substituted into `LiveState` and reject on any error.
- `deployment_validation_layers.go`: pass the resolved Web UI listen through
  `inLockValidateDeploymentCreate` and `inLockValidateDeploymentUpdate`.
- `webuihandler/config.go validateResolvedSettings`: run the evaluator against
  the proposed settings and reject when a stored deployment gains an error.
- Return evaluator warnings and reservation exclusions in the update response
  detail so the HCL editor and the API caller see them.
- Tests: extend `https_ingress_validation_test.go` with a primary-node HTTPS
  route that is accepted with a literal `listen`, rejected when the literal
  equals the Web UI address, and accepted with `ipv4()` when the Web UI listens
  on an IPv6 address.

### Phase 5: distribution and data plane

- `netmappublisher/publisher.go render`: run the evaluator and set
  `ingress_publish` on each `ClusterNetMapNode`. Subscribe to `node_statuses`
  changes so host address updates re-render.
- `secondary/network_map.go`: normalise and apply as described. Mixed-version
  window: an older secondary ignores the field and keeps the fallback behaviour.
- `lib/network`: `Dest` on `HostPortRule`, `SetNetproxyPublish`, grouped rule
  rendering, fallback path. `netaudit` key update.
- Primary self-application in `primary/runtime.go`.
- Tests: `hostports_test.go` grouping by port and address; `nft` expression
  rendering for a destination-restricted rule; netaudit round trip;
  `publisher_test.go` publish set per node; secondary normalisation rejects a
  malformed address.

### Phase 6: HCL block form

- `frontend/src/hcl` grammar: confirm nested blocks inside `ingress` parse with
  the existing block rules; add tests in `hcl/index.test.js`.
- `deploymentHcl.js`: renderer emits the block form with default omission;
  parser accepts `ingress` as a block with `https`, `tls_passthrough`,
  `port_forward` children and `listen` sub-blocks; `validateMembers` for each
  block; remove `parseIngress` call-form handling; add the call-form
  diagnostic.
- `deploymentCodeWidget.js`: completion entries for `container_port`,
  `hostname`, `listen`, `node`, `address`, `protocol`, `allow`, `any_node`,
  `any_address`, `ipv4`, `ipv6`; remove `on`-style entries if any were added.
- Networking panel (`deployment-ingress-section`): replace the reservation help
  text with a `listen` summary per route, read-only; editing stays in HCL.
- Tests: round trip spec to HCL to spec for each block kind, default omission,
  `listen` defaults, `https` with `host_port` rejected, call form rejected.

### Phase 7: documentation and end-to-end

- `docs/engineering/networking.md`: Configuration and Ingress Shape sections
  describe `listen`, the evaluator, reservations, and the DNAT destination
  match; remove the reservation sentence.
- `docs/product/deployments.md`: update the `ingress` bullet.
- `docs/future-work/networking.md`: mark the Web UI reservation as replaced by
  reserved claims; keep "Web UI through the proxy" as future work.
- `CLAUDE.md`: index this file.
- `testing-vms/e2e/cases/ingress-listen.js`: an HTTPS route on the primary with
  a literal `listen` for the test address serves; a request to a second host
  address on 443 is not redirected to netproxy; a second deployment claiming the
  same hostname with an overlapping selector is rejected; a host-mode
  deployment on the node yields the warning. `rollover-ingress.js`,
  `https-ingress.js`, and `tls-passthrough.js` pass unchanged.

## Compatibility

- Older secondaries ignore `ingress_publish` and `host_addresses` and behave as
  today. An older primary sends no publish set; new secondaries fall back to
  all-address publishing. Upgrade the primary first to get the new validation.
- Existing deployments have empty `listen` and evaluate to today's set. On the
  primary, routes that were impossible before are now possible; nothing that
  currently validates stops validating.
- The `node_statuses` column add is the only schema change.
- Netproxy needs no release. The `opendeploy` agent binary carries every change.

## Out of scope

- A cluster-level default `listen`. The default is fixed at the scheduled
  node, any address.
- `listen` on `port_forward`.
- `host_port` inside `listen`.
- Block labels (`https "api.example.com" { ... }`). The grammar keeps unlabeled
  blocks; a label form can be added later without changing the proto.
- Declaring bound ports for host-mode deployments, which would turn the
  host-mode warning into a check.
- Serving the Web UI through netproxy.
