<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import NetworkDiagram from '$lib/NetworkDiagram.svelte';
  import NodeDiagram from '$lib/NodeDiagram.svelte';
  import {
    serviceEnvironment,
    applicationDial,
    dnsNames,
    ingressListener,
  } from '$lib/networking-snippets';
</script>

<svelte:head>
  <title>Networking — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Networking</h1>
<p class="lead">This page discusses the design of OpenDeploy's networking.</p>

<!-- ============================================================ -->
<section id="goals" class="page">
<h2>Overview</h2>

<p>OpenDeploy provides a batteries-included networking implementation aimed at serving production traffic out of the box, without the need to install any additional supporting infrastructure like reverse proxies, certificate managers, or service discovery solutions.</p>

<p>The interactive diagram below illustrates OpenDeploy's networking architecture.</p>

<NetworkDiagram />

<p>There is a single cluster-wide virtual private network. Spaces, a partition and trust boundary, own a prefix within the network address space. Every deployment instance has a static address, independent of placement, derived from its space, id, and instance ordinal. WireGuard tunnels are used to bridge traffic across nodes.</p>

<p>Deployment instances run as containers in their own network namespace, joined to their node's host network namespace by a veth pair. Nodes own a pruned network map, scoped to what instances on that node can reach, and the node's agent programs that map into ordinary Linux routes, WireGuard peers, and nftables rules. The node agent is not on the packet path.</p>

<p>Traffic between deployments is forwarded by the kernel with its real source and destination addresses intact, from one veth to the other on the same node or through a WireGuard tunnel between nodes. Policy is evaluated once, at the destination, on those addresses: deployments in the same space may talk, traffic between spaces is dropped unless a policy allows it, and egress to the outside world is open and masqueraded through the node.</p>

<p>At the edge, a built-in reverse proxy, netproxy, serves public traffic and answers internal DNS. It configures itself from each deployment's ingress routes, with certificates issued automatically, and dials backends over the same virtual private network. Almost all networking configuration lives inline on the deployment itself, and there are no independent networking-related objects to keep in sync.</p>

<p>The rest of this page takes each stage in turn: <a href="#addressing">addressing</a>, <a href="#discovery">service discovery and load balancing</a>, <a href="#dns">DNS</a>, <a href="#routing">routing and transport</a>, <a href="#rollover">rollover</a>, <a href="#policy">network policy</a>, <a href="#egress">egress</a>, and <a href="#ingress">ingress</a>.</p>
</section>

<!-- ============================================================ -->
<section id="addressing" class="page">
<h2>Addressing</h2>

<p>The cluster owns one private IPv6 prefix: an RFC&nbsp;4193 unique local address block of length <code class="inl">/48</code>, generated once at install and fixed for the life of the cluster. Every workload address in the cluster lies inside it. The 80 bits below the prefix encode identity, and the layout is fixed:</p>

<figure>
<svg viewBox="0 0 880 150" role="img" aria-label="128-bit address layout: 48-bit cluster prefix, 16-bit space, 24-bit deployment, 12-bit instance ordinal, 28-bit outbound discriminator that is all zeros for inbound addresses.">
  <defs>
    <linearGradient id="gPfx" x1="0" x2="1"><stop offset="0" stop-color="var(--inline-bg)"/><stop offset="1" stop-color="var(--inline-bg)"/></linearGradient>
  </defs>
  <!-- boxes: 840px for 128 bits = 6.5625 px/bit, origin x=20 -->
  <rect x="20"    y="30" width="315"   height="48" fill="var(--inline-bg)" stroke="var(--line)" stroke-width="1.5"/>
  <rect x="335"   y="30" width="105"   height="48" fill="var(--accent-soft)" stroke="var(--accent-line)" stroke-width="1.5"/>
  <rect x="440"   y="30" width="157.5" height="48" fill="var(--accent-soft)" stroke="var(--accent-line)" stroke-width="1.5"/>
  <rect x="597.5" y="30" width="78.75" height="48" fill="var(--ok-bg)" stroke="var(--ok-line)" stroke-width="1.5"/>
  <rect x="676.25" y="30" width="183.75" height="48" fill="var(--warn-bg)" stroke="var(--warn-line)" stroke-width="1.5"/>

  <text x="177.5" y="50" font-size="12.5" font-weight="700" text-anchor="middle" fill="var(--sub)">cluster prefix</text>
  <text x="177.5" y="67" font-size="11" text-anchor="middle" class="m svg-sub">48 bits</text>
  <text x="387.5" y="50" font-size="12.5" font-weight="700" text-anchor="middle" fill="var(--accent)">space</text>
  <text x="387.5" y="67" font-size="11" text-anchor="middle" class="m svg-sub">16</text>
  <text x="518.75" y="50" font-size="12.5" font-weight="700" text-anchor="middle" fill="var(--accent)">deployment</text>
  <text x="518.75" y="67" font-size="11" text-anchor="middle" class="m svg-sub">24</text>
  <text x="636.9" y="46" font-size="12.5" font-weight="700" text-anchor="middle" fill="var(--ok)">instance</text>
  <text x="636.9" y="59" font-size="12.5" font-weight="700" text-anchor="middle" fill="var(--ok)">ordinal</text>
  <text x="636.9" y="72" font-size="11" text-anchor="middle" class="m svg-sub">12</text>
  <text x="768.1" y="46" font-size="12.5" font-weight="700" text-anchor="middle" fill="var(--warn)">outbound discriminator</text>
  <text x="768.1" y="59" font-size="11" text-anchor="middle" class="svg-sub">all zeros for inbound addresses</text>
  <text x="768.1" y="72" font-size="11" text-anchor="middle" class="m svg-sub">28</text>

  <!-- prefix boundary ticks -->
  <g class="m" font-size="11" fill="var(--sub)">
    <line x1="335" y1="78" x2="335" y2="96" stroke="var(--line)"/>
    <text x="335" y="112" text-anchor="middle" class="svg-sub">/48</text>
    <line x1="440" y1="78" x2="440" y2="96" stroke="var(--line)"/>
    <text x="440" y="112" text-anchor="middle" class="svg-sub">/64</text>
    <line x1="597.5" y1="78" x2="597.5" y2="96" stroke="var(--line)"/>
    <text x="597.5" y="112" text-anchor="middle" class="svg-sub">/88</text>
    <line x1="676.25" y1="78" x2="676.25" y2="96" stroke="var(--line)"/>
    <text x="676.25" y="112" text-anchor="middle" class="svg-sub">/100</text>
    <line x1="860" y1="78" x2="860" y2="96" stroke="var(--line)"/>
    <text x="860" y="112" text-anchor="middle" class="svg-sub">/128</text>
  </g>
  <text x="335" y="136" font-size="11" text-anchor="middle" class="svg-sub">cluster</text>
  <text x="440" y="136" font-size="11" text-anchor="middle" class="svg-sub">space</text>
  <text x="597.5" y="136" font-size="11" text-anchor="middle" class="svg-sub">deployment</text>
  <text x="676.25" y="136" font-size="11" text-anchor="middle" class="svg-sub">instance</text>
</svg>
<figcaption>The address layout. Each prefix boundary is a level of identity, and routes and policy rules are prefixes at these boundaries.</figcaption>
</figure>

<p>An address is therefore a value that can be computed in advance, before anything is scheduled or started. For a cluster prefix of <code class="inl">fd11:2233:4455::/48</code>, space <code class="inl">prod</code> with id 1, and deployment <code class="inl">payments-api</code> with id 42 (<code class="inl">2a</code> hex), a three-instance deployment has these addresses. Fields are in hex, coloured as in the diagram above:</p>

<table>
  <thead><tr><th>Address</th><th class="mono f-id">space</th><th class="mono f-id">deployment</th><th class="mono f-ord">ordinal</th><th>Written out</th></tr></thead>
  <tbody>
    <tr><td class="nw">Inbound, ordinal 0</td><td class="mono">0001</td><td class="mono">00002a</td><td class="mono">000</td><td class="mono"><span class="f-pfx">fd11:2233:4455</span>:<span class="f-id">0001:0000:2a</span><span class="f-ord">00</span>:<span class="f-ord">0</span><span class="f-disc">000:0000</span></td></tr>
    <tr><td class="nw">Inbound, ordinal 1</td><td class="mono">0001</td><td class="mono">00002a</td><td class="mono">001</td><td class="mono"><span class="f-pfx">fd11:2233:4455</span>:<span class="f-id">0001:0000:2a</span><span class="f-ord">00</span>:<span class="f-ord">1</span><span class="f-disc">000:0000</span></td></tr>
    <tr><td class="nw">Inbound, ordinal 2</td><td class="mono">0001</td><td class="mono">00002a</td><td class="mono">002</td><td class="mono"><span class="f-pfx">fd11:2233:4455</span>:<span class="f-id">0001:0000:2a</span><span class="f-ord">00</span>:<span class="f-ord">2</span><span class="f-disc">000:0000</span></td></tr>
    <tr><td class="nw">Service</td><td class="mono">0001</td><td class="mono">00002a</td><td class="mono">fff</td><td class="mono"><span class="f-pfx">fd11:2233:4455</span>:<span class="f-id">0001:0000:2a</span><span class="f-ord">ff</span>:<span class="f-ord">f</span><span class="f-disc">000:0000</span></td></tr>
  </tbody>
</table>

<p>The service address uses the top ordinal value, 4095, which is reserved for it, so a deployment can have up to 4,095 instances. Each connection to the service address is balanced to one ready instance, as described under <a href="#discovery">Service discovery and load balancing</a>.</p>

<p>The layout fixes the capacities: 65,536 spaces, about 16 million deployments, and 4,095 instances per deployment plus the reserved service ordinal. The discriminator bits leave about a million placement slots and 255 run slots per instance, where a placement is one instance assigned to one node and a run is one container lifetime of that placement. Both wrap, and a live collision on an outbound address is rejected rather than allowed.</p>

<h3>Outbound addresses</h3>

<p>Every instance has a second internal address alongside its inbound address: the outbound address. It shares the instance's prefix, and the 28 discriminator bits, which are all zeros in the inbound address, hold a value unique to the placement and run number. Each container run uses it as the source for the connections it opens, so it is the address other services see when this instance connects to them. It is never published: not in DNS, and not through config references. It lies in the same deployment prefix as the inbound address, so policy rules cover both without distinguishing them.</p>

<p>The pattern exists because during a zero-downtime upgrade, two containers for the same instance ordinal run at once, and each needs its own address for the replies to the connections it opens. How the switch between them happens is described under <a href="#rollover">Rollover</a>.</p>

<h3>What the container sees</h3>

<p>A container has one interface, <code class="inl">eth0</code>, the container end of a veth pair whose other end is on the node. Listing its addresses shows four. Two are the cluster addresses above. The other two exist only to get packets to the node and out to the internet.</p>

<table>
  <thead><tr><th>Address</th><th>Used for</th></tr></thead>
  <tbody>
    <tr>
      <td><strong>Outbound address</strong><br><span class="sub">cluster IPv6, <code class="inl">/128</code></span></td>
      <td>The source of every connection the container opens, whether to another deployment or to the external network. It is the only preferred IPv6 address, so the kernel picks it without being asked.</td>
    </tr>
    <tr>
      <td><strong>Inbound address</strong><br><span class="sub">cluster IPv6, <code class="inl">/128</code>, flagged <code class="inl">deprecated</code></span></td>
      <td>What other deployments, DNS answers, and ingress backends dial to reach this instance. Accepted as a destination and never chosen as a source, so a UDP server that must reply from it should bind it explicitly rather than the wildcard address.</td>
    </tr>
    <tr>
      <td><strong>Link-local address</strong><br><span class="sub"><code class="inl">fe80::/64</code>, generated by the kernel</span></td>
      <td>Reaching the node, which answers as <code class="inl">fe80::1</code> on its end of the veth. The IPv6 default route points there, and it is the same in every container whatever node or space it is in.</td>
    </tr>
    <tr>
      <td><strong>IPv4 address</strong><br><span class="sub"><code class="inl">10.201.0.0/16</code>, one <code class="inl">/30</code> per container</span></td>
      <td>IPv4 <a href="#egress">egress</a> only, with the IPv4 default route pointing at the node's end of the <code class="inl">/30</code>. The range is reused on every node, never routed between nodes, and closed to workload-to-workload traffic by the firewall.</td>
    </tr>
  </tbody>
</table>

<p>The routing table is as small as the address list. There is one default route per family, both via the node, and no route for the cluster prefix or the space prefix. The container cannot tell which destinations are on its own node and which are elsewhere; it hands every packet to the node and the node's routes decide.</p>

<p>The resolver in <code class="inl">/etc/resolv.conf</code> is the inbound address of the node's built-in DNS service, which is itself a deployment in the system space and so is derived like any other address.</p>

<div class="callout">
  <div class="k">Why not a prefix per node?</div>
  <p>The usual alternative gives each node its own prefix and allocates an address per placement, the way Kubernetes hands each node a pod range. Routing then aggregates: one route per node, and the routing table never changes when workloads are scheduled. Deriving addresses from identity gives that up, and gets the following in return.</p>
  <ul>
    <li><strong>No IPAM.</strong> There is no allocator to run, back up, or reconcile after a crash. Two nodes can never disagree about which address belongs to which instance.</li>
    <li><strong>Addresses survive everything but identity changes.</strong> Moving an instance to another node does not change its inbound address. Moving a deployment to another space does, because the space is part of the address. A space move is therefore a deliberate security-domain migration, carried out as a rollover so old-space and new-space placements coexist until the flip.</li>
    <li><strong>Every address is self-describing.</strong> Given any packet capture, the source and destination addresses decode to a space, a deployment, an ordinal, and a run, with no lookup table that can lag or drift.</li>
    <li><strong>Policy compiles to prefixes.</strong> "Same space" is one <code class="inl">/64</code> match. "This deployment" is one <code class="inl">/88</code> match. There is no identity table to distribute.</li>
  </ul>
  <p><strong>What it costs is route aggregation.</strong> An address says nothing about where its instance runs, so each placement is its own route, a <code class="inl">/100</code> or <code class="inl">/120</code>, on every node that can reach it, and each WireGuard peer lists every prefix placed on that peer. Linux route lookup and WireGuard's allowed-ips are both trie-based, so tens of thousands of entries cost nothing measurable. Distribution is bounded the same way: each node receives a map scoped to what its workloads may reach, and a scheduling change republishes only the maps of the nodes whose reachable set it touched.</p>
  <p>The per-node prefix model pays in the other currency: an allocator to run and back up, addresses that change whenever an instance moves, and an identity table that policy and packet captures need before an address means anything.</p>
</div>
</section>

<!-- ============================================================ -->
<section id="discovery" class="page">
<h2>Service discovery and load balancing</h2>

<p>Modern software systems often have many internal services, most of which must talk to each other. That raises three concerns:</p>

<ol>
  <li>How does one service know where to reach another?</li>
  <li>When the target service has several instances, how is traffic distributed across them?</li>
  <li>When a target instance is redeployed to a different node, some routing configuration somewhere must change. How is that done?</li>
</ol>

<p>The traditional answer is DNS. Each service has a stable name such as <code class="inl">payments-api</code>, the connecting code refers to that name, and a DNS lookup at connection time resolves it to an IP address or a set of them. The instance addresses underneath can then be ephemeral and change on every redeployment, which is how Kubernetes treats pod addresses. When they change, the DNS server is updated and new queries return the new addresses. OpenDeploy ships a DNS implementation, but it does not use DNS to answer these concerns, for two reasons:</p>

<ul>
  <li>DNS is not suited to frequent change. Its propagation is lazy and passes through caches outside the cluster's control.</li>
  <li>A string name is useful at the level where humans write configuration. It does not need to be part of the routing information that must change and propagate when an instance moves.</li>
</ul>

<p>OpenDeploy's philosophy is that these concerns belong lower in the networking stack, and that one service talking to another is a dependency that should be captured up front as part of the cluster state. The encouraged approach is to put the target's address in an environment variable in your deployment configuration:</p>

<CodeBlock code={serviceEnvironment} language="hcl" title="Target address in an environment variable" />

<p>Your application then connects to that address directly, skipping DNS completely:</p>

<CodeBlock code={applicationDial} language="typescript" title="Connecting from application code" />

<p>The reference is recorded as a dependency, so <code class="inl">payments-api</code> cannot be deleted or changed in a way that would break it. Because deployment addresses are deterministic and static, the application's view is the same whatever the instance placement. Routing changes happen transparently underneath, as updates to the host routing tables on the nodes that can reach the instance, as described under <a href="#routing">Routing and transport</a>. <a href="#dns">DNS</a> covers the cases where a runtime lookup is needed.</p>

<h3>Load balancing <span class="pill p-plan">planned</span></h3>

<p>Load balancing is also transparent to the application. The application dials the deployment's static service address, and which instance it ultimately connects to is decided underneath, on the sender's node, by one of two Linux kernel networking mechanisms:</p>

<ol>
  <li><strong>Connect hook.</strong> A cgroup hook on the <code class="inl">connect()</code> call rewrites the destination from the service address to the inbound address of one ready instance before any packet exists. The socket holds the decision, nothing is tracked per packet, and the choice dies with the socket. This is the preferred mechanism wherever the host can see the syscall.</li>
  <li><strong>DNAT.</strong> For connections the hook cannot see, such as VM-isolated runtimes and unconnected UDP, an nftables rule on the sender's node rewrites the first packet of the flow in the same way, and conntrack applies that choice to the rest of the flow.</li>
</ol>

<p>The two dispatch themselves by destination address: a connection the hook rewrote already carries a real inbound address, so the DNAT rule never matches it. Translation only ever happens on the sender's node. The service address never crosses a link, and inbound addresses are never translated, so the wire, the tunnels, and the receiving node's policy see nothing but real instance addresses. Selection is uniform over the ready instances. Retries, traffic splitting, and locality preferences belong to the built-in layer&nbsp;7 proxy, as an opt-in. Today a deployment's address reference resolves to the inbound address of its first instance; the service address and both mechanisms are designed but not yet built.</p>
</section>

<!-- ============================================================ -->
<section id="dns" class="page">
<h2>DNS</h2>

<p>Every deployment gets a DNS name automatically, based on its name, and every instance gets a sub-name of it. The pattern is:</p>

<CodeBlock code={dnsNames} language="text" title="Internal DNS names" />

<p>Names are lowercase DNS labels, underscores in deployment names become dashes, and answers are inbound addresses and nothing else. Resolution follows target state: each node's network map carries a DNS catalog listing the established ordinals of every deployment the node's workloads may reach, and the node's resolver answers from it. An ordinal is established when a placement has been told to serve it; runner health is not an input, so a crashed container keeps its records and an instance still warming up has none.</p>

<p>A known service with no established instance resolves to an empty answer, not to "no such name", so a temporarily down service is never cached as nonexistent. A deployment the node's workloads may not reach is absent from its catalog and resolves to "no such name". Internal names never leak to upstream resolvers, and names outside the cluster are forwarded to the node's normal upstream resolvers. Answers carry no locality: preferring a local instance is a balancing decision, so a DNS answer means the same thing wherever it is resolved.</p>
</section>

<!-- ============================================================ -->
<section id="routing" class="page">
<h2>Routing and transport</h2>

<p>The OpenDeploy agent runs in the node's host network namespace, where the underlay interface exists. The diagram below illustrates the components it programs there and the paths packets take through them.</p>

<NodeDiagram />

<div class="callout">
  <div class="k">Host mode is the escape hatch</div>
  <p>In rare cases you may want to run a workload container directly in the host network namespace. A <code class="inl">host</code> networking mode exists for this, beside the default <code class="inl">virtual</code> mode that the rest of this page assumes. Creating or editing a deployment in host mode requires additional permissions.</p>
</div>

<p>Cross-node routes are derived from scheduling alone: a node's network map lists the prefixes routed to every node holding a workload its own workloads may reach, and nothing else. Each node mints its WireGuard key pair at first boot, and only the public key is carried in the map. The initial design targets a full mesh of at most about 100 nodes; a larger cluster needs a different tunnel topology, which would not alter any workload address.</p>
</section>

<!-- ============================================================ -->
<section id="rollover" class="page">
<h2>Rollover</h2>

<p>A rollover replaces a running instance with a new one without changing its inbound address. Because the address is a function of identity, not of the container, the replacement inherits it by definition. The only question is when inbound traffic switches, and that is a route flip.</p>

<p><strong>A placement is in one of three states.</strong> The node's operator treats all three as "run this placement". They differ only in the network state that derives from them.</p>

<table>
  <thead><tr><th>State</th><th>Owns</th><th>Meaning</th></tr></thead>
  <tbody>
    <tr><td><strong>Serving</strong></td><td>The instance's <code class="inl">/100</code> and its inbound address</td><td>Exactly one placement per instance holds this.</td></tr>
    <tr><td><strong>Standby</strong></td><td>Its own <code class="inl">/120</code></td><td>A warming replacement. Its <a href="#addressing">outbound address</a> is routable before it serves. It is absent from DNS and ingress because the inbound address does not route to it yet.</td></tr>
    <tr><td><strong>Draining</strong></td><td>Its own <code class="inl">/120</code></td><td>A superseded placement still running. Replies to work already in flight still reach it after the inbound address has moved.</td></tr>
  </tbody>
</table>

<p>A replacement is created as standby from its very first write whenever a serving placement already exists. Creating it as serving, even briefly, would point the inbound route at a node whose container does not exist yet.</p>

<h3>Same-node rollover</h3>

<ol>
  <li>The old container keeps the inbound route.</li>
  <li>The candidate starts in a new namespace with its own outbound address routed, and the inbound address configured but not routed to it.</li>
  <li>The candidate signals readiness.</li>
  <li>The agent flips the host route for the inbound address to the candidate and moves host-port forwarding across.</li>
  <li>The old container is stopped and its network state removed.</li>
</ol>

<p>The network map does not change. Both placements already point their <code class="inl">/120</code> at this node and the <code class="inl">/100</code> never moves, so the render is byte-identical and nothing is published. This needs no special case; it falls out of deriving routes from assignments. Candidates bind ports inside their own namespace, so there is no host-port contention. Established TCP connections to the old container can still break at promotion; holding and releasing them through ingress is planned.</p>

<h3>Cross-node rollover</h3>

<ol>
  <li>The standby starts on the new node. It owns its <code class="inl">/120</code> immediately, so its outbound traffic is routable, and it does not claim the inbound address.</li>
  <li>The standby reports running. The scheduler moves the old placement to draining and the standby to serving in one atomic commit. The map re-renders with the <code class="inl">/100</code> pointing at the new node. No snapshot can observe the flip half-applied, so no rendered map ever drops the instance's route or its DNS record.</li>
  <li>The scheduler waits until every node holding network state has applied the new map, then tells the draining placement to terminate. A timeout backstops a node that is connected but wedged.</li>
</ol>

<p>Claiming the inbound address is gated on target state, not on local readiness. A node installs the inbound route only once its placement is serving. Without that gate the standby would install the route on its own node while the old node still had one, and traffic originating on each node would reach a different container until the map propagated. If the serving placement dies while a standby is warming, the standby is promoted immediately.</p>

<h3>Multi-instance rollover</h3>

<p>A deployment with several instances rolls one ordinal at a time behind its endpoint set. An ordinal is marked draining, drops out of DNS and balancing, and the remaining instances carry load. The replacement starts, reaches ready, and is marked serving before the next ordinal begins. A new version that never reaches ready halts the rollout with the remaining old instances serving; there is no automatic rollback. Surge mode applies the single-instance candidate flow per ordinal, so it is the same primitive.</p>
</section>

<!-- ============================================================ -->
<section id="policy" class="page">
<h2>Network policy</h2>

<p>A space is the unit of trust. Isolation is enforced by firewall rules rather than by separate networks, so it is one address layout and one route table for the whole cluster with policy layered on top. One space, the global space, exists for cluster-wide services such as DNS: its deployments may reach every space and accept traffic from every space. The defaults need no configuration:</p>

<table>
  <thead><tr><th>Traffic</th><th>Default</th></tr></thead>
  <tbody>
    <tr><td>Between deployments in the same space</td><td><span class="pill p-ok">allowed</span></td></tr>
    <tr><td>To or from a deployment in the global space</td><td><span class="pill p-ok">allowed</span></td></tr>
    <tr><td>Any workload to internal DNS</td><td><span class="pill p-ok">allowed</span></td></tr>
    <tr><td>Any workload to the external network</td><td><span class="pill p-ok">allowed</span></td></tr>
    <tr><td>Between deployments in different spaces</td><td><span class="pill p-no">dropped</span></td></tr>
  </tbody>
</table>

<p>Cross-space traffic is allowed by creating a <strong>network policy</strong>. A policy names a source and a destination, each either a whole space or one deployment, optionally narrowed by protocol and port. Creating one requires update access on the destination's space: the receiving side consents to being reachable. Policies are global entities with their own history, not part of any deployment's config. Enforcement is stateful. An allow means "may initiate connections toward", and replies ride the connection, so there is never a rule for return traffic.</p>

<h3>Two enforcement layers</h3>

<p><strong>Source anti-spoofing at the attachment boundary.</strong> A packet entering the host from a workload's veth may carry only that run's inbound or outbound address as its source. Anything else is dropped and counted. This is the root of trust for every other rule: policy matches on source address, so source address must be enforced, not assumed. Relying on replies to spoofed packets going astray is not enough: UDP needs no reply, audit records must attribute correctly, and an allow list keyed on source address must not be forgeable.</p>

<p><strong>Destination policy before delivery.</strong> A packet about to enter a workload's veth is dropped unless its source is in the same space, an explicit policy allows it, or it is a system path. The rules bind to delivery into local attachments, never to blanket prefix pairs.</p>

<div class="callout">
  <div class="k">Policy evaluates once, at the destination</div>
  <p>A cross-node packet leaves the sender's node unfiltered, crosses the WireGuard link, and is evaluated after decryption on the receiving node. Same-node and cross-node traffic therefore see identical policy, on identical addresses, at the same point.</p>
</div>

<p><strong>Rules compile to prefixes.</strong> Because the address layout is fixed, "same space" is one <code class="inl">/64</code> match and "this deployment" is one <code class="inl">/88</code> match. The rule program on every node is fixed; per-workload variability lives in named sets and a dispatch map, and it is applied in the same atomic kernel transaction as the routes and NAT rules, so there is never a window with routes but no policy. An attachment whose address cannot be decoded fails closed in both directions.</p>

<p>Policies travel to nodes inside the network map as resolved tuples. A rule that names a deleted entity compiles to a prefix nothing matches, so it stays fail-closed until it is cleaned up.</p>

<h3>The attachment boundary and runtimes <span class="pill p-plan">planned</span></h3>

<p>The point where a workload attaches to the host carries one more rule: a flow initiated with the inbound address as its source is rewritten to that run's outbound address at flow birth. A workload that already sources from its outbound address, which the kernel does for any container that sees both addresses, never matches the rule and stays stateless. This turns the outbound-address convention into enforcement: no run, old or candidate, can ever source traffic from an identity it does not currently hold.</p>

<p>The same rule makes every container runtime uniform. A VM-isolated runtime whose guest configures only one address costs one NAT binding per initiated flow and nothing else. Anti-spoofing, destination policy, service balancing, and routes are the same program for every attachment; runtime differences reduce to how the attachment is plumbed.</p>
</section>

<!-- ============================================================ -->
<section id="egress" class="page">
<h2>Egress</h2>

<p>Egress is open by default. The policy boundary governs who may reach a workload and never where a workload may connect to. How a connection leaves depends only on where it is going.</p>

<table>
  <thead><tr><th>Destination</th><th>Path</th></tr></thead>
  <tbody>
    <tr>
      <td>Another deployment</td>
      <td>Routed natively over the virtual private network, on the node or through the WireGuard transport. No translation. The receiver sees the sender's real address, which is what policy matches on.</td>
    </tr>
    <tr>
      <td>The external network</td>
      <td>Masqueraded through the node. IPv6 leaves from the outbound address and IPv4 from the container's machine-local address, and in both cases external services see the node's own address for that family.</td>
    </tr>
  </tbody>
</table>

<p>The virtual private network's addresses are not routable on the public internet, so the node translates both families at its uplink. A node without a global IPv6 address has no IPv6 egress path, and dual-stack clients fall back to IPv4. DNS for external names is forwarded to the node's upstream resolvers.</p>

<p><strong>IPv4 is egress-only.</strong> There is deliberately no IPv4 path between workloads. Service-to-service traffic is IPv6 on the virtual private network, where addresses are stable and policy applies, and the firewall closes the IPv4 range to workload-to-workload traffic so that the invariant cannot be bypassed.</p>
</section>

<!-- ============================================================ -->
<section id="ingress" class="page">
<h2>Ingress</h2>

<p>Ingress publishes a service to the external network. Three mechanisms exist, and they are chosen by what the traffic is rather than by where it lands. All three are declared in the deployment's <code class="inl">network</code> block and all three are derived into kernel rules and proxy state by the agent.</p>

<table>
  <thead><tr><th>Mechanism</th><th>Layer</th><th>What it does</th></tr></thead>
  <tbody>
    <tr><td class="mono">port_forward</td><td>Raw TCP/UDP</td><td>Maps a host port straight to a container port with an nftables DNAT rule, optionally restricted to an allow list of source addresses. For non-HTTP protocols.</td></tr>
    <tr><td class="mono">tls_passthrough</td><td>TLS, routed by SNI</td><td>The built-in proxy reads the hostname from the TLS handshake and relays the raw stream to the container without decrypting it. Many hostnames share one port; the service owns its own certificate.</td></tr>
    <tr><td class="mono">https</td><td>HTTP, terminated</td><td>The proxy owns TLS with automatic certificates, routes by hostname and longest path prefix, and redirects HTTP to HTTPS. Port 443 is shared with passthrough; the hostname in the handshake selects which handling applies.</td></tr>
  </tbody>
</table>

<p>Certificates for terminated routes are issued centrally on the primary and stored as versioned secrets. Private keys reach a node in a separate, restricted bundle beside the proxy's state and never enter the state file itself. Challenge answers are distributed so that any node can answer them without holding the account credentials.</p>

<h3>Listen selectors</h3>

<p><strong>Where a route is published is a selector over nodes and addresses.</strong> Each node reports the global unicast addresses on the interfaces it does not manage. A route's <code class="inl">listen</code> selectors are evaluated against that inventory: a node selector, defaulting to the node the deployment is scheduled on, crossed with an address selector, defaulting to every address. The result is a concrete set of address and port pairs per node, and the same evaluator runs at save time and at publish time so the answer given when the config is saved cannot differ from what nodes receive.</p>

<CodeBlock code={ingressListener} language="hcl" title="Ingress listen selector" />

<p>The Web UI's own listener is a reserved claim in the same evaluation rather than a blanket reservation of port 443 on the primary. A route can publish beside it on another address; a route that would land on it exactly is an error, and a wildcard that expands onto it is dropped from the publish set with a warning. Two deployments whose expanded claims share a node, address, port, and hostname collide, and the collision is rejected at save. The DNAT rules that admit traffic match on destination address, not just port, so a restricted listener genuinely publishes only where it says.</p>

<h3>Multi-node ingress</h3>

<p>Ingress backends come from the node's DNS catalog and can live on any node. The proxy dials a backend on another node over the virtual private network, so a route with <code class="inl">any_node()</code> publishes on every node that can reach its backend without a config change. Public DNS holds one record per ingress node, and DNS round robin is the availability model. Floating addresses and managed-DNS health checks are underlay concerns and stay out of scope. The Web UI is served as a route through the same proxy.</p>
</section>
