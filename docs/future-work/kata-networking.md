# Kata-backed workload networking

Design note for adapting the built-in networking plan to a containerd + Kata Containers + Cloud Hypervisor runtime direction. This note extends [networking.md](networking.md). It records the current conclusion and the options considered while the networking work is still open.

## Conclusion

Use containerd as the image store and task API, and use Kata Containers with Cloud Hypervisor as the VM-backed runtime for isolated workloads.

The preferred long-term networking model is:

```
OpenDeploy-owned routed L3 networking
+ runtime-specific workload attachments
+ Kata netns/veth to TAP redirection
+ DNS-first service discovery
```

OpenDeploy should own L3 identity, routes, fixed node tunnels, nftables, DNS state, endpoint state, and policy. Runtimes should only provide a way to attach a workload to the host dataplane.

## Runtime decision

Firecracker is not the preferred path. Raw Firecracker would require OpenDeploy to build a VM runtime: OCI image conversion, guest init, logging, readiness, storage sharing, network setup, and VM lifecycle management.

Kata Containers is a better fit because it preserves the OCI/containerd model:

```
OpenDeploy -> containerd -> containerd-shim-kata-v2 -> Cloud Hypervisor VM -> Kata agent -> workload
```

This keeps the existing `containerImage` and `nixDockerBuild` source model viable. The deployment still points at or produces an OCI image, while the runtime boundary changes from a runc container to a VM-backed container.

## Current networking plan pressure points

The current networking plan is container-oriented in several places:

| Area | Current shape | Kata issue |
|---|---|---|
| Attachment | OpenDeploy creates a netns and veth, then containerd/runc joins the netns | Kata translates a container netns/veth into a VM TAP path |
| Promotion | Both addresses are preassigned; flip the stable inbound route and retain preferred outbound `O` | Kata must preserve address configuration and source preference without guest mutation |
| Diagnostics | Inspect `/proc` inside a container netns | VM guest inspection needs Kata agent, exec, or a different signal |
| Host networking | Container joins host network namespace | Kata does not support host networking |
| Readiness | Host Unix socket bind-mounted into the container | Works only if the mount is supported; vsock or TCP may be cleaner later |
| eBPF service balancing | Host `cgroup/connect6` sees container connect calls | Kata connect calls happen in the guest kernel |

The design should replace `container network` terminology with `workload network attachment` terminology.

## Runtime-neutral networking pieces

These components should not depend on runc or Kata:

| Component | Responsibility |
|---|---|
| ULA address derivation | Stable inbound `I` and run-scoped preferred outbound `O` under the clean workload ABI |
| Endpoint state | `READY`, `DRAINING`, `DOWN` membership in endpoint sets |
| Host routes | Route local workload addresses to the active attachment |
| nftables | Host ports, IPv4 egress masquerade, anti-spoofing, policy |
| Fixed `ip6tnl` or SIT interfaces | Cross-machine IPv6-in-IPv6 or IPv6-in-IPv4 transport between hosts |
| DNS netstate | Service discovery from endpoint sets |
| Ingress routing state | Public route claims, backend endpoint selection, ACME material distribution |

The runtime-specific piece is only the attachment:

| Runtime | Attachment |
|---|---|
| runc | netns + veth, container joins the netns |
| Kata + Cloud Hypervisor | netns + veth as containerd-facing contract, Kata redirects traffic to VM TAP/virtio-net with TC filters |

Kata's documented default model uses TC redirection between the container-facing veth and the VM TAP. This preserves the host-side veth/netns contract while running the workload inside a VM.

## Preferred datapath

Use routed L3 as the default datapath.

Same-machine packet path:

```
workload -> virtio-net -> Cloud Hypervisor TAP -> Kata TC redirect -> host veth -> host route -> target attachment
```

Cross-machine packet path:

```
workload -> host attachment -> host route -> fixed node tunnel -> remote host route -> remote attachment -> workload
```

This preserves the core goals from `networking.md`:

- The primary remains control plane only.
- Existing traffic does not depend on the primary.
- The host kernel remains the dataplane.
- Inner packets carry unchanged workload addresses, not service NAT addresses.
- Debugging uses `ip`, `nft`, and containerd/Kata tools.

## Rollover direction

Avoid guest network mutation on the promotion critical path. The implemented
runc lifecycle already has the runtime-neutral shape Kata should preserve:

1. Derive `I = Address(prefix, space, deployment, ordinal, 0, 0)` and a unique
   `O` whose placement and run slots are nonzero normalized values.
2. Assign both before workload start. Configure `I` with `preferred_lft=0` and
   keep `O` preferred.
3. Route `O` for the run's full lifetime. Use the `I` host route to decide which
   attachment receives stable inbound traffic.
4. Promote by flipping only the `I` route to the candidate attachment.
5. Keep `O` preferred after promotion, then stop and remove the old attachment.

The old and candidate workloads may both have `I` configured because their
links are isolated. The host route controls reachability. Anti-spoof filters
allow only the `I` and `O` assigned to each attachment. DNS, endpoint state,
ingress, and Address refs use `I`, never `O`.

This keeps promotion host-local and fast for both runc and Kata.

## Networking options considered

### Option 1: routed L3 with stable and run addresses

This is the recommended default.

Every workload run receives stable inbound `I` and run-scoped outbound `O`.
The host routes `I` to the active attachment and routes each `O` for that run's
full life. `O` is preferred before and after promotion; the route flip changes
only which attachment receives `I`.

| Property | Outcome |
|---|---|
| Performance | Lowest-overhead default path; no conntrack or userspace hop for east-west traffic |
| Kata fit | Compatible if addresses are installed before workload start |
| Rollover | Host route flip, no guest mutation required during promotion |
| Debugging | Simple routes and attachment state |
| Risk | Requires source-address controls and validation with Kata address mirroring |

### Option 2: host VIP DNAT from stable to run address

The workload only owns a run address. The host owns the stable address and DNATs stable-address traffic to the active run address.

| Property | Outcome |
|---|---|
| Performance | Adds conntrack and NAT on stable-address traffic |
| Kata fit | High compatibility; guest only needs one address |
| Rollover | Strong drain behavior; existing flows can stay mapped to old run address |
| Debugging | More state in conntrack and nftables |
| Risk | Conflicts with the `no NAT-based service translation` principle |

This is useful for host ports, ingress entry, and possibly a targeted graceful-rollover mode. It should not be the universal east-west datapath.

### Option 3: pure run-address endpoints

Each run receives a unique routable address. DNS only returns `READY` run addresses. There is no stable instance address in the datapath.

| Property | Outcome |
|---|---|
| Performance | Fast routed path |
| Kata fit | High compatibility |
| Rollover | DNS and endpoint-set update only |
| Debugging | Simple per-run routes |
| Risk | Loses stable per-instance identity and increases netmap churn |

This is simpler than Option 1 but weakens the identity model in `networking.md`.

### Option 4: CNI or bridge-based networking

Use existing CNI plugins and a bridge-oriented network model.

| Property | Outcome |
|---|---|
| Performance | Worse than routed L3; bridge and plugin overhead |
| Kata fit | Matches common Kata and Kubernetes deployment patterns |
| Rollover | Harder to make OpenDeploy-owned and route-flip based |
| Debugging | State split between OpenDeploy, CNI, containerd, and Kata |
| Risk | Violates the batteries-included, no-plugin principle |

OpenDeploy should avoid full external CNI as the product boundary. A small internal CNI-like attachment adapter is acceptable if it helps interoperate with Kata.

### Option 5: userspace proxy for all service traffic

Route all service traffic through an OpenDeploy proxy.

| Property | Outcome |
|---|---|
| Performance | Highest overhead; extra user/kernel crossings and copies |
| Kata fit | Runtime-neutral |
| Rollover | Strongest HTTP drain and retry behavior |
| Debugging | Proxy is central observability point and central failure point |
| Risk | Becomes a bottleneck and changes protocol semantics |

This is appropriate for ingress and opt-in L7 east-west traffic. It should not be the default datapath.

## Load balancing conclusion

The `networking.md` plan leaves future service virtual addresses outside the
workload ABI and requires a separate allocation and design. One possible
consumer is host eBPF `cgroup/connect6` balancing, but that approach is
runc-centric because the host sees container `connect()` calls. With Kata, the
workload `connect()` happens inside the guest kernel, so host `cgroup/connect6`
does not naturally see it.

The Kata-compatible mechanism is sender-side service DNAT at the attachment
boundary, layered under the connect hook and self-dispatching by destination
address — see `service-balancing-and-attachment-nat.md`, which records the
agreed design (this section's "deliberate tradeoff" is that design's DNAT
rung, scoped to the service range only).

Preferred long-term stance:

- DNS endpoint-set balancing is the default internal service discovery mechanism.
- No service virtual address is allocated until there is a separate design; the implementation is the connect hook for host-visible workloads with the sender-side DNAT rung as the Kata-compatible fallback.
- Guest eBPF is not a good default because it couples OpenDeploy to guest images and guest kernel capabilities.
- L7 proxying remains opt-in for HTTP semantics.

## Host networking

Host networking is not supported by Kata. It should be modeled as a container-only compatibility escape hatch.

Kata deployments should use virtual networking and explicit `portForwarding` or `ingress` configuration for external reachability. Workloads that require host network namespace semantics, multicast LAN discovery, packet capture, DHCP, or direct interface access should use the runc/container runtime path if mixed runtimes are supported.

## Netproxy services

The current implementation runs DNS and ingress proxy in one `opendeploy-net` internal deployment. Kata changes the tradeoff.

Recommended split to evaluate:

| Service | Preferred placement |
|---|---|
| Networking reconciler | Host agent |
| DNS | Host-side service, or isolated workload if security requires it |
| Ingress proxy | Isolated Kata workload |
| ACME issuance | Primary agent, with certificates distributed through netstate |

Host-side DNS avoids a circular dependency where workload networking depends on a workload-hosted DNS service. It also reduces per-query VM overhead. The ingress proxy is internet-facing and should remain isolated if performance allows.

## Heavy-load considerations

Expected east-west datapath cost, from lowest to highest:

1. Routed L3 to veth/TAP.
2. Host DNAT to a run address.
3. Userspace L4/L7 proxy.

Important bottlenecks:

| Bottleneck | Applies to |
|---|---|
| virtio-net and guest kernel overhead | All Kata workload traffic |
| TC redirect overhead | Kata netns/veth to TAP attachment |
| IP-in-IP encapsulation and underlay | Cross-machine traffic |
| conntrack pressure | DNAT, host ports, service VIPs, IPv4 egress |
| nftables rule scale | Host ports, policies, anti-spoofing |
| proxy CPU and memory | Ingress and L7 east-west traffic |
| guest vCPU saturation | High packets-per-second workloads |

Operational rules:

- Use nftables sets and maps instead of long linear rule chains.
- Avoid conntrack on default east-west traffic.
- Use conntrack where it provides value: host ports, public ingress, IPv4 egress, and targeted graceful rollover.
- Keep fixed node tunnels on the host, not inside each guest.
- Configure MTU once for the full path: guest virtio-net, Kata TAP, veth, fixed node tunnel, and underlay.
- Enable multiqueue or equivalent virtio-net scaling where Cloud Hypervisor and Kata support it.
- Enforce anti-spoofing at the host attachment boundary.

## Implementation guidance

The networking code should expose runtime-neutral operations:

```
SetupWorkloadAttachment(spec) -> Attachment
PublishEndpoint(deployment, ordinal, attachment, addresses)
PromoteEndpoint(deployment, ordinal, old, candidate)
ApplyHostPorts(deployment, attachment, rules)
TeardownWorkloadAttachment(attachment)
```

The attachment should record runtime-specific fields without forcing all runtimes into container terminology:

```
Attachment {
  id
  runtime
  host_link
  netns_path        # runc/Kata containerd-facing contract
  inbound_address
  outbound_address
  machine_local_v4
}
```

The public deployment config should continue to describe workload intent, not runtime mechanics. Runtime support can be cluster-wide or machine-wide initially. Per-deployment runtime selection can be added later if needed.

## Changes implied for networking.md

The existing plan should eventually be revised in these areas:

- Replace `container` terminology with `workload` where the concept is runtime-neutral.
- Replace `netns/veth per container` with `workload attachment`, with runc and Kata implementations.
- Preserve the implemented ROLLOVER invariant: preassign `I` and `O`, keep `O` preferred for the full run, and flip only the `I` route.
- Mark host networking as unsupported for Kata.
- Revisit host `cgroup/connect6` service balancing because it does not naturally apply to Kata guests.
- Consider splitting DNS and ingress proxy placement.
- Document anti-spoofing as a required host-side enforcement point.

## Open questions

- Whether to run the first Kata release as a fixed cluster runtime mode or a fixed machine runtime mode.
- Whether runc remains available for host-network and privileged infrastructure workloads.
- Whether DNS should be host-side from the first networking release.
- Whether `I` and `O`, including `I` with `preferred_lft=0`, can be reliably preconfigured through Kata's current netns mirroring path. Downgraded from a correctness requirement to a performance question by the conditional-SNAT attachment boundary (`service-balancing-and-attachment-nat.md`): a guest that sources `I` on initiated flows is SNATed to its `O` at flow birth, so mirroring fidelity buys per-flow statelessness rather than gating Kata support.
- Whether `portForwarding` should use per-flow DNAT only or nftables maps with deterministic ownership.
- Whether direct TCP rollover should accept broken existing connections or offer an optional DNAT-backed graceful mode.
