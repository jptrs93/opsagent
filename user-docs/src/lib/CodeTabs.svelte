<script lang="ts">
  import CodeBlock from './CodeBlock.svelte';
  import type { CodeVariant } from './languages';

  let { variants, title = 'LogEvent' }: { variants: readonly CodeVariant[]; title?: string } = $props();
  const id = $props.id();
  let selected = $state<CodeVariant['language']>('protobuf');
  const activeIndex = $derived(Math.max(0, variants.findIndex((variant) => variant.language === selected)));
  const active = $derived(variants[activeIndex]);

  function navigate(event: KeyboardEvent, index: number) {
    let next: number;
    switch (event.key) {
      case 'ArrowRight': next = (index + 1) % variants.length; break;
      case 'ArrowLeft': next = (index - 1 + variants.length) % variants.length; break;
      case 'Home': next = 0; break;
      case 'End': next = variants.length - 1; break;
      default: return;
    }
    event.preventDefault();
    selected = variants[next].language;
    const button = event.currentTarget as HTMLButtonElement;
    button.parentElement?.querySelectorAll<HTMLButtonElement>('[role="tab"]')[next]?.focus();
  }
</script>

{#if active}
  <div class="code-tabs">
    <div class="tab-list" role="tablist" aria-label={`${title} representation`} aria-orientation="horizontal">
      {#each variants as variant, index}
        <button
          type="button"
          role="tab"
          id={`${id}-tab-${index}`}
          aria-selected={index === activeIndex}
          aria-controls={`${id}-panel`}
          tabindex={index === activeIndex ? 0 : -1}
          onclick={() => selected = variant.language}
          onkeydown={(event) => navigate(event, index)}
        >{variant.label}</button>
      {/each}
    </div>
    <div id={`${id}-panel`} role="tabpanel" aria-labelledby={`${id}-tab-${activeIndex}`} tabindex="0">
      <CodeBlock code={active.code} language={active.language} {title} />
    </div>
  </div>
{/if}

<style>
  .code-tabs { min-width: 0; max-width: 100%; margin: 16px 0; }
  .tab-list { display: flex; gap: 4px; overflow-x: auto; padding: 3px; border-bottom: 1px solid var(--line, #e2e8f0); }
  button {
    flex-shrink: 0;
    padding: 8px 12px;
    border: 0;
    border-bottom: 2px solid transparent;
    border-radius: 5px 5px 0 0;
    background: transparent;
    color: var(--sub, #475569);
    font: 600 13px/1.4 -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
    cursor: pointer;
  }
  button:hover { color: var(--accent, #2563eb); background: var(--accent-soft, #eff6ff); }
  button[aria-selected='true'] { color: var(--accent, #2563eb); border-bottom-color: var(--accent, #2563eb); }
  button:focus-visible { outline: 2px solid var(--accent, #2563eb); outline-offset: -2px; }
  [role='tabpanel']:focus { outline: none; }
  [role='tabpanel'] { min-width: 0; border-radius: 10px; }
</style>
