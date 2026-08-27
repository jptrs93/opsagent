# UDP reply source addresses

A wildcard-bound UDP server in a virtual-mode workload sends its replies from
the run-scoped outbound address `O` rather than the stable inbound address `I`
that the client addressed. Clients that use a connected UDP socket discard those
replies. This note records the current addressing behaviour, the defect it
produces, and the options for resolving it.

This note extends [networking.md](networking.md) and
[../engineering/networking.md](../engineering/networking.md).

## Status

No platform change has been made. The adopted direction is workload-side:
a UDP server binds `I` explicitly instead of the wildcard address. A server that
must answer on more than one address sets `IPV6_RECVPKTINFO` and replies from
the received destination address instead. `testexamples/netprobe` binds `I`
(`serveUDP`, `main.go:290`). The platform-side options in this note are recorded
and undecided.

## Current state

### Address pair

Each virtual-mode run holds two IPv6 addresses, assigned before the workload
starts:

```text
I = Address(prefix, space, deployment, ordinal, 0, 0)
O = Address(prefix, space, deployment, ordinal, placementSlot, runSlot)
```

`I` is the stable inbound address. DNS, endpoint state, ingress backends, typed
Address environment references, published host ports, and direct clients use
`I`. `O` is the run-scoped outbound source address.

### Address layout

`backend/lib/network/addr.go:25-49` defines the bit layout:

```text
[ prefix 48 ][ space 16 ][ deployment 24 ][ ordinal 12 ][ placement 20 ][ run 8 ]

SpacePrefixBits      = 64
DeploymentPrefixBits = 88
InstancePrefixBits   = 100
PlacementPrefixBits  = 120
```

`I` sets `placement` and `run` to zero, so `I` is the base address of the
instance `/100`. `O` lies inside the same `/100`. `I` and `O` are therefore
indistinguishable at space, deployment, and instance granularity, and differ
only at placement `/120` or host `/128` granularity.

### Source preference

`addContainerV6Addr` (`backend/lib/network/manager_linux.go:560`) assigns `I`
with `preferred_lft=0` and `O` as preferred. The recorded rationale is that a
warming rollover candidate must not source traffic from `I` before promotion,
because the previous run still holds the host route for `I` and would receive
the replies.

Under RFC 6724 source address selection, rule 3 avoids deprecated addresses, so
the kernel selects `O` whenever it chooses a source itself.

### Promotion

`Manager.Activate` (`backend/lib/network/manager_linux.go:272`) routes `I` to
one run by replacing a host-side route:

```text
dst <I>/128 dev <hostVeth>        # localWorkloadRoute, routes_linux.go:39
```

Activation and rollover promotion do not write to the container network
namespace. Both addresses are already present, so promotion is a single
host-side route replacement.

The agent does read a live container's network namespace at runtime.
`RecoverContainerNet` (`manager_linux.go:214`) calls `findContainerVeth`
(`:231`) and `validateContainerV6Addresses` (`:235`) against running
containers after an agent restart, both through `netlink.NewHandleAt`.

## Defect

### Mechanism

A UDP socket bound to the wildcard address has no per-datagram destination
state. `recvfrom` reports the source only. On reply the kernel performs a route
lookup and runs source address selection, which skips the deprecated `I` and
selects `O`.

TCP is unaffected. An accepted socket takes its local address from the incoming
SYN, so replies carry `I` without application involvement. A UDP socket bound to
a specific address is likewise unaffected, because the socket has a local
address.

A client using a connected UDP socket installs a peer filter. A datagram from
`O` does not match the connected peer `(I, port)` and is discarded by the
client's kernel before the application observes it. The application sees a read
timeout with no error.

### Observed behaviour

From a workload in space 1 to a workload in space 2 on the same node:

```text
connected UDP -> fd78:f7e6:71ce:2:0:1700::        timeout      # I, returned by DNS
connected UDP -> fd78:f7e6:71ce:2:0:1700:0:2f01   reply        # O
```

An unconnected socket receives the reply from either address, because it accepts
datagrams from any source. Diagnostic tools that use unconnected sockets
therefore report the path as healthy while a connected client fails.

### The netproxy DNS resolver is unaffected

The netproxy holds the same address pair (`fd78:f7e6:71ce::600:0:0` deprecated,
`fd78:f7e6:71ce::600:0:601` preferred) and binds DNS to the wildcard address,
confirmed in `/proc/net/udp6`. Its replies are sourced from `I` and connected
clients succeed.

The cause is `github.com/miekg/dns`, which uses `ReadMsgUDP` and `WriteMsgUDP`
with `IPV6_PKTINFO` ancillary data (`udp.go:43,53`). No code under `backend/`
sets `IPV6_RECVPKTINFO`. The behaviour is supplied by the library, not by the
platform.

### Scope

| Path | Affected | Basis |
|---|---|---|
| Workload to workload by `.internal` name or `I` | Yes | Reproduced |
| Published UDP host port | Expected | Code inspection only, untested |
| Workload to workload over TCP | No | Accepted socket carries `I` |
| Netproxy DNS | No | `miekg/dns` sets `IPV6_PKTINFO` |
| Ingress (HTTPS termination, TLS passthrough) | No | TCP only |

Published host ports DNAT to `I` (`hostports.go:159` and
`backend/lib/engine/runner/container.go:1214`, both `TargetV6: cn.InboundAddr`),
and UDP is an accepted forward protocol (`container.go:1226`). A reply sourced
from `O` does not match the conntrack entry, so reverse translation does not
occur. No UDP host port existed in the test cluster, so this path has not been
exercised.

### Secondary failure mode: inbound policy

A reply sourced from `O` does not match the conntrack entry created by the
request, whose reply tuple expects `(I, port)`. The reply therefore misses the
`ct state established,related accept` rule at the head of the `ip6 opendeploy`
forward chain and is evaluated as a new flow against the *client's* inbound
dispatch chain (`ip6 daddr vmap @dst_dispatch`).

A client whose inbound policy accepts the server's source prefix does not
observe this, which is why the socket-level discard is the visible symptom. A
client with a restrictive inbound policy has its replies dropped at the boundary
as well. Correcting the source address resolves both: the reply then matches
`established` and is accepted regardless of the client's inbound rules.

### Effect on the test suite

Two cases in `testing-vms/e2e/cases/network-policy.js` were affected:

- `network-policy-protocol-scoped` asserts the UDP probe succeeds once the
  override allows `udp/9000`. The assertion could not pass while the reply was
  discarded, and the case consumed its full timeout.
- `network-policy-port-scoped` asserts the same probe is denied at
  `stage=response`. The reply was discarded regardless of policy, so the
  assertion passed without testing the boundary.

`testexamples/netprobe` used `net.ListenUDP` on the wildcard address with
`WriteToUDP` for the server role, and `net.DialTimeout` for the probe role, so
it exhibited both halves. The server role now binds `I`, which satisfies the
first case and makes the second fail closed on a dropped request rather than a
discarded reply. The probe role continues to use a connected socket, which is
what makes it an oracle for this class of defect.

## Options

| Option | Implemented by | Workload change | Notes |
|---|---|---|---|
| A. `IPV6_RECVPKTINFO` | Workload | Per-datagram; requires `recvmsg`/`sendmsg` | Standard practice for UDP servers on multiple addresses |
| B. Bind to `I` explicitly | Workload | One call at startup | Adopted. Socket no longer answers on `O`, link-local, or loopback |
| C. Source hint at promotion | Platform | None | Detailed below |
| D. Conntrack translation | Platform | None | Replaces the assigned `I` with a translated address |
| E. Document the limitation | Neither | None | Leaves the stable-name property unavailable to UDP |

Options A and B are alternatives. Either alone is sufficient. A workload needs
`I` to apply option B, which typed Address environment references already
provide.

Options C and D are alternatives to each other, and either removes the need for
A and B.

## Proposal C: source hint at promotion

### Mechanism

Set a preferred source on the container's default route inside the network
namespace, so that source address selection is bypassed:

```text
default via fe80::1 dev eth0 src <I>
```

The route is created without `Src` at `manager_linux.go:543`. A route preferred
source (`rt6i_prefsrc`) overrides selection deterministically.

Raising `preferred_lft` on `I` achieves a similar result but is not
deterministic. With both addresses preferred, RFC 6724 rules 1 through 7 tie,
and rule 8 also ties because `I` and `O` share the leading 100 bits. Selection
then depends on address list order.

### Timing

The hint cannot be applied at setup. A warming rollover candidate that sourced
from `I` would have replies to its own outbound connections routed to the
previous run, which still holds the host route for `I`. The hint must be applied
at promotion and removed at demotion.

### Implementation surface

| Work | Location |
|---|---|
| `Src` on the namespace default route | `manager_linux.go:543` |
| Namespace handle in `Activate` | `manager_linux.go:272`; `NewHandleAt` pattern at `:162`, `:402`, `:431` |
| Re-assert after agent restart | `RecoverContainerNet:214`, alongside `:242` and `:245` |
| Remove the hint at demotion | Demotion path |
| Divergence detection | `audit_state.go:47` currently tracks host-side routes only |
| Tests | `routes_linux_test.go`, `manager_test.go`, and the two e2e cases above |

Estimated effort is half a day for the change and its wiring, and one to two
days including recovery, audit coverage, error semantics, and tests.

### Failure behaviour

If the namespace write fails or lags behind the host route replacement, the
container replies from `O`, which is the present behaviour. The change is
monotonic: it does not introduce a state worse than the current one, and TCP is
unaffected in either case.

This supports making the host route replacement authoritative and the namespace
write best-effort, with divergence reconciled by the audit pass. Promotion then
retains its current latency and failure characteristics. Extending
`audit_state.go` becomes a requirement rather than an option, because an
unreconciled hint is otherwise undetectable.

### Concurrent source risk

A demoted run that retains the hint sources its own outbound traffic from `I`
while the promoted run holds the host route for `I`. Replies to the demoted
run's outbound connections are then delivered to the promoted run. This is worse
than the current behaviour and is the primary correctness requirement of the
proposal.

Removing the hint on demotion is therefore mandatory rather than cleanup. The
size of the exposure depends on whether a demoted run has a drain window in
which it continues to make outbound connections. This has not been determined.

### Policy impact

Network policy rules match `ip6 saddr` at space `/64` and deployment `/88`
granularity. `I` and `O` share the leading 100 bits, so both are covered
identically by any rule at space, deployment, or instance granularity. A
placement-scoped `/120` source would distinguish them; no such rule shape is in
use.

### Interaction with the Kata direction

[kata-networking.md](kata-networking.md) lists promotion as a pressure point:
under a Kata runtime, address configuration and source preference must be
preserved "without guest mutation". Proposal C writes into the workload's
network namespace, which becomes a guest under Kata and would require mutation
through the Kata agent.

Proposal D operates on the host dataplane and would survive that transition
unchanged. This is an argument for D if the Kata direction is pursued.

### Retired property

`Activate` currently documents that "activation and rollover do not mutate the
network namespace". Proposal C retires that property. The property is internal
to `backend/lib/network` and is not part of any workload-facing contract. The
agent already opens live containers' namespaces for reads during recovery, so
the change extends existing access from read to write rather than introducing
it. If the proposal is adopted, the comment at `manager_linux.go:269` must be
updated.

## Proposal D: conntrack translation

### Mechanism

Stop assigning `I` inside the network namespace. Translate on the host instead:

```text
inbound:  client -> I    DNAT to O    delivered in the namespace
reply:    O -> client    conntrack reverse translation    appears as I -> client
```

The container holds only `O`, so its replies are sourced from `O` by
construction and conntrack rewrites them to `I`. No source-selection question
arises, and TCP and UDP behave identically. Promotion retargets the DNAT rule
from the old run's `O` to the new run's `O`, so it remains a single host-side
operation and the property that activation does not mutate the network namespace
survives. That property is proposal D's structural advantage over proposal C.

Workload traffic is already conntracked: the `ip6 opendeploy` forward chain
begins with `ct state established,related accept`. Proposal D adds NAT
translation to tracking that already exists, rather than introducing tracking.

### Costs

**Rollover pinning becomes platform-wide.** Changing a DNAT rule does not
re-evaluate existing conntrack entries, so established flows continue to
translate to the demoted run's `O`. That pinning currently affects host-port and
ingress DNAT only. Under proposal D it governs every workload-to-workload flow.

**UDP correctness becomes timeout-dependent.** Reverse translation holds only
while the conntrack entry lives. Request and reply within that window are
correct. A server that sends spontaneously, or replies after an idle gap longer
than the UDP timeout, emits an untranslated packet from `O`, which is the
present defect reappearing intermittently. Proposal C is stateless and has no
such window.

**`I` stops being a real address in the namespace.** Option B becomes invalid,
because `bind` on `I` would fail with `EADDRNOTAVAIL`. A workload that reads its
own address to advertise it — gossip membership, Raft peers, broker advertised
listeners — advertises the run-scoped `O`, which changes on every run.

**Filter dispatch requires rework.** DNAT runs at prerouting and the filter
forward hook runs afterwards, so policy would see `O` as the destination.
Dispatch is keyed on `I` (`filter.go:153`, `dispatch[cn.InboundAddr] = chain`).
Rekeying it to `O` makes the vmap churn on every rollover instead of remaining
stable per instance.

**Diagnosis becomes indirect.** `ip -6 addr` in the container does not show `I`,
captures show `O`, and conntrack state is required to explain what a client
observes.

### The narrower variant does not work

Keeping `I` assigned and delivered directly, and translating only the reply
source, has no mechanism. The inbound packet is not translated, so the reply
from `O` does not match the conntrack entry's expected reply tuple `(I, port)`
and is classified as a new flow. There is no connection state to key the
translation on, leaving only a stateless rule such as "source `O`, source port
in the service port set, SNAT to `I`", which would also rewrite unrelated
outbound traffic from those ports. Full DNAT is what makes the reverse
translation available.

## Reproduction

With a virtual-mode UDP server on port 9000 in one space and a client in
another, from the client's network namespace:

```bash
ip netns exec <client-netns> python3 -c '
import socket
for dst in ["<I>", "<O>"]:
    s = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM); s.settimeout(5)
    try:
        s.connect((dst, 9000)); s.send(b"ping"); print(dst, "reply:", s.recv(2048))
    except Exception as e:
        print(dst, "failed:", type(e).__name__)
    s.close()'
```

A connected socket to `I` times out and a connected socket to `O` receives a
reply. Use `connect` rather than `sendto`; an unconnected socket succeeds
against both and does not reproduce the defect.

## Open questions

- Whether a demoted run has a drain window in which it makes outbound
  connections, which determines the exposure of the concurrent source risk.
- Whether any component identifies a run by its outbound source address, which
  proposal C would change.
- Whether the published UDP host port path fails as expected.
- Whether the Kata direction is firm enough to prefer proposal D over C.
