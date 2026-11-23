<script>
  import { onMount } from 'svelte';
  import { nodes } from './model.js';

  let { nodeCount = 3, view = 'isometric', active = 'all', playing = true, oninspect = () => {} } = $props();
  let svg;
  let hovered = $state(null);
  const positions = [[300, 350], [595, 210], [800, 425]];
  const flatPositions = [[290, 350], [620, 210], [805, 425]];
  let visibleNodes = $derived(nodes.slice(0, nodeCount));
  let points = $derived(view === 'isometric' ? positions : flatPositions);
  let links = $derived(nodeCount === 3 ? [[0, 1], [1, 2], [0, 2]] : [[0, 1]]);
  let focus = $derived(hovered ?? active);
  const tone = { underlay: '#a798f5', ipv6: '#70dfbd', ingress: '#f4b77c' };

  function opacity(layer, indices = []) {
    if (focus === 'all') return 1;
    if (focus.startsWith('node-')) return indices.some(i => nodes[i].id === focus) ? 1 : 0.2;
    if (focus === 'ingress' && layer === 'ipv6' && indices.includes(0) && indices.includes(1)) return 1;
    return focus === layer ? 1 : layer === 'underlay' && focus === 'ipv6' ? 0.55 : 0.16;
  }
  function inspect(value) { hovered = value; oninspect(value); }
  function clear() { hovered = null; oninspect(null); }
  function line(a, b) { return `M ${points[a][0]} ${points[a][1] + 42} L ${points[b][0]} ${points[b][1] + 42}`; }
  function diamond(x, y, w, h) { return `${x},${y - h} ${x + w},${y} ${x},${y + h} ${x - w},${y}`; }

  $effect(() => {
    if (!svg) return;
    if (playing) svg.unpauseAnimations(); else svg.pauseAnimations();
  });

  onMount(() => { if (!playing) svg.pauseAnimations(); });

  export function exportSvg() {
    const copy = svg.cloneNode(true);
    const originals = [svg, ...svg.querySelectorAll('*')];
    const clones = [copy, ...copy.querySelectorAll('*')];
    const properties = ['fill', 'stroke', 'stroke-width', 'stroke-dasharray', 'stroke-linecap', 'opacity', 'font-family', 'font-size', 'font-weight', 'letter-spacing', 'text-anchor', 'paint-order'];
    originals.forEach((element, i) => {
      const style = getComputedStyle(element);
      for (const property of properties) clones[i].style.setProperty(property, style.getPropertyValue(property));
    });
    copy.querySelectorAll('animateMotion, .packet').forEach(element => element.remove());
    copy.setAttribute('xmlns', 'http://www.w3.org/2000/svg');
    copy.setAttribute('width', '1100'); copy.setAttribute('height', '640');
    copy.style.background = '#14181f';
    const url = URL.createObjectURL(new Blob([new XMLSerializer().serializeToString(copy)], { type: 'image/svg+xml' }));
    const a = document.createElement('a'); a.href = url; a.download = 'opendeploy-network.svg'; a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
</script>

<svelte:window onkeydown={event => { if (event.key === 'Escape') clear(); }} />

<svg bind:this={svg} class="network-diagram" viewBox="0 0 1100 640" role="group" aria-label="Interactive networking architecture: public ingress enters node 01; private IPv6 connects workloads inside the WireGuard underlay.">
  <title>OpenDeploy network architecture</title>
  <desc>Violet links show the WireGuard underlay between {nodeCount} nodes. Mint lines inside those links show virtual IPv6 traffic. An amber path brings public HTTPS traffic to the ingress proxy on node 01, which can connect to the API workload on node 02.</desc>
  <defs>
    <pattern id="network-dots" width="22" height="22" patternUnits="userSpaceOnUse"><circle cx="1" cy="1" r="0.8" fill="#303640" /></pattern>
    <linearGradient id="network-plane" x1="0" y1="0" x2="1" y2="1"><stop stop-color="#a798f5" stop-opacity="0.045" /><stop offset="1" stop-color="#a798f5" stop-opacity="0.01" /></linearGradient>
    <linearGradient id="network-top"><stop stop-color="#323a49" /><stop offset="1" stop-color="#242b36" /></linearGradient>
    <filter id="network-glow" x="-100%" y="-100%" width="300%" height="300%"><feGaussianBlur stdDeviation="3" /></filter>
  </defs>
  <rect width="1100" height="640" fill="url(#network-dots)" opacity="0.48" />
  <path d="M 172 350 L 592 125 Q 608 117 625 128 L 980 413 Q 995 425 976 437 L 593 603 Q 580 609 565 601 L 173 374 Q 154 363 172 350 Z" fill="url(#network-plane)" stroke="#353444" stroke-dasharray="5 7" />
  <text x="817" y="214" fill="#77798c" font-size="9" letter-spacing="1.5">PRIVATE CLUSTER</text>
  <text x="817" y="229" fill="#77798c" font-size="9" letter-spacing="1.5">BOUNDARY</text>
  <path d="M 870 241 V 295 L 906 350" fill="none" stroke="#484454" stroke-dasharray="3 5" />

  {#each links as [a, b]}
    <g opacity={opacity('underlay', [a, b])} class="fade">
      <path d={line(a,b)} fill="none" stroke="#0c0f15" stroke-width="37" stroke-linecap="round" transform="translate(0 8)" />
      <path d={line(a,b)} fill="none" stroke="#7765ad" stroke-width="32" stroke-linecap="round" />
      <path d={line(a,b)} fill="none" stroke="#2f2a48" stroke-width="29" stroke-linecap="round" />
      <path d={line(a,b)} fill="none" stroke="#a798f5" stroke-opacity="0.2" stroke-width="19" stroke-linecap="round" />
    </g>
    <g opacity={opacity('ipv6', [a, b])} class="fade">
      <path d={line(a,b)} fill="none" stroke="#70dfbd" stroke-width="4" stroke-linecap="round" />
      {#each [0, 1, 2] as packet}
        <circle class="packet" r="4" fill="#d8fff1" stroke="#70dfbd" stroke-width="2">
          <animateMotion dur="4.8s" begin={`${-packet * 1.6}s`} repeatCount="indefinite" path={line(a,b)} />
        </circle>
      {/each}
    </g>
    <path d={line(a,b)} fill="none" stroke="transparent" stroke-width="35" role="button" tabindex="0" aria-label={`Inspect WireGuard connection between ${nodes[a].name} and ${nodes[b].name}`} onmouseenter={() => inspect('underlay')} onmouseleave={clear} onfocus={() => inspect('underlay')} onblur={clear} onclick={() => inspect('underlay')} onkeydown={event => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); inspect('underlay'); } }} class="hit-target" />
  {/each}

  <g opacity={opacity('ingress', [0])} class="fade">
    <path d="M 127 206 L 127 262 Q 127 283 149 295 L 263 359" fill="none" stroke="#f4b77c" stroke-width="15" stroke-opacity="0.06" />
    <path d="M 127 206 L 127 262 Q 127 283 149 295 L 263 359" fill="none" stroke="#f4b77c" stroke-width="2.5" />
    <circle class="packet" r="4" fill="#ffdbb6"><animateMotion dur="3s" repeatCount="indefinite" path="M 127 206 L 127 262 Q 127 283 149 295 L 263 359" /></circle>
    <text x="148" y="253" fill="#f4b77c" font-size="10" letter-spacing="1.3">HTTPS :443</text>
    <g role="button" tabindex="0" aria-label="Inspect public ingress" onmouseenter={() => inspect('ingress')} onmouseleave={clear} onfocus={() => inspect('ingress')} onblur={clear} onclick={() => inspect('ingress')} onkeydown={event => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); inspect('ingress'); } }} class="hit-target">
      <rect x="63" y="106" width="128" height="102" rx="16" fill="#211e1c" stroke="#5b4534" />
      <circle cx="127" cy="145" r="19" stroke="#f4b77c" fill="none" stroke-width="1.4" />
      <ellipse cx="127" cy="145" rx="8" ry="19" stroke="#f4b77c" fill="none" stroke-width="1.2" />
      <path d="M108 145 H146 M111 136 H143 M111 154 H143" stroke="#f4b77c" fill="none" stroke-width="1" />
      <text x="127" y="188" text-anchor="middle" fill="#f3ceab" font-size="12">Public internet</text>
    </g>
  </g>

  {#each visibleNodes as node, i}
    {@const x = points[i][0]}
    {@const y = points[i][1]}
    {@const iso = view === 'isometric'}
    {@const selected = focus === node.id}
    <g class="node hit-target" class:selected role="button" tabindex="0" aria-label={`Inspect ${node.name}: ${node.service}`} onmouseenter={() => inspect(node.id)} onmouseleave={clear} onfocus={() => inspect(node.id)} onblur={clear} onclick={() => inspect(node.id)} onkeydown={event => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); inspect(node.id); } }}>
      <rect x={x-102} y={y-103} width="204" height="225" fill="transparent" />
      <ellipse cx={x} cy={y+62} rx="100" ry="37" fill="#080b10" opacity="0.4" />
      {#if iso}
        <path d={`M${x-90} ${y+35} L${x} ${y+79} L${x+90} ${y+35} V${y+49} L${x} ${y+93} L${x-90} ${y+49} Z`} fill="#1a1a28" stroke="#4b4267" />
        <polygon points={diamond(x,y+35,90,44)} fill="#272336" stroke={selected ? '#d6c9ff' : '#786898'} stroke-width="1.2" />
        <polygon points={diamond(x,y+34,77,36)} fill="none" stroke="#a798f5" stroke-opacity="0.23" />
        <path d={`M${x-64} ${y-22} L${x} ${y+11} L${x+64} ${y-22} V${y+21} L${x} ${y+54} L${x-64} ${y+21} Z`} fill="#1c2530" stroke="#465266" />
        <path d={`M${x} ${y+11} V${y+54} L${x+64} ${y+21} V${y-22} Z`} fill="#171f28" stroke="#465266" />
        <polygon points={diamond(x,y-22,64,33)} fill="url(#network-top)" stroke="#566277" />
        <polygon points={diamond(x,y-25,42,21)} fill="#1d423e" stroke="#70dfbd" stroke-width="1.25" />
        <polygon points={diamond(x,y-25,29,14)} fill="none" stroke="#70dfbd" stroke-opacity="0.4" />
        <path d={`M${x-7} ${y-29} l7 -4 7 4 -7 4 Z M${x-7} ${y-24} l7 4 7 -4`} fill="none" stroke="#a3f3d9" stroke-width="1.4" />
        <path d={`M${x-48} ${y+4} l23 12 m-23 -6 l23 12`} stroke="#65738a" stroke-width="2" />
        <circle cx={x-14} cy={y+30} r="2" fill="#70dfbd" />
        <path d={`M${x+28} ${y+38} V${y+51} L${x} ${y+65}`} fill="none" stroke="#70dfbd" stroke-width="3" />
      {:else}
        <rect x={x-84} y={y-55} width="168" height="120" rx="22" fill="#272336" stroke={selected ? '#d6c9ff' : '#786898'} />
        <rect x={x-67} y={y-41} width="134" height="91" rx="13" fill="#222d37" stroke="#566277" />
        <rect x={x-45} y={y-24} width="90" height="49" rx="7" fill="#1d423e" stroke="#70dfbd" />
        <text x={x} y={y+4} text-anchor="middle" font-size="11" fill="#a3f3d9">Workload</text>
        <path d={`M${x} ${y+25} V${y+65}`} stroke="#70dfbd" stroke-width="3" />
      {/if}
      {#if i === 0}
        <rect x={x-98} y={y+1} width="73" height="23" rx="5" fill="#30261f" stroke="#b78657" />
        <circle cx={x-87} cy={y+12.5} r="2.5" fill="#f4b77c" />
        <text x={x-58} y={y+16} text-anchor="middle" fill="#f4c69b" font-size="9">INGRESS</text>
      {/if}
      <text x={x} y={y-88} text-anchor="middle" fill={selected ? '#ffffff' : '#e0e5ed'} font-size="17" font-weight="600">{node.name}</text>
      <text x={x} y={y-68} text-anchor="middle" fill="#8995a5" font-size="11">{node.service}</text>
      <text x={i === 1 ? x+107 : x} y={i === 1 ? y+18 : y+116} text-anchor={i === 1 ? 'start' : 'middle'} fill="#8c85a5" font-size="9" letter-spacing="1.2">WIREGUARD PEER / 0{i+1}</text>
    </g>
  {/each}

  <g opacity={focus === 'all' || focus === 'underlay' || focus === 'ipv6' ? 1 : 0.3} class="fade">
    <path d="M 478 322 L 500 355 H 584" stroke="#a798f5" fill="none" stroke-width="1" />
    <circle cx="478" cy="322" r="3" fill="#a798f5" />
    <text x="506" y="373" font-size="11" fill="#b6a8e5">WireGuard underlay</text>
    <path d="M 450 320 L 425 271 H 366" stroke="#70dfbd" fill="none" stroke-width="1" />
    <circle cx="450" cy="320" r="3" fill="#70dfbd" />
    <text x="342" y="255" font-size="11" fill="#70dfbd">Virtual IPv6</text>
  </g>
  <g transform="translate(69 545)" opacity="0.5">
    <path d="M0 28 V0 M0 28 L25 42 M0 28 L25 14" fill="none" stroke="#8c99aa" stroke-width="1.2" />
    <text x="-3" y="-9" font-size="9" fill="#8c99aa">Z</text><text x="31" y="17" font-size="9" fill="#8c99aa">Y</text><text x="31" y="46" font-size="9" fill="#8c99aa">X</text>
  </g>
</svg>

<style>
  .network-diagram { display: block; width: 100%; height: auto; font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; }
  .fade { transition: opacity 220ms ease; }
  .hit-target { cursor: pointer; outline: none; }
  .hit-target:focus-visible { outline: 2px solid #e9ddff; outline-offset: 6px; }
  .node { transition: filter 180ms ease; }
  .node.selected { filter: drop-shadow(0 0 10px #a798f527); }
  @media (prefers-reduced-motion: reduce) { .fade, .node { transition: none; } }
</style>
