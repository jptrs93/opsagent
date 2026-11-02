<script lang="ts">
  import '../app.css';
  import { page } from '$app/state';
  import { afterNavigate } from '$app/navigation';
  import { base } from '$app/paths';
  import { onMount } from 'svelte';
  import { groups } from '$lib/nav';

  let { children } = $props();
  let open = $state(false);
  let current = $state('goals');
  let menu: HTMLButtonElement;
  let article: HTMLElement;
  let sections: HTMLElement[] = [];
  let collapsed = $state<Record<string, boolean>>({});

  function toggleGroup(title: string) {
    collapsed[title] = !collapsed[title];
    try { localStorage.setItem('nav-collapsed', JSON.stringify(collapsed)); } catch {}
  }

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

  const anchorIcon = '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 17H7A5 5 0 0 1 7 7h2"/><path d="M15 7h2a5 5 0 1 1 0 10h-2"/><line x1="8" x2="16" y1="12" y2="12"/></svg>';

  function addAnchors() {
    for (const heading of article.querySelectorAll<HTMLElement>('section.page > h2, h3[id]')) {
      if (heading.querySelector('.anchor')) continue;
      const id = heading.id || heading.closest('section.page')?.id;
      if (!id) continue;
      const link = document.createElement('a');
      link.className = 'anchor';
      link.href = `#${id}`;
      link.setAttribute('aria-label', 'Link to this section');
      link.innerHTML = anchorIcon;
      heading.append(link);
    }
  }

  afterNavigate(() => {
    open = false;
    sections = Array.from(article.querySelectorAll('section.page, h3[id]'));
    addAnchors();
    updateCurrent();
  });

  onMount(() => {
    try { collapsed = JSON.parse(localStorage.getItem('nav-collapsed') ?? '{}'); } catch {}
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
  <nav aria-label="Documentation" class="tree">
    {#each groups as group}
      {@const expanded = !collapsed[group.title]}
      <div class="tree-root">
        <button type="button" class="tree-head" aria-expanded={expanded} onclick={() => toggleGroup(group.title)}>
          <span class="chev" class:open={expanded} aria-hidden="true"></span>{group.title}
        </button>
        {#if expanded}
          <div class="tree-items">
            {#each group.documents as document}
              {@const active = page.route.id === `/${document.slug}`}
              <a class="nav-item" class:active href={`${base}/${document.slug}/`} aria-current={active ? 'page' : undefined} onclick={() => open = false}>{document.title}</a>
              {#if active && document.sections.length}
                <div class="subnav" id="page-nav">
                  {#each document.sections as [id, title]}
                    <a href={`#${id}`} class:current={current === id} aria-current={current === id ? 'location' : undefined} onclick={() => open = false}>{title}</a>
                  {/each}
                </div>
              {/if}
            {/each}
          </div>
        {/if}
      </div>
    {/each}
  </nav>
</aside>

<main class="content" id="main" tabindex="-1">
  <div class="wrap" id="top" bind:this={article}>
    {@render children()}
  </div>
</main>
