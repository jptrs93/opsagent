<script lang="ts">
  import { onMount } from 'svelte';
  import type { CodeLanguage, createCodeEditor } from './languages';

  let { code, language, title, copy: withCopy = false }: {
    code: string; language: CodeLanguage; title?: string; copy?: boolean;
  } = $props();
  const id = $props.id();
  const labels: Record<CodeLanguage, string> = {
    text: 'Text', json: 'JSON', hcl: 'HCL', protobuf: 'Protobuf', typescript: 'TypeScript', smithy: 'Smithy', typedef: 'Type definition',
  };
  let host: HTMLDivElement;
  let editor: ReturnType<typeof createCodeEditor> | undefined;
  let mounted = $state(false);
  let copyMessage = $state('');
  let alive = false;
  let copyAttempt = 0;
  const label = $derived(`${title ? `${title} - ` : ''}${labels[language]} source`);
  const shown = $derived(code.replace(/\n+$/, ''));
  const header = $derived(Boolean(title) || withCopy);

  onMount(() => {
    alive = true;
    void import('./languages').then(({ createCodeEditor }) => {
      if (!alive) return;
      editor = createCodeEditor(host, shown, language, label);
      mounted = true;
    }).catch(() => {
      // A failed chunk or editor initialization must leave readable source.
      if (alive) {
        editor?.destroy();
        editor = undefined;
        host.replaceChildren();
      }
    });
    return () => {
      alive = false;
      editor?.destroy();
    };
  });

  $effect(() => {
    if (mounted) editor?.update(shown, language, label);
  });

  $effect(() => {
    code;
    language;
    copyAttempt += 1;
    copyMessage = '';
  });

  async function copy() {
    const attempt = ++copyAttempt;
    copyMessage = '';
    try {
      await navigator.clipboard.writeText(code);
      if (alive && attempt === copyAttempt) copyMessage = 'Copied';
    } catch {
      if (alive && attempt === copyAttempt) copyMessage = 'Copy failed. Select and copy the code.';
    }
  }
</script>

<div class="code-block" class:plain={!header}>
  {#if header}
    <div class="code-header">
      <span class="code-title" id={`${id}-heading`}>{title ?? labels[language]}</span>
      {#if withCopy}
        <span class="copy-status" id={`${id}-status`} role="status">{copyMessage}</span>
        <button type="button" onclick={copy} aria-label={`Copy ${label}`} aria-describedby={`${id}-status`}>Copy</button>
      {/if}
    </div>
  {/if}
  <div class="code-body">
    {#if !mounted}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex (Scrollable source must be keyboard accessible before hydration.) -->
      <pre role="region" tabindex="0" aria-label={header ? undefined : label} aria-labelledby={header ? `${id}-heading` : undefined}><code class={`language-${language}`}>{shown}</code></pre>
    {/if}
    <div class="editor-host" bind:this={host}></div>
  </div>
</div>

<style>
  .code-block {
    min-width: 0;
    max-width: 100%;
    margin: 12px 0;
    overflow: hidden;
    border: 1px solid var(--code-line);
    border-radius: 6px;
    background: var(--code-bg);
    color: var(--code-ink);
  }
  .code-header {
    display: flex;
    align-items: center;
    gap: 8px 12px;
    min-height: 34px;
    padding: 4px 6px 4px 16px;
    border-bottom: 1px solid var(--code-line);
    font: 12px/1.4 -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
    color: var(--sub);
  }
  .code-title { margin-right: auto; min-width: 0; font-weight: 600; overflow-wrap: anywhere; }
  .copy-status { color: var(--accent); }
  button {
    flex-shrink: 0;
    border: 1px solid transparent;
    border-radius: 5px;
    padding: 4px 9px;
    background: transparent;
    color: var(--sub);
    font: inherit;
    cursor: pointer;
  }
  button:hover { color: var(--accent); background: var(--accent-soft); }
  button:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
  pre:focus { outline: none; }
  .code-body, .editor-host { min-width: 0; max-width: 100%; }
  .editor-host, pre, code {
    font-family: 'SF Mono', SFMono-Regular, ui-monospace, 'Cascadia Code', Menlo, Consolas, monospace;
  }
  pre {
    margin: 0;
    padding: 14px 16px;
    overflow-x: auto;
    border: 0;
    border-radius: 0;
    background: transparent;
    color: inherit;
    font-size: 13px;
    line-height: 1.55;
    white-space: pre;
    tab-size: 4;
  }
  code { padding: 0; border: 0; background: transparent; color: inherit; font-size: inherit; }
</style>
