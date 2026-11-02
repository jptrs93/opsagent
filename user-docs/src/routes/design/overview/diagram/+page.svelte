<script lang="ts">
  import ClusterStateDiagram from '$lib/ClusterStateDiagram.svelte';
</script>

<svelte:head>
  <title>Cluster state diagram - OpenDeploy</title>
</svelte:head>

<section class="page diagram-page" id="cluster-diagram">
  <h1 class="page-title">Cluster state</h1>
  <p class="lead">Standalone development page for the cluster state model. Hover or click the log, the primary, identities, projections, and flows.</p>
  <ClusterStateDiagram />
</section>

<style>
  :global(.wrap:has(.diagram-page)) { max-width: 1240px; }
</style>
