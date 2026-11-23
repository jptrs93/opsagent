<script>
  import { onMount } from 'svelte';
  import NetworkDiagram from './NetworkDiagram.svelte';
  import { layers, nodes } from './model.js';

  let nodeCount = $state(3);
  let view = $state('isometric');
  let active = $state('all');
  let hovered = $state(null);
  let playing = $state(true);
  let diagram;
  let exportMessage = $state('');
  let inspected = $derived(hovered ?? active);
  let node = $derived(nodes.find(n => n.id === inspected));
  let detail = $derived(layers[inspected] ?? layers.all);
  let step = $state(-1);
  const steps = [
    { title: 'Arrive from the public world', text: 'A public HTTPS connection reaches the ingress proxy on node 01.', layer: 'ingress' },
    { title: 'Travel inside the underlay', text: 'The proxy’s private IPv6 connection to node 02 is carried inside an encrypted WireGuard link.', layer: 'underlay' },
    { title: 'Reach the remote workload', text: 'Node 02 decrypts the packet and routes it to the API workload on the same private IPv6 network.', layer: 'ipv6' }
  ];
  onMount(() => {
    const media = window.matchMedia('(prefers-reduced-motion: reduce)');
    playing = !media.matches;
    const update = () => { playing = !media.matches; };
    media.addEventListener('change', update);
    return () => media.removeEventListener('change', update);
  });
  function selectLayer(key) { active = key; hovered = null; step = -1; }
  function advance() { step = (step + 1) % steps.length; active = steps[step].layer; hovered = null; }
  function reset() { selectLayer('all'); nodeCount = 3; view = 'isometric'; }
  function exportDiagram() { diagram.exportSvg(); exportMessage = 'SVG downloaded'; setTimeout(() => exportMessage = '', 2500); }
</script>

<svelte:window onkeydown={event => { if (event.key === 'Escape') selectLayer('all'); }} />

<main class="app-shell">
  <header class="masthead">
    <a class="brand" href="./" aria-label="OpenDeploy networking model"><span class="brand-mark" aria-hidden="true"><i></i><i></i><i></i></span>OpenDeploy<span class="brand-divider"></span><span class="brand-section">Architecture lab</span></a>
    <span class="edition"><span></span> INTERACTIVE FIELD NOTES <b> / 001</b></span>
  </header>

  <section class="intro">
    <div><p class="eyebrow">NETWORKING, MADE VISIBLE</p><h1>One cluster.<br class="mobile-break" /> Three perspectives.</h1><p class="intro-copy">Follow a packet from the public world to a private workload.<br /> Explore the layers that make every node feel like one network.</p></div>
    <button class="export-button" onclick={exportDiagram}><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><path d="M12 3v12m-4-4 4 4 4-4M5 16v5h14v-5" /></svg>Export diagram<span class="export-type">SVG</span></button>
    <span class="sr-only" role="status">{exportMessage}</span>
  </section>

  <section class="model-panel" aria-label="Network architecture explorer">
    <div class="model-toolbar">
      <div class="layer-tabs" aria-label="Focus a network layer">
        {#each Object.entries(layers) as [key, layer]}
          <button class:active={active === key} aria-pressed={active === key} onclick={() => selectLayer(key)} style={`--layer-color:${layer.color}`}>
            {#if key === 'all'}<span class="all-icon" aria-hidden="true">▧</span>{:else}<span class="color-dot" aria-hidden="true"></span>{/if}{layer.label}
          </button>
        {/each}
      </div>
      <div class="node-control"><span>Nodes</span><div class="segmented">{#each [2, 3] as count}<button class:chosen={nodeCount === count} aria-pressed={nodeCount === count} aria-label={`Show ${count} nodes`} onclick={() => { nodeCount = count; hovered = null; }}>{count}</button>{/each}</div></div>
    </div>

    <div class="model-body">
      <div class="stage">
        <div class="stage-heading"><span class="tiny-label">CLUSTER TOPOLOGY</span><span class="schematic-tag">Illustrative model</span></div>
        <NetworkDiagram bind:this={diagram} {nodeCount} {view} {active} {playing} oninspect={value => hovered = value} />
        <div class="stage-bottom"><span class="hover-hint"><svg width="13" height="16" viewBox="0 0 13 16" fill="none" stroke="currentColor" aria-hidden="true"><path d="m2 1 9 8-5 1-2 4z" /></svg> Hover to explore · select a layer to focus</span><div class="view-control" aria-label="Diagram projection"><button class:chosen={view === 'isometric'} aria-pressed={view === 'isometric'} onclick={() => view = 'isometric'}>Isometric</button><button class:chosen={view === 'flat'} aria-pressed={view === 'flat'} onclick={() => view = 'flat'}>Plan view</button></div></div>
      </div>

      <aside class="inspector" aria-label="Layer details">
        <div class="inspector-top"><span class="tiny-label">{node ? 'NODE INSPECTOR' : 'LAYER INSPECTOR'}</span><span class="inspector-symbol" aria-hidden="true">⌘</span></div>
        <div class="detail" style={`--detail-color:${node ? '#a798f5' : detail.color}`}>
          <p class="detail-eyebrow">{node ? 'COMPUTE / PRIVATE NETWORK' : detail.eyebrow}</p>
          <h2>{node ? node.name : detail.title}</h2>
          <p class="detail-copy">{node ? node.detail : detail.description}</p>
        </div>

        <div class="cross-section">
          <span class="tiny-label">{node ? node.service.toUpperCase() : 'INSIDE A CROSS-NODE LINK'}</span>
          <div class="tunnel-shell"><div class="tunnel-core"><span></span>IPv6 packet<span></span></div></div>
          <div class="cross-caption"><span class="mini-line purple"></span>WireGuard transport<span class="lock-icon" aria-hidden="true">↔</span></div>
        </div>

        {#if detail.facts && !node}
          <ul class="facts">{#each detail.facts as fact}<li><span style={`color:${detail.color}`}>↳</span>{fact}</li>{/each}</ul>
        {:else}
          <div class="layer-key">
            {#each ['underlay', 'ipv6', 'ingress'] as key, i}
              <button onclick={() => selectLayer(key)}><span class="key-number">0{i+1}</span><span class="mini-line" style={`background:${layers[key].color}`}></span><span>{layers[key].label}</span><span class="key-arrow">↗</span></button>
            {/each}
          </div>
        {/if}
        <p class="inspector-note">{node ? 'Workload roles are illustrative. Any ingress node can reach a backend on another node.' : 'The mint path sits inside the violet transport. These are two views of the same cross-node traffic.'}</p>
      </aside>
    </div>

    <div class="playback-bar">
      <div class="playback-left"><button class="play-button" onclick={() => playing = !playing} aria-label={playing ? 'Pause traffic animation' : 'Play traffic animation'}>{#if playing}<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor" aria-hidden="true"><path d="M2 1h3v10H2zm5 0h3v10H7z" /></svg>{:else}<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor" aria-hidden="true"><path d="m3 1 8 5-8 5z" /></svg>{/if}</button><span>Traffic flow <b>{playing ? 'Running' : 'Paused'}</b></span><span class="playback-separator"></span><span class="simulation-note">Illustrative traffic · not live telemetry</span></div>
      <button class="reset-button" onclick={reset}><span aria-hidden="true">↺</span> Reset view</button>
    </div>
  </section>

  <section class="journey" aria-label="Follow a request">
    <div class="journey-intro"><p class="eyebrow">FOLLOW A REQUEST</p><h2>A small journey.<br /> Across the whole stack.</h2><button class="trace-button" onclick={advance}>{step === -1 ? 'Walk through the flow' : step === 2 ? 'Start again' : 'Next step'}<span aria-hidden="true">→</span></button></div>
    <div class="journey-steps">
      {#each steps as item, i}
        <button class="journey-step" class:current={step === i} style={`--step-color:${layers[item.layer].color}`} onclick={() => { step = i; active = item.layer; hovered = null; }} aria-pressed={step === i}>
          <div class="step-top"><span class="step-number">0{i+1}</span><span class="step-rule"></span><span class="step-arrow" aria-hidden="true">↗</span></div>
          <h3>{item.title}</h3><p>{item.text}</p>
        </button>
      {/each}
    </div>
  </section>

  <footer><span>OpenDeploy <span class="footer-dot">/</span> Network architecture</span><span>One private network. Encrypted between nodes.</span></footer>
</main>
