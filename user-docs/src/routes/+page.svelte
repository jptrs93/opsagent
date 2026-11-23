<script lang="ts">
  import { base } from '$app/paths';
  import { groups } from '$lib/nav';

  const blurbs: Record<string, string> = {
    Guides: 'Step-by-step tasks, from a first install to a running service.',
    'Concepts & Design': 'The ideas behind the system and how each subsystem is built.',
    Reference: 'Every type you can write, field by field, with examples.',
  };
</script>

<svelte:head><title>OpenDeploy Docs</title></svelte:head>

<h1 class="page-title">OpenDeploy documentation</h1>
<p class="lead">Guides to get things done, the design behind each subsystem, and a reference for every type.</p>
{#each groups as group}
  <h2>{group.title}</h2>
  <p>{blurbs[group.title]}</p>
  <div class="cards">
    {#each group.documents as document}
      <a class="card" href={`${base}/${document.slug}/`}>
        <h4>{document.title}</h4>
      </a>
    {/each}
  </div>
{/each}
