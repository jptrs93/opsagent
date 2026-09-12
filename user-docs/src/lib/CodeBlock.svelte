<script lang="ts">
  import { onMount } from 'svelte';
  import type { CodeLanguage, createCodeEditor } from './languages';

  let { code, language, title }: { code: string; language: CodeLanguage; title?: string } = $props();
  const id = $props.id();
  const labels: Record<CodeLanguage, string> = {
    text: 'Text', json: 'JSON', hcl: 'HCL', protobuf: 'Protobuf', typescript: 'TypeScript', smithy: 'Smithy',
  };
  let host: HTMLDivElement;
  let editor: ReturnType<typeof createCodeEditor> | undefined;
  let mounted = $state(false);
  let copyMessage = $state('');
  let alive = false;
  let copyAttempt = 0;
  const label = $derived(`${title ? `${title} - ` : ''}${labels[language]} source`);

  onMount(() => {
    alive = true;
    void import('./languages').then(({ createCodeEditor }) => {
      if (!alive) return;
      editor = createCodeEditor(host, code, language, label);
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
    if (mounted) editor?.update(code, language, label);
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

<div class="code-block">
  <div class="code-header">
    <span class="code-heading" id={`${id}-heading`}>
      {#if title}<span class="code-title">{title}</span>{/if}
      <span class="code-language">{labels[language]}</span>
    </span>
    <span class="copy-status" id={`${id}-status`} role="status">{copyMessage}</span>
    <button type="button" onclick={copy} aria-label={`Copy ${label}`} aria-describedby={`${id}-status`}>Copy</button>
  </div>
  <div class="code-body">
    {#if !mounted}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex (Scrollable source must be keyboard accessible before hydration.) -->
      <pre role="region" tabindex="0" aria-labelledby={`${id}-heading`}><code class={`language-${language}`}>{code}</code></pre>
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
    border: 1px solid #263449;
    border-radius: 10px;
    background: var(--code-bg, #0f172a);
    color: var(--code-ink, #e2e8f0);
  }
  .code-header {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px 12px;
    padding: 8px 12px 8px 17px;
    border-bottom: 1px solid #263449;
    font: 12px/1.4 -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
  }
  .code-heading { display: flex; flex-wrap: wrap; align-items: baseline; gap: 10px; margin-right: auto; min-width: 0; }
  .code-title { font-weight: 600; overflow-wrap: anywhere; }
  .code-language { color: #94a3b8; }
  .copy-status { color: #7dd3fc; }
  button {
    flex-shrink: 0;
    border: 1px solid #334155;
    border-radius: 5px;
    padding: 4px 9px;
    background: transparent;
    color: #bae6fd;
    font: inherit;
    cursor: pointer;
  }
  button:hover { background: #1e293b; border-color: #60a5fa; }
  button:focus-visible { outline: 2px solid #7dd3fc; outline-offset: -2px; }
  pre:focus { outline: none; }
  .code-body, .editor-host { min-width: 0; max-width: 100%; }
  .editor-host, pre, code {
    font-family: 'SF Mono', SFMono-Regular, ui-monospace, 'Cascadia Code', Menlo, Consolas, monospace;
  }
  pre {
    margin: 0;
    padding: 15px 17px;
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
