<script lang="ts">
  import NetworkDiagram from '$lib/NetworkDiagram.svelte';
</script>

<svelte:head>
  <title>Networking diagram - OpenDeploy</title>
</svelte:head>

<section class="page diagram-page" id="diagram">
  <h1 class="page-title">Networking architecture</h1>
  <p class="lead">Standalone development page for the interactive networking model. Hover or click layers, nodes, tunnels, and flows.</p>
  <NetworkDiagram />
</section>

<style>
  :global(.wrap:has(.diagram-page)) { max-width: 1240px; }
</style>
