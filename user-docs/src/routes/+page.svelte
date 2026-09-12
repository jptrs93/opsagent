<script lang="ts">
  import { base } from '$app/paths';
</script>

<svelte:head><title>OpenDeploy Docs</title></svelte:head>

<h1 class="page-title">OpenDeploy documentation</h1>
<p class="lead">The design of OpenDeploy's built-in networking and logging systems.</p>
<div class="cards">
  <a class="card" href={`${base}/networking/`}>
    <h2>Networking</h2>
    <p>Addressing, service discovery, routing, network policy, and ingress.</p>
  </a>
  <a class="card" href={`${base}/logging/`}>
    <h2>Logging</h2>
    <p>Log events, structured payloads, storage, collection, and queries.</p>
  </a>
</div>
