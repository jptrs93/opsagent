# NetState Split Implementation Plan

**Status: deferred.** Reviewing this plan showed that every consumer-visible
gain — write churn, the DNS empty-answer fix, cert-bundle seq skew, the
wiped-state-dir freeze — could be had inside the existing fused `netstate.pb`
with a far smaller patch, which has since shipped (see below). The split's one
remaining benefit is the architectural boundary itself: `netconfig.pb` as a
pure function of sequenced state, byte-stable under status churn. That
property currently has no consumer, for the same reason the `derived_from_seq`
stamp was dropped from this plan: nothing yet compares, replays, or reasons
about the config artifact across time or nodes. Revisit when the global
event-log work produces such a reader — at that point do the split and the
provenance stamp together, justified by the same consumer.

## What shipped instead (the fused-artifact patch)

All in `backend/app/netproxy`, wire-compatible, no rollout ordering:

- **Diff-gated writes.** `RunNetStateWriter` renders with seq 0, compares the
  encoded bytes against the last written content, and only bumps/writes a file
  when its own content changed. `netstate.pb` and `certbundle.pb` now have
  independent sequences. `SetNetproxyIngress` — which flushes and rebuilds the
  whole nftables NAT table — runs only when the netstate content changed. The
  first write after agent startup is unconditional so ingress forwarding is
  reconciled from an unknown machine state. This removed the
  status-heartbeat-rate rewrite of both files and the nftables churn.
- **Complete DNS catalog.** Every virtual-mode placement contributes its
  `DnsService` entry; readiness gates the endpoint set, not the service's
  presence. The DNS server answers authoritatively for any catalogued name:
  AAAA with answers when endpoints are live, empty NOERROR when the service
  exists but is down (previously the lookup leaked upstream and came back
  NXDOMAIN), and empty NOERROR for non-AAAA query types on known names
  (previously a forwarded NXDOMAIN for A could contradict the AAAA answer).
  Old netproxy binaries reading the new file see zero-endpoint services, find
  no answers, and forward upstream — exactly the old behaviour.
- **Wall-clock seq floor.** Artifact sequences seed at
  `max(persisted seq, unix-millis)`. Netproxy runs in a separate process whose
  monotonic seq gates survive an agent state-dir wipe; restarting the count
  low left a running netproxy silently dropping every update until its own
  restart.

Not done: `node_identifier` is still written but unvalidated — netproxy's
process config carries no node identity to check it against, so validation
would mean inventing config for a purely theoretical misplaced-file case.

## Original target design (kept for when a consumer appears)

**`netconfig.pb`** — pure function of sequenced state: ULA prefix, node
identifier, upstream resolvers, ACME challenges, the full DNS catalog, and
ingress route structure with candidate backends keyed by
`scheduled_instance_id`. Rewritten only on config-rate changes, stamped with
the primary's `derived_from_seq` once the stamp can be computed worker-side
(requires a global seq on the assignment/config wire messages).

**`netlive.pb`** — the overlay: `scheduled_instance_id → up` for this node's
placements, its own local counter. Never leaves the node.

**Composition in netproxy:** `catalog ∩ serving ∩ up`; missing overlay =
fail-closed. `certbundle.pb` on the config trigger.

Rollout would use a dual-write bridge (agent writes both formats,
content-diffed), an order-free fallback read in netproxy, and an
operator-gated legacy sweep once every `opendeploy-net` deployment runs the
new reader. Two-file composition must tolerate skew between config and
overlay — a real cost the fused artifact's single atomic write-rename does
not have, and part of why the split waits for a consumer that needs it.

## Invariants (unchanged by the deferral)

- Runner status stays unsequenced and node-local; it reaches sequenced state
  only via scheduler decisions (promotion/drain).
- Liveness never leaves the node (fate-sharing under partition; no primary in
  the local dataplane's crash path).
- Readiness gates endpoint sets, never a service's presence in the catalog.
