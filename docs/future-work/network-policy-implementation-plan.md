# Network policy implementation plan

## Purpose

This document is the implementation plan for the logical network policy
boundary: source anti-spoofing at workload attachments, default
destination-side connectivity rules, and explicit cross-space override
policies. It closes the security gate defined in
[`cross-node-routing-implementation-plan.md`](cross-node-routing-implementation-plan.md)
and implements the Network policy section of [`networking.md`](networking.md).
The shipped dataplane it builds on — addressing, tunnels, routes, netmap
distribution and application on workers and the primary — is documented in
`docs/engineering/networking.md`.

## Current state

Nothing is enforced today. The only nftables usage is NAT
(`lib/network/nft_linux.go`): DNAT for `portForwarding` (with `ipFilter`
source allow-lists) and the v4 egress masquerade. There are no filter-type
chains, no forward-hook rules, and no drop verdicts. `EnsureBase` enables
IPv4/IPv6 forwarding host-wide with nothing filtering forwarded traffic, so
workload-to-workload reachability is unrestricted — same-space and
cross-space, same-node and cross-node — and nothing validates workload source
addresses. No policy shape exists in any proto.

## Fixed design decisions

- Rules are expressed in logical identity terms — space ids, deployment ids,
  ports — never prefixes. Prefix compilation (`SpaceCIDR`, `DeploymentCIDR`)
  is pure derivation in code, the same pattern by which tunnels are derived
  from the netmap rather than expressed in it.
- The default connectivity rules are fixed system intent. They are written
  directly into the code that derives kernel state, are never present in any
  distributed message, and require no distributed inputs beyond what nodes
  already hold (the ULA prefix, local attachments, and the fixed space ids).
- Override policies are global first-class entities with their own storage,
  versions, and history — not part of any deployment spec. A rule's peers may
  be deployments or whole spaces, and later user devices; none of those
  generalizations fit a spec-embedded list, and a rule edit must never touch a
  deployment's config history or trigger a redeploy. Deployments may
  *present* the rules relevant to them, but presentation is derived; the
  policy table is the source of truth.
- Only explicit override policies are distributed, riding `ClusterNetMap` as
  resolved-identity tuples rendered from the policy table.
- Policy evaluates unchanged logical source and destination addresses, before
  local routing or after tunnel decapsulation identically. Underlay addresses,
  tunnel interfaces, and route state are never policy identity.
- Enforcement is stateful. An allow rule means "may initiate connections
  toward"; replies and ICMPv6 errors flow via conntrack `established,related`.
  One-directional rules are one-directional by initiation.
- All filter state lives in the existing `opendeploy` nftables tables and is
  rendered by the same full flush-and-rebuild transaction as the NAT rules,
  under the network manager's lock. There is never a window with routes but no
  policy, or DNAT but no policy.
- The forward-chain policy is `accept`. Enforcement is expressed as explicit
  rules scoped to opendeploy interfaces and the cluster prefix; host traffic,
  host firewalls, and non-cluster flows are untouched.
- Destination-side deny binds to delivery into local workload attachments
  (interface match), never to blanket prefix pairs: a cross-node packet
  transits the sender's forward chain toward a tunnel and must not be dropped
  there.

## Default rules

Implicit, derived per node from `containerNets` (veth, `I`, `O`, `V4`; the
attachment's space decodes from `I`) plus the ULA prefix and the fixed space
ids (`0` `_system`, `1` `global`).

Source anti-spoofing, per attachment: packets entering from a workload veth
may only carry that run's assigned `I` or `O` (and its assigned machine-local
v4 on the v4 side); everything else is dropped. During rollover both
attachments share the non-preferred `I` and have their own `O`. This is the
root of trust for every address-based rule below.

Destination ingress, evaluated before delivery into a local attachment, for a
destination deployment `D` in space `S`:

| # | Rule | Reason |
|---|---|---|
| 1 | allow `established,related` | replies, DNS answers, ICMPv6 errors (PMTU) |
| 2 | allow source space `S` `/64` → `D` | same-space default |
| 3 | allow any → `global` (space 1) deployments | global space is shared; mirrors own-or-global reference locality |
| 4 | allow any → netproxy `/88` on `udp/53` + `tcp/53` | DNS; scoped to the netproxy deployment (fixed identity `(0, opendeploy-net)`), not the space, and to the DNS port |
| 5 | allow `_system` (space 0) `/64` → `D` | netproxy initiates ingress backend dials; only the system space carries network privilege, never `global` |
| 6 | allow explicit override rules targeting `D` | from the distributed policy rules |
| 7 | drop source ∈ cluster `/48` → `D` | default deny, cluster-sourced only |

Non-cluster sources fall through rule 7 and are accepted: externally DNAT'd
`portForwarding` traffic and terminated-ingress traffic are governed by their
own publication mechanisms, not workload policy. Egress has no rules; internet
egress stays open.

Rule 4 means any future `_system`-space service reachable by workloads
requires its own explicit default — a deliberate code change, not inheritance
from a space-wide allow. Rule 3 makes global-space placement a network-level
publication decision and must be documented as such on the spaces surface.

The machine-local IPv4 path is closed in the same change: established accept,
per-attachment v4 anti-spoofing, and a drop for traffic from a workload veth
to the machine-local v4 range. Container-to-container v4 would otherwise
bypass the logical boundary entirely; v4 remains an egress-only path.

## Kernel shape

One filter chain per family in the existing `opendeploy` tables, hook
`forward`, priority filter, policy `accept`. Dispatch by interface, rules by
logical prefix:

```text
chain forward:
    ct state established,related accept
    iifname <veth_X> jump wl_src_<X>      one per attachment
    oifname <veth_X> jump wl_dst_<D>      one per attachment, chain shared per deployment

chain wl_src_<X>:                         anti-spoofing
    ip6 saddr { I(X), O(X) } return
    counter drop

chain wl_dst_<D>:                         destination policy, deployment D in space S
    ip6 saddr <S /64> accept
    ip6 daddr <netproxy /88> udp dport 53 accept        (netproxy's own chain)
    ip6 saddr <space0 /64> accept
    ip6 saddr <global /64 sources per rule 3 shape> accept
    ip6 saddr <override /64 or /88> [tcp|udp dport ...] accept
    ip6 saddr <cluster /48> counter drop
```

Interface-keyed dispatch makes rollover exact: old and candidate attachments
each get their own `wl_src` chain with their own `O`, and delivery policy
follows whichever attachment host routing selects. The `counter` on drops is
kept as cheap diagnostics (visible via `nft list` and netaudit). If
per-deployment chain counts ever matter, dispatch moves to a `daddr` verdict
map without changing the model.

## Override policies

### Storage

A dedicated `network_policies` table, following the existing entity patterns:
per-row optimistic-concurrency version, an append-only history log carrying
the acting user, soft delete, and — because these rows are netmap render
inputs — every write advances the global write sequence in its own
transaction (the publisher rejects content changes without a sequence
advance).

```text
network_policies:
  id                 never recycled
  version            optimistic concurrency
  action             ALLOW (DENY schema-reserved; rejected on writes)
  src_space_id       exactly one of the two src fields is set
  src_deployment_id
  dst_space_id       exactly one of the two dst fields is set
  dst_deployment_id
  ports              empty = all ports and protocols
```

Peers are single-id anchors, never `(space, deployment)` pairs: a deployment
peer stores the deployment id alone and its space resolves at render time, so
space moves follow automatically; a space peer stores the space id alone.
This makes both peer kinds move-stable and makes `allow space A → space B`
a first-class rule shape. Ids are never recycled, so a rule referencing a
deleted entity compiles to permanently vacant prefixes — fail-closed until
cleaned up.

Deny follows the `ipFilter` precedent: the action exists in the schema from
day one, is defined to win over allow regardless of specificity (no
deployment-level-allow-beats-space-level-deny ordering), and is rejected on
writes until implemented — a restriction can never be saved silently
unenforced. The evaluation order is fixed now so no semantics change later:
explicit deny → explicit allow → implicit defaults → default drop. Deny can
then also override defaults (block a same-space peer, close the global-space
allow, narrow ports on an otherwise-open peer). Deny inherits `ipFilter`'s
mixed-version discipline: an old agent ignoring a deny is an unenforced
restriction, so deny distribution must be gated on all agents understanding
it — unlike allows, where an ignoring agent merely preserves default deny.

### API and authorization

Rule writes require update access on the destination's space: the destination
consents to being reachable. This re-imposes by authorization what
spec-embedded rules would have enforced structurally. Naming a source in
another space requires view access on that space (the same
existence-disclosure boundary as other cross-space references). Validation on
write: referenced entities exist, exactly one anchor per peer, source and
destination not the same space when both are space peers, and
deployment-peer rules whose source and destination resolve to the same space
are rejected as redundant with the same-space default.

### Presentation

A global policies page is the source of truth and edit surface (and later the
natural home for user-device grants). The deployment side panel shows a
derived view — rules whose destination is the deployment or its space, and
rules where it is the source — linking back to the global page. Policies are
not part of the deployment spec, its HCL form, or its config history.

### Wire model

`ClusterNetMap` gains policy rules rendered by the primary from the policy
table:

```proto
message ClusterNetMap {
  ...
  repeated NetPolicyRule policy_rules = 8;
}

message NetPolicyRule {
  NetPolicyPeer source = 1;
  NetPolicyPeer destination = 2;
  repeated NetPortMatch ports = 3;   // empty = all ports and protocols
}

message NetPolicyPeer {
  int32 space_id = 1;
  int32 deployment_id = 2;           // 0 = the whole space
}

message NetPortMatch {
  NetProtocol protocol = 1;          // TCP | UDP
  int32 port = 2;
  int32 port_end = 3;                // 0 = single port
}
```

Rendering resolves each stored peer to a wire tuple: a deployment peer
becomes `(current space, deployment id)`, a space peer becomes `(space, 0)`.
Space moves therefore never leave stale rules, and the storage model change
does not alter the wire model at all. The peer shape deliberately
accommodates future non-workload sources (user-device grants) without change.
When deny lands, `NetPolicyRule` gains an action enum (`UNSPECIFIED` invalid,
`ALLOW`, `DENY`); until then the map carries allows only, and the field
number for the action is reserved from the start.

The publisher currently re-renders on node and scheduled-instance updates
only; it gains a subscription to policy-table updates, since a rule edit
changes neither. Rule-only map changes carry a new `derived_from_seq` like any
other content change; the barrier semantics are unchanged and policy imposes
no new barrier requirement (a late allow is a brief deny, a late revoke is a
brief allow — both acceptable).

### Application

`network.Manager` gains `SetPolicyRules`. Workers pass `policy_rules` from the
accepted map alongside `ReconcileTopology`; the primary's in-process applier
does the same. The manager merges explicit rules (filtered to locally
attached destination deployments) with the implicit defaults and renders the
filter chains in the shared nftables rebuild.

Mixed-version behavior is safe by construction: old agents' decoders drop the
unknown field and re-encode without it, so they accept the map and simply keep
not enforcing — the current behavior, never a broken map. Override rules are
not effective until every agent is upgraded; release notes state this, the
same discipline as `ipFilter`.

## Rollout

Enforcement ships on, unconditionally — no setting, no audit mode. Arming
default deny converts a fully open fabric in one upgrade; any cross-space
flow not covered by the defaults breaks at that release and needs an
an override rule. Pre-alpha, this is accepted and called out in the
release notes; the drop-rule counters provide after-the-fact diagnosis. The
only rollout ordering that matters is within a cluster: upgrade all agents
through the release normally — an old agent simply keeps not enforcing until
upgraded, and allows it cannot decode leave it at default-deny-less openness,
never at broken connectivity.

## Milestones

### Milestone: default boundary

- Filter-chain rendering in `nft_linux.go` from manager state: anti-spoofing,
  conntrack accept, default rules 1–5 and 7, the v4 close, per-family, in the
  existing atomic rebuild. Enforced immediately.
- netaudit extended to cover filter chains (desired vs kernel, same
  divergence-recheck model).
- Unit tests: rendered ruleset as a pure function of attachment set and
  prefix, including rollover (two attachments, shared `I`).
- VM end-to-end coverage from the cross-node plan's verification list:
  same-space allowed, cross-space denied, both same-node and cross-node;
  spoofed source dropped; DNS and ingress backend dials unaffected; PMTU
  (ICMPv6 packet-too-big) traverses; v4 container-to-container dropped.

No wire changes. This alone closes the cross-node security gate for the
default boundary.

### Milestone: policy entity

- The `network_policies` table with version, history log, soft delete, and
  global-sequence stamping; the schema-reserved DENY action with write
  rejection.
- CRUD API with destination-consent authorization and peer validation.
- FE: the global policies page (source of truth) and the derived
  policies view on the deployment side panel.
- No enforcement effect yet.

### Milestone: override distribution and enforcement

- `NetPolicyRule` on `ClusterNetMap`; primary render from the policy table
  (deployment-peer spaces resolved at render time); publisher subscription to
  policy-table updates.
- `SetPolicyRules` on the manager; wired from worker session and primary
  applier; merged into the chain render as rule 6.
- End-to-end: cross-space flow denied, allowed after rule addition without
  workload restart, denied again after removal (established flows excepted),
  deployment-peer rule follows a source space move, space-peer rule allows
  all members of the source space.

### Future, out of scope here

- Deny overrides: DENY action enforcement with the evaluation order fixed
  above, gated on all agents understanding deny.
- User-device grants: new `NetPolicyPeer` instances plus access-node default
  rules; no new machinery (see the device-access discussion).
- Compiling rule 5 down to exact ingress backend address/port pairs.
- Workload egress policy, labels as selectors, flow observability (eBPF
  counters as a richer diagnostics surface later).

## Failure behavior

- Rendering is part of the existing full-state nftables rebuild: a failed
  transaction leaves the previous complete ruleset in place, and netaudit
  reports divergence between desired and kernel state.
- A node that misses map updates enforces stale override rules until
  reconnect, bounded by the same session-snapshot recovery as routes. Defaults
  are node-local and never stale.
- Deleting a deployment or space does not cascade to policy rows. A rule
  anchored to a deleted entity compiles to prefixes nobody can hold (ids are
  never recycled) — fail-closed — and the global policies page surfaces it as
  dangling for manual cleanup.

## Completion criteria

- A workload cannot emit packets with another workload's logical source
  identity.
- Same-space traffic is allowed and cross-space traffic is denied by default
  on same-node and cross-node paths, with identical behavior.
- DNS, ingress backend dials, global-space reachability, external DNAT'd
  traffic, and internet egress work with no explicit rules.
- Allow rules take effect and revoke without restarting either workload;
  DENY-action rules are rejected on writes.
- The machine-local v4 path cannot carry workload-to-workload traffic.
- Old agents in a mixed-version cluster accept maps carrying policy rules.
- netaudit detects filter-chain divergence.
