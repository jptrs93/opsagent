<script lang="ts">
  import ClusterStateDiagram from '$lib/ClusterStateDiagram.svelte';
  import { base } from '$app/paths';
</script>

<svelte:head>
  <title>Design overview — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Design overview</h1>
<p class="lead">This page is an overview of OpenDeploy's system design, the first step to understanding its internals.</p>

<section id="overview" class="page">
<h2>Overview</h2>

<p>What OpenDeploy does separates into two parts.</p>

<p>The first part is a state management and distribution system. A global declarative state describes the current and target state of everything: the nodes that exist, which workloads should be running where, their environments and configuration, networking, and so on. Participants in the cluster, nodes and users, receive a projection of this global state, a partial view of what is relevant to them. Key to this part of the system is the rate of change of each piece of information and the dependency graph of which data projects to which participants. When something changes it should fan out to the minimum set of nodes affected by the new information, and unnecessary data churn is avoided in general.</p>

<p>The second part is the end features OpenDeploy offers: running containers, networking, and configuration. Each node achieves these by fulfilling the target state described by its projection of the cluster state. For example, a node's projection includes its network map, the prefixes routed to reachable nodes and their WireGuard keys, and the node's agent programs that map into WireGuard peers, routes, and firewall rules in its Linux kernel. Each node reconciles its own projection independently and locally, yet the features that result, such as the <a href="{base}/design/networking/">private cluster network</a>, are cluster wide: they emerge from every node's local work fitting together. Nodes never coordinate with each other directly. Anything that needs coordination between nodes is orchestrated through the cluster state.</p>

<p>Architecturally, OpenDeploy consists of a small agent that runs on each participating node. One of these nodes is designated the primary and acts as the state management control plane: it validates every change, enforces access control, and distributes changes to the nodes they are relevant to. Every other node maintains a connection to the primary. The diagram in the <a href="#state">cluster state</a> section illustrates this architecture.</p>

<p>The primary is a single node, with a simple recovery process a few minutes long to fail over to a replacement. There is a separation between the service OpenDeploy provides as an orchestration platform and whatever service the applications running within it provide. If the primary goes down, workloads cannot be changed or upgraded, but they stay running and serving traffic on their current version. Rare primary downtime does not automatically mean application downtime.</p>
</section>

<section id="state" class="page">
<h2>Cluster state</h2>

<p>The cluster state is a set of instances of the resources described in the <a href="{base}/reference/deployment/">reference</a>: deployments, spaces, nodes, secrets, config values, assets, network policies, users, and access rules. Changes are appended to an immutable event log, where one event holds all the changes made together, so the log is the full version history of every resource and the version control for your deployment configuration.</p>

<p>Changes are made by identities, which are nodes or human users, and the primary node decides whether each is valid for the identity making it. The primary is the only identity that stores the whole log; it appends the accepted events and pushes what changed to every identity it is relevant to.</p>

<p>Every other identity holds a partial projection of the state that is relevant to it: a secondary node has the instances scheduled on it and its network map, and a user's browser has what that user's grants allow it to see. The diagram below follows a change from the identity that makes it, through the primary and the log, out to the identities it concerns.</p>

<ClusterStateDiagram />
</section>

<section id="content" class="page">
<p class="todo">Enrolment, upgrades, and backups are not written yet.</p>
</section>
