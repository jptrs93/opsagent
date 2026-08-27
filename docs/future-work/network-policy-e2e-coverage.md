# Network policy E2E coverage plan

## Purpose

The policy boundary shipped in `implement space reachability` (nftables filter
program, override entity, netmap distribution) and the primary-side applier
shipped in `apply primary netmap` are covered end-to-end by three cases in
`testing-vms/e2e/cases/network-policy.js`: cross-space deny, override allow,
revoke. That proves the happy path on one node. This document designs the
scenarios that close the residual gaps listed in
[`network-policy-implementation-plan.md`](network-policy-implementation-plan.md)
(cross-node denial, cross-node overrides, spoofing, PMTU, the v4 close, a
deployment peer following a space move) plus the gaps that fall out of reading
the implementation: port/protocol scoping, the update path, rollover, netproxy
dials into a non-global space, and kernel-level attribution of a drop.

Shipped behaviour being tested is described in
[`docs/engineering/networking.md`](../engineering/networking.md); the rendering
under test is `RenderFilterState` in `backend/lib/network/filter.go` and its
kernel compilation in `backend/lib/network/nft_linux.go`.

## Weaknesses in the current three cases

These are worth fixing before adding volume, because every new case inherits
the same oracle.

1. **`result=error` is not evidence of a policy drop.** The probe in
   `testexamples/nixdockerbuild1/main.go` logs one line for every failure mode:
   DNS NXDOMAIN, no route, TCP timeout, connection refused, TLS error, 5xx. A
   suite-wide regression that broke `.internal` resolution, or a server that
   never started, would keep `network-policy-cross-space-denied` green. The
   deny assertion needs to distinguish *resolved the name, then the connection
   was filtered* from *never got that far*.
2. **Nothing proves the path works when policy permits it.** The isolated-space
   server is only ever reached across a space boundary. If the space, its
   addressing, or its DNS catalog were broken, the deny case would still pass
   and the allow case would be the only thing that failed — with no way to tell
   which of the two layers is at fault.
3. **The deny is never attributed to a counted rule.** The `wl_dst_<id>` final
   drop carries a counter precisely so a drop is diagnosable; no test reads it.

**Fix 1 — classify probe failures.** Resolve the host explicitly, then dial,
and log the stage:

```text
netprobe probe=<label> stage=dns result=ok addr=fd..:...
netprobe probe=<label> stage=connect result=error err=<class>   # class ∈ timeout|refused|unreachable|other
netprobe probe=<label> result=ok status=200 bytes=…
```

The deny assertion then becomes `stage=dns result=ok` **and**
`stage=connect result=error err=timeout` — a filtered packet is silently
dropped, so timeout is the signature; `refused` or `unreachable` means
something other than the boundary produced the failure and must fail the case.

**Fix 2 — pair every deny with a positive control** (case A1 below).

**Fix 3 — assert the counter** (case C3 below, host-side).

## Harness extensions

### `testexamples/netprobe` (new)

`nixdockerbuild1`'s log lines are asserted by a dozen unrelated cases; grow a
dedicated example instead. One binary, both roles:

- **Probe role.** `NETPROBE_TARGETS` = comma-separated `label=url`. Each target
  is polled on a fixed interval with keep-alives disabled (every attempt must
  be a fresh connection, so a policy change is observable without a restart),
  logging the staged lines above.
- **UDP probe.** `NETPROBE_UDP_TARGETS` = `label=host:port`; sends a nonce,
  waits for the echo, logs `result=ok|error`.
- **Stream probe.** `NETPROBE_STREAM_TARGET` holds one SSE connection open and
  logs `netprobe stream=<label> ticks=<n>` per event — the oracle for
  "established connections survive a revoke".
- **Server role.** `NETPROBE_LISTEN` = comma-separated `tcp/8080`,
  `tcp/8081`, `udp/9000`. TCP serves `/` (identity echo) and
  `/bulk?bytes=N` (N bytes of response body, for the PMTU case). UDP echoes.

Two TCP ports and a UDP port on one deployment is what makes port scoping,
range scoping and protocol scoping single-case assertions rather than
multi-deployment set-ups.

### `helpers/ui.js`

- `updateNetworkPolicy(page, {id, source, destination, ports})` — the Edit
  button path, currently untested.
- `expectNetworkPolicyRejected(page, {…, message})` — form stays open, inline
  error matches.
- `expectNetworkPolicyDangling(page, id)` — the amber badge on the row.
- `expectDeploymentNetworkPolicies(page, name, rows)` — the derived read-only
  view (`data-testid="deployment-network-policies"`), which no case reads.
- `setDeploymentEnvAddressRef(page, {name, machine, envVar, space, deployment})`
  — drives the HCL editor to write `address("<space>", "<deployment>")`, the
  same shape `setDeploymentHttpsRoutes` uses. **Required for every cross-node
  case**: cross-node DNS is not implemented, so a cross-node probe must target
  the literal `I`, and the address env ref is the only supported way to obtain
  it without hardcoding the ULA prefix.

### `testing-vms/test-orchestrator/netpolicycheck.go` (new)

A post-flow step (`c.step("network policy kernel checks", …)`) using the
existing `vmBash` / `vmCombinedOutput` helpers. The Playwright container cannot
read `nft` counters, enter a container netns, or restart an agent; these
assertions have to run on the VM. It self-discovers state — `nft -j list table
ip6 opendeploy` for chains, sets and counters, `ip netns list` plus the
`od<deploymentID>s{0,1}` veth naming for attachments — so it needs no ids
passed from the browser session.

It runs **after all Playwright flows**, because C4 deliberately diverges kernel
state.

## Group A — same-node semantics (`cases/network-policy.js`)

Extends the existing space (`e2e-netpol`) and server. The server becomes a
`netprobe` in server role on `tcp/8080,tcp/8081,udp/9000`; probes are
`netprobe` deployments in global space on worker-2.

| id | proves | mechanics and assertions |
|---|---|---|
| `network-policy-same-space-allowed` | rule 2 (same-space default), and that the deny case's failure is the boundary | second `netprobe` **inside** `e2e-netpol` on worker-2, targeting the server by DNS name. No policy. Expect `result=ok status=200`. This is the positive control for `network-policy-cross-space-denied`; the two must run adjacent. |
| `network-policy-dns-allowed-cross-space` | rule 4 (netproxy `udp/53` + `tcp/53` from any space) | the global-space probe's `stage=dns result=ok` line while its connect stage times out. DNS crosses the boundary; the payload does not. Assert both from the *same* probe so they cannot be satisfied by different deployments. |
| `network-policy-global-destination-open` | rule 3 (global-space destination accepts every cluster source) | a probe **in `e2e-netpol`** targeting an existing global-space httpecho. Expect ok with no policy. This is the only rule with no coverage at all today, and it is the one that makes global-space placement a publication decision. |
| `network-policy-port-scoped` | the override's port match is real, not a blanket accept | allow `global → netpol-server, tcp/8080`. Probe targets both `:8080` and `:8081` on the same server. Expect `8080 ok`, `8081 connect timeout`. |
| `network-policy-port-range` | `PortEnd` compilation (`dport 8080-8081`) | edit the rule to `tcp/8080-8081`; both probes flip to ok. |
| `network-policy-protocol-scoped` | protocol match | rule `udp/9000` only. UDP probe ok, TCP probes denied. Covers the `unix.IPPROTO_UDP` arm, which no test exercises. |
| `network-policy-source-deployment-scoped` | the `/88` deployment source prefix and `DeploymentCIDR` | two probes in global space; rule source = deployment `probe-a`. Expect `probe-a ok`, `probe-b timeout`. Today only space sources are tested, so a bug that widened a deployment source to its whole space would be invisible. |
| `network-policy-space-destination` | the `Destination.DeploymentID == 0` render branch | rule `global → space e2e-netpol`; both deployments in that space become reachable. |
| `network-policy-edited` | the update endpoint publishes (publisher subscribes to policy updates, not just create/delete) and FE optimistic concurrency | Edit the rule's ports; probes flip without a restart of either workload. |
| `network-policy-dangling-fails-closed` | ids are never recycled, an unresolvable peer distributes nothing, FE flags it | create a throwaway deployment, make it the rule's source, delete it. Assert the row shows `dangling` **and** a live probe stays denied — i.e. the vacant rule opened nothing. |
| `network-policy-same-space-rejected` | `NetworkPolicyRedundantErr` reaches the operator | pick the same space on both sides; expect the inline rejection, form still open. |
| `network-policy-derived-view` | the deployment inspector's derived read-only view | open the server's inspector, assert the active rule appears; delete the rule, assert it disappears. |

### Destination consent (authorization)

`network-policy-destination-consent`, added **inside the
`cases/access-enforcement.js` window** while the restricted session and its
virtual authenticator are live (re-establishing that context later is more
expensive than the case). As the `e2e-restricted` space_admin:

- creating a policy whose destination is `e2e-restricted` and whose source is a
  visible space **succeeds** (update access on the destination = consent);
- creating one whose destination is global **fails** with the not-found-shaped
  error (no update access on the destination space);
- the Network page hides policies whose peers it cannot see.

This is the only end-to-end exercise of `requireNetworkPolicyWriteAccess` and
`networkPolicyVisible`. DENY rejection and peer-existence validation stay at
the unit level (`webuihandler/network_policies_test.go`) — the FE offers no
deny control, and bodies are binary protobuf, so driving it from Playwright
would mean hand-encoding a request.

## Group B — cross-node (`cases/network-policy-cross-node.js`, new)

The largest gap: today every policy case is same-node, so nothing proves the
central claim that a packet is evaluated **once, at the destination node, after
decapsulation**, and that the sender's forward chain does not drop it toward
the tunnel. All targets are literal `I` via an `address()` env ref.

| id | proves | mechanics and assertions |
|---|---|---|
| `network-policy-cross-node-same-space-allowed` | the tunnel path itself, and that B2's failure is policy | a second placement of the netpol space on **worker-1** probing the worker-2 server by literal `I`. No policy, expect ok. Positive control for the whole group. |
| `network-policy-cross-node-denied` | destination-side evaluation after decapsulation | global-space probe on **worker-1** → isolated server on worker-2. Expect connect timeout. |
| `network-policy-cross-node-allowed` | policy rules ride `ClusterNetMap` and are applied by the receiving worker | same rule as the same-node case; probe flips to ok with neither workload restarted. |
| `network-policy-cross-node-revoked` | revoke propagates through a map republish, not just a local reconcile | delete the rule; fresh connections time out again (occurrence-count assertion, as today). |
| `network-policy-primary-node-enforced` | `netmapapply.go` — the primary's in-process applier and its `SetPolicyRules` path | put the probe on the **primary** and the server on worker-2, then repeat deny → allow → revoke. The primary applier is new in `apply primary netmap`, has no retry-through-reconnect safety net (it retries on a timer instead), and is currently exercised by unit tests only. |
| `network-policy-cross-node-pmtu` | ICMPv6 packet-too-big rides `established,related` through the boundary | with the allow in force, request `/bulk?bytes=2000000` cross-node through the tunnel (reduced MTU). Assert the probe reports the exact byte count. A missing `related` accept shows up as a stall, not an error, so assert on bytes and not merely on status. |

## Group C — dataplane assertions (orchestrator step)

Each check writes its evidence into the results directory so a failure is
diagnosable from the artifacts alone.

| id | proves | mechanics and assertions |
|---|---|---|
| `spoofed-source-dropped` | `src_ok` anti-spoofing, the root of trust for every address rule | `nsenter` into a workload netns, `ip -6 addr add <peer's I>/128 dev eth0`, then connect to a **same-space** peer sourced from that address. Same-space means policy would allow it; only the `(veth, saddr)` pair check can drop it. Assert the connection fails **and** the forward-chain `iifname @managed … != @src_ok` counter increments. Remove the address afterwards. |
| `v4-container-to-container-dropped` | the machine-local v4 close | from one netns, connect to a peer's machine-local v4 address (read from the peer's netns). Assert failure and an ip-family drop-counter increment, while the v4 *egress* line from `nixdockerbuild1` confirms egress still works. Without this, container-to-container v4 would bypass the logical boundary entirely and no test would notice. |
| `drop-counters-attributable` | a deny is a counted drop in the destination deployment's own chain | snapshot `wl_dst_<serverID>`'s final drop counter, let the denied probe run through ≥2 poll intervals, snapshot again. Assert the packet delta ≥ the number of attempts. Converts group A/B's deny assertions from "it failed" to "the boundary dropped it". |
| `skeleton-persists-across-churn` | the static skeleton is built once and steady-state reconciles only refill sets | after the flow's deployment churn, assert the forward-chain counters are non-zero and strictly monotonic across two reads. A regression that rebuilt the skeleton per reconcile would zero them. |
| `agent-restart-rederives-filter` | filter state is re-derived, not persisted; recovery re-renders recovered attachments | `systemctl restart opendeploy` on worker-2; assert the `wl_dst` chains, `managed`/`src_ok` elements and `dst_dispatch` entries return for every recovered attachment, that a previously allowed probe recovers, and that netaudit logs `in sync` afterwards. |
| `netaudit-divergence-detected` | netaudit sees filter divergence (`missing_filter` / `missing_elements`), and stays log-only | `nft flush chain ip6 opendeploy wl_dst_<id>`, wait two audit cycles (60s each), assert the warn line names the missing filter rules and that the chain is **not** silently repaired. Then force a reconcile (restart the deployment) and assert `in sync` returns. **Runs last** — between the flush and the reconcile that deployment is genuinely unfiltered. |

Assert netaudit lines through the logs UI search rather than raw JSON matching
(the UI hook exposes the shredded message, not the raw line).

## Group D — interactions with other subsystems

| id | proves | mechanics and assertions |
|---|---|---|
| `network-policy-rollover-continuity` | the render's rollover shape (two attachments, shared `I`, distinct `O`, one shared `wl_dst` chain) survives a promotion | with an allow in force, roll the server over (`UPGRADE_ROLLOVER`). Assert no `result=error` occurrence appears between the last v1 `ok` and the first v2 `ok` — the occurrence-count helper already supports this. Unit tests cover the rendered ruleset for this case; nothing covers it live. |
| `network-policy-follows-space-move` | deployment peers are single-id anchors whose space resolves at render time | anchor the rule's **source** to the probe deployment, then move the probe from global to a third space. The destination and the probe URL are unchanged; only the source's addresses change. Assert the probe keeps succeeding — a space-anchored rule, or a render that cached the old space, would break here. |
| `network-policy-ingress-into-isolated-space` | rule 5 (`_system` → D): netproxy dials a backend in another space | give the isolated-space server an HTTPS ingress route and fetch it externally. Every existing ingress case lives in global space, where rule 3 would mask a broken rule 5. |
| `network-policy-port-forward-external-open` | non-cluster sources fall through the cluster-`/48` drop | publish the isolated server's 8080 on a host port; curl it from the Playwright container. Externally DNAT'd traffic must not be governed by workload policy. |
| `network-policy-egress-open` | egress has no rules | give the isolated-space server the existing `OPENDEPLOY_E2E_IPV4_EGRESS_URL` env and assert the egress line. One env var, no new case body. |
| `network-policy-established-survives-revoke` | enforcement is stateful; a revoke is flow-scoped like removing a port forward | with an allow in force, hold an SSE stream (`NETPROBE_STREAM_TARGET`), delete the rule, then assert the stream's tick counter keeps advancing while fresh connections time out. |

## Ordering and placement

```text
… issued-tls
network-policy            (A: space, server, probes, defaults, overrides, FE)
network-policy-cross-node (B: worker-1 → worker-2, primary → worker-2)
network-policy-interactions (D: rollover, space move, ingress, port forward, established)
… remainder of the existing flow (agent upgrades, pgbackrest, …)
──────────── after all Playwright flows ────────────
network policy kernel checks (C, orchestrator step; C6 last)
```

Group A/B/D cases delete the policies they create, so a later case never runs
under an override it did not set. Group D's rollover and space-move cases run
after A and B because they reuse those deployments. Placing B before the agent
upgrade rollout also means the policy path is exercised on both the installed
and upgraded agent versions within one run.

## Deliberately not covered

- **DENY enforcement** — rejected on writes; nothing to observe end-to-end.
- **Mixed-version policy behaviour** — the "older" release the harness installs
  is built from the same tree, so it decodes `policy_rules` like any other
  agent. A true old-agent test needs a pinned real release and belongs with the
  upgrade-compat work, not here.
- **Stale-map enforcement during a primary outage** — worth doing eventually
  (the cached map must keep enforcing while a new rule does not take effect
  until reconnect), but it needs a primary-down window in the middle of the
  flow, which the current harness structures around backup/restore only.
- **User-device grants and workload egress policy** — unimplemented.
