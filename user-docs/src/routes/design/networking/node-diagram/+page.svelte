<script lang="ts">
  import NodeDiagram from '$lib/NodeDiagram.svelte';
</script>

<svelte:head>
  <title>Node diagram - OpenDeploy</title>
</svelte:head>

<section class="page diagram-page" id="node-diagram">
  <h1 class="page-title">Inside one node</h1>
  <p class="lead">Standalone development page for the per-node namespace model. Hover or click namespaces, components, and flows.</p>
  <NodeDiagram />
</section>

<style>
  :global(.wrap:has(.diagram-page)) { max-width: 1240px; }
</style>
