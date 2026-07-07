# Workload addressing and WireGuard routing options

Design note comparing two possible cross-host addressing models for the built-in networking layer. This expands on [networking.md](networking.md) and [kata-networking.md](kata-networking.md).

## Context

The cluster has one generated IPv6 ULA `/48`. Workloads attach to each host through isolated network namespaces and veth pairs. Cross-host traffic is expected to use a host WireGuard mesh.

The key design choice is whether a workload address should encode its current machine placement, or whether placement should remain routing state.

Chosen direction:

1. Start with flat stable workload identity addresses. In the first cross-host implementation, these logical addresses are also the addresses routed by WireGuard, so peer `AllowedIPs` contain workload `/128`s.
2. Keep the internal model explicit about `logical_address`, `machine`, and endpoint `state`, so the first implementation does not permanently assume that logical addresses must always be the transport locator.
3. If WireGuard allowed-ips churn becomes a scale limit, extend the design with implicit machine-scoped routing addresses and host-side logical-to-locator translation around WireGuard. DNS, status, and policy should continue to speak in logical workload addresses.
4. Enforce policy in the logical address space: source anti-spoofing at the workload attachment boundary, destination ingress policy before delivery to a workload, same-space allow by default, and cross-space deny by default.

The two base options are:

1. Machine-scoped locator addresses: workload IPs live under a machine prefix.
2. Flat stable workload identity addresses: workload IPs are derived from deployment identity and routing maps them to the current machine.

## WireGuard constraint

WireGuard `AllowedIPs` are prefix-based CIDR matches. They do not support arbitrary bit-pattern matches.

For outbound packets, `AllowedIPs` answer:

```
destination prefix -> peer public key
```

For inbound packets, they also enforce source ownership:

```
peer public key -> allowed source prefixes
```

`AllowedIPs` can represent `fdxx:...:machine-42::/64` or `fdxx:...:workload-7/128`. It cannot represent "all addresses whose machine id bits are 42" unless those bits are a leading prefix.

The common Go API supports appending allowed IPs to an existing peer, but removing or moving an entry generally means replacing the affected peer's full allowed IP list.

## Option A: machine-scoped locator addresses

Each enrolled machine receives a stable numeric machine id. The address layout puts machine id in high-order bits after the cluster prefix, so every machine owns a contiguous prefix.

Example shape:

```
<ULA /48> : <machine id> : <deployment id> : <ordinal/run>
```

Example ownership:

```
machine A owns fdxx:xxxx:xxxx:000a::/64
machine B owns fdxx:xxxx:xxxx:000b::/64
```

### Routing

Each host programs local `/128` routes for workloads it runs:

```
fdxx:...:machine-B:deployment-7/128 -> dev od7s0
```

Each host programs WireGuard peers by machine prefix:

```
peer machine-A AllowedIPs = fdxx:...:000a::/64
peer machine-B AllowedIPs = fdxx:...:000b::/64
```

When a new workload starts on machine B, other machines do not need WireGuard changes. The new address is already covered by machine B's prefix.

When a workload moves from machine B to machine C, its address changes because the machine prefix changes. DNS and endpoint state must update to the new address.

### DNS

DNS must know current placement:

```
api.prod.internal -> fdxx:...:machine-B:deployment-7
```

If the workload moves:

```
api.prod.internal -> fdxx:...:machine-C:deployment-7
```

Clients with cached old addresses may fail until they re-resolve, unless the system adds a temporary forwarding/override mechanism.

This is similar to Kubernetes pod IPs: pod IPs are locators, not durable identities. Kubernetes hides pod churn behind Services, ClusterIPs, EndpointSlices, and low-TTL DNS.

### Network policy

Network policy must track current endpoint addresses. If deployment A moves machines, the source/destination address set for deployment A changes.

WireGuard proves a packet came from a machine prefix, not from the specific workload that should own a source address. Host-side attachment anti-spoofing is still required. Destination ingress policy should still evaluate logical workload identity, even though the routed address is placement-shaped.

### Pros

- Compact WireGuard state.
- WireGuard config changes mostly when machines enroll, leave, or change endpoints.
- Better scaling for tens or hundreds of thousands of workloads.
- Route aggregation is natural and easy to debug.
- Similar to Kubernetes node PodCIDR models.

### Cons

- Workload IP changes when the workload moves machines.
- DNS and endpoint state must update on placement changes.
- DNS caches and long-lived clients can observe stale IPs.
- Deployment-level network policy churns with placement.
- Stable per-instance IP semantics require an additional abstraction, such as a service VIP, proxy, or temporary route override.

## Option B: flat stable workload identity addresses

Workload addresses are derived from workload identity only. Machine placement is not part of the address.

Current planned shape:

```
<ULA /48> : <kind> : <deployment id> : <field>
```

For example:

```
kind 0 = stable instance address, field = ordinal
kind 2 = run-scoped address, field = run number
```

A deployment instance keeps the same stable address regardless of which machine currently runs it.

### Routing

Each host programs local `/128` routes for workloads it runs:

```
fdxx:...:deployment-7-instance-0/128 -> dev od7s0
```

Other hosts route cluster traffic into `wg0`, and WireGuard `AllowedIPs` map individual remote workload addresses to peers:

```
peer machine-B AllowedIPs =
  fdxx:...:deployment-7-instance-0/128
  fdxx:...:deployment-8-instance-0/128
```

When a workload is created, the hosting machine must become the owner of that workload's `/128` in other machines' WireGuard config.

When a workload moves from machine B to machine C:

```
remove workload /128 from peer machine-B
add workload /128 to peer machine-C
```

The workload address does not change.

### DNS

DNS can derive stable addresses without placement:

```
api.prod.internal -> fdxx:...:deployment-7-instance-0
```

If the workload moves, DNS does not need to change for that stable instance address. Routing ownership changes instead.

### Network policy

Network policy is cleaner because address identity is stable.

Example:

```
deployment B allows deployment A
```

can compile to destination-side host filters that match deployment-derived source and destination prefixes or sets. Moving A or B between machines does not necessarily require policy changes.

WireGuard still does not replace network policy. Same-host traffic bypasses WireGuard, so host-side nftables or eBPF filters are still needed. WireGuard only provides cross-host machine/source ownership. Attachment anti-spoofing remains mandatory because destination policy trusts the logical source address.

### Pros

- Workload stable addresses survive machine moves.
- DNS is simpler and less sensitive to placement churn.
- Cached stable addresses continue to work after routing converges.
- Deployment-level network policy can be identity-based rather than placement-based.
- Cross-host anti-spoofing can be tied to exact workload `/128` ownership.

### Cons

- WireGuard `AllowedIPs` can grow to one `/128` per remote workload.
- Workload creation, deletion, and moves can require WireGuard config updates across machines.
- Removing or moving one address generally requires replacing the affected peer's allowed IP list.
- Large clusters may have significant control-plane update cost.
- Machine drain or mass rollout can create a large burst of WireGuard reconciliation work.

## Scale implications

With 10,000 workloads in the flat model, each machine may have thousands of remote `/128` entries in WireGuard `AllowedIPs`.

Steady-state packet lookup is not expected to be a linear scan; WireGuard keeps an allowed-ips lookup structure. The larger concern is update cost:

- large netlink messages when replacing a peer's allowed IP list
- kernel trie updates
- primary netmap fanout
- worker reconciliation latency
- memory usage for many prefixes

With machine-scoped prefixes, WireGuard state is proportional to machine count instead of workload count. Workload churn moves to DNS, endpoint state, and policy state instead.

The first implementation should still use the flat model because it is simpler and keeps logical workload identity, DNS, and policy semantics aligned. It should be implemented as a routing strategy, not as an assumption baked into every layer.

## Hybrid option: home prefixes plus overrides

A possible compromise is to assign every machine a home prefix, allocate stable workload addresses from the workload's home machine prefix, and use more-specific `/128` WireGuard overrides only when a workload runs away from its home machine.

Normal case:

```
peer machine-A AllowedIPs = fdxx:...:machine-A::/64
peer machine-B AllowedIPs = fdxx:...:machine-B::/64
```

Moved workload:

```
deployment-7 address is inside machine-A home prefix
deployment-7 currently runs on machine-B

peer machine-A AllowedIPs = fdxx:...:machine-A::/64
peer machine-B AllowedIPs = fdxx:...:machine-B::/64
                            fdxx:...:machine-A:deployment-7/128
```

WireGuard uses longest-prefix match, so the `/128` override wins over the home `/64`.

This keeps WireGuard compact in the common case while allowing stable addresses across moves. It adds persistent machine IDs, home-prefix assignment, deployment address allocation/home metadata, and override reconciliation.

This hybrid is not the preferred extension path at the moment. It introduces long-lived allocation/home state and still needs cleanup rules to prevent permanent `/128` override fragmentation.

## Future extension: implicit locator addresses

If flat `/128` WireGuard ownership becomes too expensive, add a host-side identity/locator translation layer while preserving flat logical addresses as the product-facing model.

Each workload has:

```
logical address = deployment-scoped stable identity
locator address = machine-scoped routing address derived from current placement
```

Example:

```
logical L_B = fdxx:...:deployment-B
locator R_B = fdxx:...:machine-B:deployment-B
```

WireGuard then routes only machine prefixes:

```
peer machine-A AllowedIPs = fdxx:...:machine-A::/64
peer machine-B AllowedIPs = fdxx:...:machine-B::/64
```

The host translates packets around the cross-host boundary.

Outbound on machine A:

```
workload A sends:  src=L_A dst=L_B
host maps:         src=L_A -> R_A, dst=L_B -> R_B
WireGuard routes:  dst=R_B via machine-B prefix
```

Inbound on machine B:

```
WireGuard receives: src=R_A dst=R_B
host maps:          src=R_A -> L_A, dst=R_B -> L_B
workload B sees:    src=L_A dst=L_B
```

The full `L -> R` mapping does not necessarily need to be distributed as full addresses. If `R` is derivable from `L` plus current machine id, the distributed placement state can be:

```
logical address L_B -> current machine B
```

Then each host can derive `R_B` locally.

This extension moves churn from WireGuard allowed-ips replacement into a translation map that can be updated one endpoint at a time. It also keeps DNS and network policy on stable logical addresses.

### Policy placement with locator translation

If locator addresses are introduced, policy must stay above the routing translation layer.

Outbound on the source machine:

```
workload emits:       src=L_A dst=L_B
source anti-spoof:    verify L_A belongs to this attachment
optional egress rule: evaluate L_A -> L_B in logical space
translation:          L_A/L_B -> R_A/R_B
WireGuard routing:    route R_B by machine prefix
```

Inbound on the destination machine:

```
WireGuard receives:   src=R_A dst=R_B
reverse translation:  R_A/R_B -> L_A/L_B
ingress policy:       allow same-space, explicit cross-space, or system traffic
workload receives:    src=L_A dst=L_B
```

The policy engine can share endpoint state with the translation engine, but policy rules must not match locator addresses. Locator addresses are topology and routing state; logical addresses plus workload metadata are identity.

The default policy remains:

- Deny workload-to-workload traffic first.
- Allow same-space workload-to-workload traffic on all protocols and ports.
- Deny cross-space traffic unless an explicit policy allows it.
- Allow internet egress by default.

Possible implementation mechanisms:

- nftables maps for logical-to-locator address translation.
- eBPF maps at tc or another host datapath hook.
- Encapsulation instead of rewrite, preserving the inner logical packet and routing an outer machine-scoped packet.

The extension is intentionally deferred. It should be added only if benchmarks show WireGuard `/128` ownership updates are a real bottleneck, or if network policy implementation naturally creates the required logical-to-locator maps.

Design constraints for the first flat implementation:

- Endpoint state should carry logical address and current machine separately.
- DNS should return logical addresses, not assume they are placement locators.
- Network policy should be expressed and stored in logical workload terms.
- Source anti-spoofing should validate logical source addresses at each workload attachment.
- Destination ingress policy should be compiled from logical source/destination identity and metadata, not from machine placement.
- The WireGuard reconciler should be isolated as one routing backend.
- Code should avoid naming that makes logical address and routed locator permanently equivalent.

## Space ids in address derivation

Encoding space id into the logical address layout was considered because it would make same-space policy easy to express as prefix rules.

Example shape:

```
<ULA /48> : <space id> : <kind/deployment/ordinal bits>
```

This is not the preferred v1 layout. Space membership should be compiled into nftables sets from authoritative endpoint state instead. That keeps deployment addresses stable if a deployment changes spaces and avoids making space-id bit allocation part of the address ABI.

A per-space prefix can be reconsidered later if spaces become hard tenant domains and changing a deployment's space is intentionally treated as an address-changing redeploy. Even with per-space prefixes, attachment anti-spoofing and destination policy remain mandatory; address shape is not the security boundary.

## Decision

Use Option B first: flat stable workload identity addresses routed directly by WireGuard `/128` ownership.

This gives the simplest initial cross-host model:

```
DNS returns logical workload addresses
WireGuard AllowedIPs map logical /128s to current machines
network policy is defined over logical workload identities
host attachment anti-spoofing proves packets came from the claimed logical source
```

If scale testing shows WireGuard update churn is too costly, evolve to implicit locator addresses and translation rather than switching product semantics to machine-scoped workload IPs.

## Open questions

- What target scale should the first cross-host implementation support: 1,000, 10,000, or 100,000 workloads?
- How often do we expect cross-machine moves after initial placement?
- Are stable per-instance IPs a product guarantee, or are stable DNS/service names enough?
- Should the first implementation avoid service VIPs entirely, or adopt a Kubernetes-like Service abstraction earlier?
- How expensive is WireGuard peer allowed-ips replacement at 10,000 and 100,000 `/128` entries on target kernels?
- Which translation mechanism is best if implicit locator addresses become necessary: nftables maps, eBPF, or encapsulation?
- At what scale should policy and translation state move from nftables maps to eBPF maps, if ever?
