# WireGuard transport implementation plan (phase 1: static keys)

> Status 2026-08-31: fully implemented and rolled out. Phase 1 shipped in
> v0.0.541 (`backend/lib/wgkey`, registration over enrollment and cluster
> hello, map distribution, the transport reconciler, the netaudit WireGuard
> surface, the FE transport-key column, and the e2e "wireguard transport
> checks" step, verified by a clean full-suite run). After every cluster was
> rolled forward, the retained `ip6tnl`/SIT fallback was **removed entirely**
> (the "later cleanup" milestone): WireGuard is the only cross-node transport,
> the reconciler lives in `transport_linux.go`, and a node key is now a hard
> invariant — key load failure blocks boot, `wgkey.ValidatePublic` rejects the
> empty string, and the map renderer, worker map validation, and topology
> conversion all hard-error on a keyless node. The mixed-version pairwise rule
> and the empty-key-means-no-capability convention below are historical. The
> dedicated migration/trust/key-loss e2e cases were never written; migration
> coverage is moot now, key-loss remains worth a case. Known limitation: a
> node whose kernel lacks the wireguard module registers a key and then fails
> transport reconciliation — visible via NetMapStatus and netaudit, with no
> fallback of any kind.

## Purpose

This document is the implementation plan for replacing the fixed `ip6tnl`/SIT
node tunnels with WireGuard as the cross-node transport, phase 1: static
(non-expiring) node keys. Key rotation is phase 2 and is out of scope here
except where phase 1 must leave a door open for it. The transport design
discussion and trade-offs are recorded against
[`networking.md`](networking.md); the shipped tunnel implementation this
replaces is documented in `docs/engineering/networking.md`.

Motivation, in order: the cross-node trust upgrade (cryptographic node-level
source attribution via cryptokey routing, closing the acknowledged
unauthenticated-underlay gap in the policy story), underlay confidentiality,
collapse of the per-peer tunnel netdev mesh to one interface with peer table
entries, and UDP transport (NAT and stateful-firewall traversal that raw
protocol-41 encapsulation fights).

## Fixed decisions

- WireGuard **replaces** `ip6tnl`/SIT rather than becoming an optional second
  transport. Both coexist only during the mixed-version migration window.
- Node private keys are generated on the node, stored as a `0600` file under
  `/var/lib/opendeploy/` beside the TLS material, and never transit any
  channel or enter any database — the primary's own key included. A primary
  restored from backup onto new hardware is a different machine and mints a
  fresh key; transport keys are machine-bound, cluster state is not.
- Public-key-to-node binding rides the existing mTLS channels: enrollment for
  new workers, a cluster-stream report on connect for upgraded nodes. The
  binding is exactly as strong as the per-install CA; no new PKI, no
  trust-on-first-use.
- `nodes.wg_public_key = ''` means "no WireGuard capability". The pairwise
  transport rule is derived deterministically from the map by both ends: both
  nodes have keys ⇒ WireGuard; either lacks one ⇒ `ip6tnl`. No flag day.
- Each peer entry's allowed-ips is exactly the prefix set the map routes to
  that node (`/100`s and `/120`s) — the same derivation as remote routes,
  delivered to a different netlink API. The applied-stamp barrier semantics
  are unchanged; a rollover's `/100` move becomes an allowed-ips move, which
  the kernel applies atomically (assigning a prefix to a peer removes it from
  the previous holder).
- One cluster-wide WireGuard UDP listen port. As implemented it is the code
  constant `network.DefaultWGListenPort` (51833 — deliberately not the stock
  51820, so a host already running its own WireGuard does not collide),
  stamped per node entry into the map as `ClusterNetMapNode.wg_listen_port`;
  workers use their own entry for listening and each peer's entry for the
  endpoint. Carrying it per entry rather than as a global is the phase-2 door
  for per-node dual listeners; promoting the constant to a settings value is
  deferred until someone needs to change it. Persistent keepalive defaults to
  off (underlays are assumed directly routable, as today); a settings knob may
  enable it later for NATed underlays.
- The single-underlay-family constraint is kept for phase 1. WireGuard could
  relax it (per-peer endpoints of either family); that is deferred.
- Phase 2 door: nothing may hard-code "one interface / one key / one port"
  per node. The reconciler models the local transport as a set of (key, port)
  listeners with one active for outbound, even though the phase-1 set always
  has size one. Rotation will run old and new listeners concurrently and use
  the existing barrier for the switch.
- Policy enforcement is untouched: decrypted packets emerge from the
  WireGuard interface into the same forward chain, and `dst_dispatch`
  evaluation is `daddr`-keyed. The anti-spoofing chain of custody gains a
  middle tier (workload-level at the origin veth, node-level at the receiving
  WireGuard interface, deployment-level at the destination chain).

## Database and API changes

- `enrollment_requests` gains `wg_public_key TEXT NOT NULL DEFAULT ''`
  (migration in `backend/storage/primarydb/pq/sql/migrations.sql`); the
  accept flow copies it into the node row, mirroring `underlay_address`.
- The key is carried as versioned node state. As implemented (2026-08-30,
  superseding the original `node_wg_key_log` design), node state was split
  into an identity row (`nodes`) plus append-only `node_versions` rows
  (roles, addresses, `wg_public_key`, allowed spaces), matching the other
  versioned entities: every version append allocates and stamps the global
  write sequence in the same transaction, so map re-render follows
  structurally rather than by write-site convention. The version rows double
  as the key audit history — an unexpected key change is a compromise
  indicator, and a `wg_public_key` transition of `'' → k` is a first set,
  `k1 → k2` a rotation, `k → ''` a retirement — so no separate key log
  exists.
- Enrollment proto: the enrollment request message gains the public key
  field. Cluster proto: a pubkey report message (or field on the existing
  hello/status exchange) for upgraded nodes, and the `ClusterNetMap` per-node
  entry gains `wg_public_key`. Workers validate the field during map
  acceptance (well-formed 32-byte key) alongside the existing underlay
  checks. `NetMapStatus` and the barrier are unchanged.

## Implementation sequence

### Milestone: key material

Node-local keypair generation (Curve25519) at agent first boot, file
persistence, load-or-generate on start. Primary generates its own at install
or first boot through the same code path. No wire or map changes yet; the key
sits unused. Registration writes the key into the node's versioned state,
which is the audit trail.

### Milestone: registration and distribution

The enrollment request carries the pubkey; accept copies it to the node row.
Upgraded workers report their pubkey on cluster-stream connect; the primary
upserts it (advancing the global sequence) and logs the change. The netmap
render includes per-node pubkeys; workers validate and persist them with the
map. Still no dataplane change: this milestone is observable as "the map
carries keys".

### Milestone: dataplane reconciliation

The tunnel reconciler (`backend/lib/network/tunnels_linux.go`) becomes a
transport reconciler: ensure the local WireGuard device exists with the local
private key and cluster listen port (via `wgctrl`/netlink), reconcile the
peer set `{pubkey, endpoint, allowed-ips}` from the accepted map, and point
remote-prefix host routes at the WireGuard device for WG-capable pairs while
retaining `ip6tnl` devices and routes for pairs where either side lacks a
key. Applied-stamp reporting after a clean apply, exactly as today. MTU is
set once for the path accounting for WireGuard overhead (80 bytes over an
IPv6 underlay), and the existing MTU coherence rules extend to it. When a map
shows every node keyed, per-pair `ip6tnl` state is torn down; the `ip6tnl`
reconciliation code is retained until a later cleanup once mixed-version
windows are no longer a concern.

### Milestone: audit and diagnostics

`backend/lib/netaudit` grows a WireGuard surface: diff the live device
config and peer table (pubkeys, endpoints, allowed-ips, listen port) against
desired state, log-only, same 60-second cadence and recheck-once semantics.
Divergence reporting distinguishes "peer missing/extra" from "allowed-ips
drift". Handshake staleness is not audited (sessions are traffic-driven and
idle peers legitimately have none). Node inspection surfaces the pubkey and
per-peer transport selection; the FE nodes page shows the key and whether the
node's pairs are fully on WireGuard.

### Milestone: end-to-end verification

- Existing cross-node e2e cases (netprobe reachability, policy cross-node
  enforcement, cross-node rollover with the barrier) pass unchanged over
  WireGuard — the assertion is that nothing above the transport noticed.
- A migration case: cluster starts mixed (one node keyless), pairs verify
  `ip6tnl` fallback, the node gains a key, pairs converge to WireGuard and
  tunnel devices disappear.
- A trust case: traffic captured on the underlay between nodes is
  ciphertext; an injected forged-encapsulation packet (the old gap) does not
  reach a workload.
- Key-loss recovery: wipe a worker's key file, restart; it regenerates,
  re-reports, and cross-node traffic converges after map propagation.

## Failure behavior

- **Peer with stale endpoint or dead node**: handshakes fail silently and
  traffic toward its prefixes drops, matching today's dead-tunnel behavior;
  liveness remains the cluster stream's job, not the transport's.
- **Key file lost**: the node regenerates and re-reports; until every peer
  applies the map carrying the new key, that node's cross-node traffic
  blackholes. Bounded by map propagation; acceptable for phase 1 (rotation
  machinery in phase 2 makes even this window clean).
- **Reconcile failure**: same posture as routes and nftables — the previous
  applied state stays, the applied stamp is not reported, the barrier holds,
  and the reconciler retries.
- **Primary outage**: workers reconstruct the device and peer set from the
  persisted map and local key file; existing cross-node traffic continues.

## Deferred work

- Phase 2 rotation: pending-key columns (`wg_public_key_next`,
  `wg_next_listen_port`), the dual-listener window, and barrier-driven
  promotion, per the design discussion.
- Per-pair preshared keys (post-quantum hedge): explicit non-goal — pairwise
  N² secret distribution cuts against the netmap model.
- Mixed underlay families per peer; NATed-underlay keepalive defaults.
  (The retained `ip6tnl` code was removed after the v0.0.541 rollout.)
- Wire-capture guidance in the engineering docs (underlay captures become
  ciphertext; capture at the WireGuard device or veths instead).

## Completion criteria

- All cross-node traffic between upgraded nodes flows over WireGuard; no
  `ip6tnl` devices exist on a fully-upgraded cluster.
- The netmap is the single source of pubkeys, endpoints, and allowed-ips;
  netaudit reports sync for the WireGuard surface.
- The e2e migration, trust, and key-loss cases pass in the VM suite.
- `docs/engineering/networking.md` is updated to describe the shipped
  transport (device, cryptokey routing, key custody, migration rule), and
  the unauthenticated-underlay caveats in the engineering and future-work
  docs are removed or scoped to mixed-version windows.
