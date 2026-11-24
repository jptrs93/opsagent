<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';

  const typeDefinition = `type Space root {
    id: u64
    name: string
}
`;
</script>

<svelte:head>
  <title>Space — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Space</h1>

<section id="content" class="page">
<p>Spaces are an organisational and trust boundary and have the following type definition:</p>

<CodeBlock code={typeDefinition} language="typedef" title="Space" />

<p>A space's name must be non-empty and unique within the cluster, and can be changed at any time. The id is assigned on creation. Two spaces exist from the start: the system space, id 0, which holds the cluster's own services and accepts no user deployments, and the global space, id 1, whose deployments and values are reachable from every other space. Spaces you create are numbered from 2 in the order they are created.</p>

<p>Deployments, secrets, config values, assets, and network policies each belong to exactly one space, and a deployment may only reference values and addresses from its own space or the global space. Traffic between deployments in the same space is allowed and traffic between spaces is denied unless a <a href="{base}/reference/network-policy/">network policy</a> opens it, except that traffic into the global space is allowed from every space. Which space may publish which ingress hostname is set by the <a href="{base}/reference/domain-policy/">domain policies</a>. A node may be limited to a set of spaces it will run deployments for. A space cannot be deleted while any deployment, value, asset, policy, or access rule still uses it.</p>
</section>
