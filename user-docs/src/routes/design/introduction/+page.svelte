<script lang="ts">
  import ClusterStateDiagram from '$lib/ClusterStateDiagram.svelte';
  import { base } from '$app/paths';
</script>

<svelte:head>
  <title>Design introduction — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Design introduction</h1>
<p class="lead">This page introduces OpenDeploy's system design and is the starting point for the other design pages.</p>

<section id="overview" class="page">
<h2>Overview</h2>

<p>You can think of what OpenDeploy does in two parts.</p>

<p>The first part is a state management and distribution system. A global declarative state describes the current and target state of the cluster: the nodes that exist, which workloads should be running where, their environments and configuration, networking, and so on. Participants in the cluster, nodes and users, receive a projection of this global state, a partial view of what is relevant to them. The system tracks which data projects to which participants and how often each piece changes, so that a change fans out only to the participants it affects.</p>

<p>The second part is the reconciliation of the target state by each node. A node fulfils the target state described by its projection, and the end features OpenDeploy offers, running containers, networking, and configuration, are the result. Each node does this independently and locally, and anything that needs coordination between nodes is orchestrated through the cluster state.</p>

<p>Architecturally, OpenDeploy consists of a small agent that runs on each participating node. One of these nodes is designated the primary and acts as the state management control plane: it validates every change, enforces access control, and distributes changes to the nodes they are relevant to. Every other node maintains a connection to the primary. The diagram in the <a href="#state">cluster state</a> section illustrates this architecture.</p>

<p>The primary is a single node, with a simple recovery process a few minutes long to fail over to a replacement. There is a separation between the service OpenDeploy provides as an orchestration platform and whatever service the applications running within it provide. If the primary goes down, workloads cannot be changed or upgraded, but they stay running and serving traffic on their current version. Rare primary downtime does not automatically mean application downtime.</p>
</section>

<section id="state" class="page">
<h2>Cluster state</h2>

<p>The cluster state consists of instances of the resources described in the <a href="{base}/reference/deployment/">reference</a>: deployments, spaces, nodes, secrets, config values, assets, network policies, users, and access rules. Changes are appended to an immutable event log, where one event holds a batch of atomic changes. The log has the full version history of every resource and is designed to be the source of truth and version control for your deployment configuration.</p>

<p>Changes are made by identities, which are nodes or human users, and the primary node decides whether each is valid for the identity making it. The primary is the only identity that stores the whole log; it appends the accepted events and pushes what changed to every identity it is relevant to.</p>

<p>Every other identity holds a partial projection of the state that is relevant to it: a secondary node has the instances scheduled on it and its network map, and a user's browser has what that user's grants allow it to see. The diagram below follows a change from the identity that makes it, through the primary and the log, out to the identities it concerns.</p>

<ClusterStateDiagram />
</section>

<section id="reconciliation" class="page">
<h2>Node reconciliation</h2>

<p>Each node's agent runs a reconciliation loop: comparing its projection with what is actually running on the machine, closing the gap, and reporting its status. The projection is cached on the node, so if the primary is unreachable the node carries on from its last copy.</p>

<p>The agent does this by programming standard components. Containers run through containerd, with cgroups for resource limits, bind mounts for volumes and assets, and environment variables for secrets and config values. Networking uses Linux kernel features: a network namespace and veth pair per container, routes, a WireGuard device, and nftables rules, as <a href="{base}/design/networking/">Networking</a> describes. The agent is on neither the packet path nor a container's execution path: if it stops, containers keep running and the kernel keeps forwarding.</p>

<p>Nodes never coordinate with each other directly. Features that span the cluster, such as the private cluster network, emerge from every node's local work fitting together, and anything that needs ordering across nodes goes through the cluster state. Moving an instance to another node without downtime shows the pattern. The new node starts a standby and reports it running. The primary switches which placement is serving in a single change, which updates the network map of every node that can reach the instance. Once those nodes confirm the new map is applied, the primary tells the old node to stop its container.</p>

<p>A node that cannot reach its target reports the failure as the instance's status: a failed image build, an unavailable secret, or a container that keeps crashing. A crashed container is restarted with a growing delay, and during a rollover a new version that never signals ready is stopped while the old one keeps serving.</p>

<p>Not everything a node produces enters the cluster state. Container logs and resource metrics stay on the node that produced them, and a query is authorised by the primary and answered by the nodes holding the data. <a href="{base}/design/logging/">Logging</a> describes that system.</p>
</section>
