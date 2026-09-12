<script lang="ts">
  import '../app.css';
  import { page } from '$app/state';
  import { afterNavigate } from '$app/navigation';
  import { base } from '$app/paths';
  import { onMount } from 'svelte';

  let { children } = $props();
  let open = $state(false);
  let current = $state('goals');
  let menu: HTMLButtonElement;
  let article: HTMLElement;
  let sections: HTMLElement[] = [];

  const documents = [
    { slug: 'networking', title: 'Networking', sections: [
      ['goals', 'Design overview'], ['addressing', 'Addressing'], ['dns', 'DNS'],
      ['balancing', 'Load balancing'], ['routing', 'Routing and transport'],
      ['rollover', 'Rollover'], ['policy', 'Network policy'], ['egress', 'Egress'], ['ingress', 'Ingress'],
    ] },
    { slug: 'logging', title: 'Logging', sections: [
      ['goals', 'Design overview'], ['structure', 'Parsed payload'], ['storage', 'Write-ahead log'],
      ['collector', 'Log collector'], ['search', 'Query engine'], ['system', 'System logs'], ['capture', 'Log consumer'],
    ] },
  ];

  function updateCurrent() {
    if (!sections.length) return;
    const probe = Math.min(window.innerHeight * 0.3, 200);
    let id = sections[0].id;
    for (const section of sections) {
      if (section.getBoundingClientRect().top <= probe) id = section.id;
    }
    if (window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 2) {
      id = sections[sections.length - 1].id;
    }
    current = id;
  }

  afterNavigate(() => {
    open = false;
    sections = Array.from(article.querySelectorAll('section.page, h3[id]'));
    updateCurrent();
  });

  onMount(() => {
    const observer = new ResizeObserver(updateCurrent);
    observer.observe(article);
    return () => observer.disconnect();
  });

  function onKeydown(event: KeyboardEvent) {
    if (event.key === 'Escape' && open) {
      open = false;
      menu.focus();
    }
  }
</script>

<svelte:window onscroll={updateCurrent} onresize={updateCurrent} onkeydown={onKeydown} />
<a class="skip-link" href="#main">Skip to content</a>
<button class="menu-btn" type="button" bind:this={menu} aria-controls="sidebar" aria-expanded={open} onclick={() => open = !open}>Menu</button>
<aside class="sidebar" class:open id="sidebar">
  <a class="brand" href={`${base}/`}>
    <span class="name">Open<span>Deploy</span></span>
    <span class="kind">Documentation</span>
  </a>
  <nav aria-label="Documentation" class="nav-group">
    <h5>System Design</h5>
    {#each documents as document}
      {@const active = page.route.id === `/${document.slug}`}
      <a class="nav-item" class:active href={`${base}/${document.slug}/`} aria-current={active ? 'page' : undefined} onclick={() => open = false}>{document.title}</a>
      {#if active}
        <div class="subnav" id="page-nav">
          {#each document.sections as [id, title]}
            <a href={`#${id}`} class:current={current === id} aria-current={current === id ? 'location' : undefined} onclick={() => open = false}>{title}</a>
          {/each}
        </div>
      {/if}
    {/each}
  </nav>
  <nav aria-label="Reference" class="nav-group">
    <h5>Reference</h5>
    <a class="nav-item" href={`${base}/data-model/index.html`} data-sveltekit-reload>Data model</a>
  </nav>
</aside>

<main class="content" id="main" tabindex="-1">
  <div class="wrap" id="top" bind:this={article}>
    {@render children()}
  </div>
</main>
