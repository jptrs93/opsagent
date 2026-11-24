<script lang="ts">
  import { legend, buildDetails } from './network-diagram-text';
  import './network-diagram.css';

  type Kind = 'ingress' | 'virtual' | 'egress';
  type Workload = { id: string; name: string; addr: string; slot: 0 | 1; tags: string[] };
  type Node = { id: string; name: string; x: number; host: string; tags: string[]; workloads: Workload[] };

  const COL_W = 190;
  const COL_TOP = 40;
  const COL_BOTTOM = 374;
  const V_BAND = { x: 120, y: 62, w: 770, h: 160 };
  const U_BAND = { x: 120, y: 246, w: 770, h: 142 };
  const CUBE_W = 66;
  const CUBE_H = 44;
  const CUBE_Y = 118;
  const SHELF_Y = CUBE_Y + CUBE_H + 26;
  const HOST_Y = 318;
  const BRIDGE_Y = (V_BAND.y + V_BAND.h + U_BAND.y) / 2;
  const PROXY_H = 92;
  const PROXY_W = 72;
  const PROXY_Y = BRIDGE_Y - PROXY_H / 2;

  const nodes: Node[] = [
    {
      id: 'A', name: 'Node A', x: 260, host: '203.0.113.10', tags: ['underlay', 'A', 'flow:ingress', 'flow:ew'],
      workloads: [
        { id: 'proxy', name: 'netproxy', addr: 'ingress · DNS', slot: 0, tags: ['virtual', 'underlay', 'A', 'flow:ingress'] },
        { id: 'api', name: 'api', addr: 'fd5a:…:1', slot: 1, tags: ['virtual', 'A', 'flow:ew'] },
      ],
    },
    {
      id: 'B', name: 'Node B', x: 470, host: '203.0.113.11', tags: ['underlay', 'B', 'flow:ingress'],
      workloads: [
        { id: 'web', name: 'web', addr: 'fd5a:…:2', slot: 0, tags: ['virtual', 'B', 'flow:ingress'] },
        { id: 'db', name: 'db', addr: 'fd5a:…:3', slot: 1, tags: ['virtual', 'B'] },
      ],
    },
    {
      id: 'C', name: 'Node C', x: 680, host: '203.0.113.12', tags: ['underlay', 'C', 'flow:ew', 'flow:egress'],
      workloads: [
        { id: 'worker', name: 'worker', addr: 'fd5a:…:4', slot: 0, tags: ['virtual', 'C', 'flow:ew'] },
        { id: 'app', name: 'app', addr: 'fd5a:…:5', slot: 1, tags: ['virtual', 'C', 'flow:egress'] },
      ],
    },
  ];
  const byId = Object.fromEntries(nodes.map((n) => [n.id, n]));
  const hostX = (n: Node, off = 0) => n.x + COL_W / 2 + off;
  const cubeW = (w: Workload) => (w.id === 'proxy' ? PROXY_W : CUBE_W);
  const cubeX = (n: Node, w: Workload) => n.x + (w.id === 'proxy' ? 8 : w.slot === 0 ? 18 : COL_W - 18 - CUBE_W);
  const cubeCX = (n: Node, w: Workload) => cubeX(n, w) + cubeW(w) / 2;
  const cubeBottom = CUBE_Y + CUBE_H;
  const cubeTop = (w: Workload) => (w.id === 'proxy' ? PROXY_Y : CUBE_Y);
  const cubeHeight = (w: Workload) => (w.id === 'proxy' ? PROXY_H : CUBE_H);

  const clouds = [
    { id: 'in', x: 58, y: HOST_Y, tags: ['public', 'flow:ingress'] },
    { id: 'out', x: 956, y: HOST_Y, tags: ['public', 'flow:egress'] },
  ];

  type Tunnel = { id: string; tags: string[]; d: string };
  const straight = (a: Node, b: Node) => `M${hostX(a)},${HOST_Y} L${hostX(b)},${HOST_Y}`;
  const arc = (a: Node, b: Node) => `M${hostX(a)},${HOST_Y} Q${(hostX(a) + hostX(b)) / 2},${HOST_Y + 104} ${hostX(b)},${HOST_Y}`;
  const tunnels: Tunnel[] = [
    { id: 'AB', tags: ['underlay', 'wg', 'A', 'B', 'flow:ingress'], d: straight(byId.A, byId.B) },
    { id: 'BC', tags: ['underlay', 'wg', 'B', 'C'], d: straight(byId.B, byId.C) },
    { id: 'AC', tags: ['underlay', 'wg', 'A', 'C', 'flow:ew'], d: arc(byId.A, byId.C) },
  ];

  const down = (n: Node, w: Workload, off: number) =>
    `M${cubeCX(n, w)},${cubeBottom} L${cubeCX(n, w)},${SHELF_Y} L${hostX(n, off)},${SHELF_Y} L${hostX(n, off)},${HOST_Y}`;
  const up = (n: Node, w: Workload, off: number) =>
    `M${hostX(n, off)},${HOST_Y} L${hostX(n, off)},${SHELF_Y} L${cubeCX(n, w)},${SHELF_Y} L${cubeCX(n, w)},${cubeBottom}`;
  const strip = (d: string) => d.replace(/^M[^LQ]*/, ' ');

  type Step = { x: number; y: number; text: string };
  type Seg = { d: string; kind: Kind; arrow?: boolean };
  type Flow = {
    id: string; tag: string; title: string; segs: Seg[];
    motion: { d: string; kind: Kind; dur: number; begin: string }[]; steps: Step[];
  };

  const A = byId.A, B = byId.B, C = byId.C;
  const proxy = A.workloads[0], api = A.workloads[1], web = B.workloads[0], worker = C.workloads[0], app = C.workloads[1];

  const ewD = down(C, worker, 0) + strip(arc(C, A)) + strip(up(A, api, 0));
  const proxyRight = cubeX(A, proxy) + PROXY_W;
  const ingressEntryD = `M${clouds[0].x + 56},${HOST_Y} L${hostX(A)},${HOST_Y} L${hostX(A)},${HOST_Y - 18} L${cubeCX(A, proxy)},${HOST_Y - 18} L${cubeCX(A, proxy)},${PROXY_Y + PROXY_H}`;
  const ingressHopD = `M${proxyRight},${BRIDGE_Y - 20} L${hostX(A, 16)},${BRIDGE_Y - 20} L${hostX(A, 16)},${HOST_Y} L${hostX(B)},${HOST_Y}` + strip(up(B, web, 0));
  const egressD = down(C, app, 16) + ` L${clouds[1].x - 56},${HOST_Y}`;

  const flows: Flow[] = [
    {
      id: 'ingress', tag: 'flow:ingress', title: 'Public ingress',
      segs: [{ d: ingressEntryD, kind: 'ingress', arrow: true }, { d: ingressHopD, kind: 'virtual', arrow: true }],
      motion: [
        { d: ingressEntryD, kind: 'ingress', dur: 3, begin: '0s' },
        { d: ingressHopD, kind: 'virtual', dur: 4, begin: '3s' },
        { d: ingressEntryD, kind: 'ingress', dur: 3, begin: '3.5s' },
        { d: ingressHopD, kind: 'virtual', dur: 4, begin: '6.5s' },
      ],
      steps: [
        { x: 180, y: HOST_Y, text: 'A client connects to 203.0.113.10 on a published port.' },
        { x: cubeX(A, proxy) - 2, y: BRIDGE_Y, text: 'netproxy matches the SNI or Host header and picks the backend by its virtual address.' },
        { x: (hostX(A) + hostX(B)) / 2, y: HOST_Y, text: 'It continues as ordinary virtual traffic through the tunnel to Node B.' },
        { x: cubeCX(B, web), y: SHELF_Y, text: 'web receives it on its stable inbound address. The backend could be on any node.' },
      ],
    },
    {
      id: 'ew', tag: 'flow:ew', title: 'East-west traffic',
      segs: [{ d: ewD, kind: 'virtual', arrow: true }],
      motion: [
        { d: ewD, kind: 'virtual', dur: 6, begin: '0s' },
        { d: ewD, kind: 'virtual', dur: 6, begin: '3s' },
      ],
      steps: [
        { x: cubeCX(C, worker), y: SHELF_Y, text: 'worker resolves api.internal and gets api\'s stable address.' },
        { x: hostX(C), y: SHELF_Y + 36, text: 'Node C routes the packet into the tunnel to Node A.' },
        { x: (hostX(A) + hostX(C)) / 2, y: HOST_Y + 52, text: 'WireGuard encrypts it. Node A accepts it only from Node C\'s routed prefixes.' },
        { x: cubeCX(A, api), y: SHELF_Y, text: 'api receives it unchanged: no NAT on the path.' },
      ],
    },
    {
      id: 'egress', tag: 'flow:egress', title: 'Egress',
      segs: [{ d: egressD, kind: 'egress', arrow: true }],
      motion: [
        { d: egressD, kind: 'egress', dur: 3.5, begin: '0s' },
        { d: egressD, kind: 'egress', dur: 3.5, begin: '1.75s' },
      ],
      steps: [
        { x: cubeCX(C, app), y: SHELF_Y, text: 'app dials an external host from its outbound address.' },
        { x: hostX(C, 16), y: HOST_Y, text: 'Node C masquerades the flow to its host address.' },
        { x: 914, y: HOST_Y - 40, text: 'The destination sees 203.0.113.12. Replies map back to app.' },
      ],
    },
  ];
  const flowByTag = Object.fromEntries(flows.map((f) => [f.tag, f]));

  const details = buildDetails(nodes.map((n) => ({ id: n.id, name: n.name, host: n.host, workloads: n.workloads.map((w) => w.name) })));

  let hover = $state<string | null>(null);
  let pinned = $state<string | null>(null);

  const active = $derived(hover ?? pinned);
  const activeFlow = $derived(active ? flowByTag[active] : undefined);
  const lit = (tags: string[]) => !active || tags.includes(active);
  const detail = $derived(details[active ?? '_'] ?? details._);

  function enter(key: string) { hover = key; }
  function leave() { hover = null; }
  function toggle(key: string) { pinned = pinned === key ? null : key; }
</script>

<div class="nd">
  <div class="nd-stage">
    <svg viewBox="0 14 1016 406" role="img" aria-label="Three nodes drawn as columns. The top band is the virtual private network holding each node's workloads; the bottom band is the underlay with each node's host address, joined by WireGuard tunnels. Public traffic enters at Node A's host address, reaches netproxy, and is forwarded over a tunnel to a workload on Node B. A workload on Node C talks to one on Node A through a tunnel, and another leaves to the internet masqueraded as Node C.">
      <defs>
        <filter id="nd-glow" x="-50%" y="-50%" width="200%" height="200%">
          <feGaussianBlur stdDeviation="2.5" result="b" />
          <feMerge><feMergeNode in="b" /><feMergeNode in="SourceGraphic" /></feMerge>
        </filter>
        <filter id="nd-lift" x="-10%" y="-10%" width="120%" height="120%">
          <feDropShadow dx="0" dy="2" stdDeviation="3" class="nd-shadow" />
        </filter>
        <linearGradient id="nd-bridge" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" class="nd-stop-virtual" />
          <stop offset="0.5" class="nd-stop-virtual" />
          <stop offset="0.5" class="nd-stop-underlay" />
          <stop offset="1" class="nd-stop-underlay" />
        </linearGradient>
        {#each ['virtual', 'ingress', 'egress'] as k}
          <marker id={`nd-arrow-${k}`} viewBox="0 0 10 10" refX="7" refY="5" markerWidth="6" markerHeight="6" orient="auto">
            <path d="M1,1 L8,5 L1,9 z" class={`nd-fill-${k}`} />
          </marker>
        {/each}
      </defs>

      <!-- bands -->
      <g class="nd-layer" class:dim={!lit(['virtual'])}
        onpointerenter={() => enter('virtual')} onpointerleave={leave} onclick={() => toggle('virtual')} role="button" tabindex="-1">
        <rect x={V_BAND.x} y={V_BAND.y} width={V_BAND.w} height={V_BAND.h} rx="10" class="nd-plane" />
        <text x={V_BAND.x + 16} y={V_BAND.y + 26} class="nd-label nd-ink-virtual">Virtual IPv6</text>
        <text x={V_BAND.x + 16} y={V_BAND.y + 41} class="nd-label nd-ink-virtual">network</text>
        <text x={V_BAND.x + 16} y={V_BAND.y + 60} class="nd-sub nd-mono nd-ink-virtual nd-tiny">fd5a:9c1e:7a40::/48</text>
        <text x={V_BAND.x + 16} y={V_BAND.y + 78} class="nd-sub nd-tiny nd-ink-virtual">one /48 for the cluster</text>
        <text x={V_BAND.x + 16} y={V_BAND.y + 92} class="nd-sub nd-tiny nd-ink-virtual">a /64 per space</text>
        <text x={V_BAND.x + 16} y={V_BAND.y + 106} class="nd-sub nd-tiny nd-ink-virtual">a /88 per deployment</text>
      </g>
      <g class="nd-layer" class:dim={!lit(['underlay'])}
        onpointerenter={() => enter('underlay')} onpointerleave={leave} onclick={() => toggle('underlay')} role="button" tabindex="-1">
        <rect x={U_BAND.x} y={U_BAND.y} width={U_BAND.w} height={U_BAND.h} rx="10" class="nd-ground" />
        <text x={U_BAND.x + 16} y={U_BAND.y + 26} class="nd-label nd-ink-underlay">Underlay</text>
        <text x={U_BAND.x + 16} y={U_BAND.y + 41} class="nd-label nd-ink-underlay">network</text>
        <text x={U_BAND.x + 16} y={U_BAND.y + 58} class="nd-sub nd-tiny nd-ink-underlay">LAN, VPC, or internet</text>
      </g>

      <!-- public internet -->
      {#each clouds as c}
        <g class="nd-layer" class:dim={!lit(c.tags)}>
          <text x={c.x} y={c.y - 2} class="nd-label nd-center nd-small">Public internet</text>
          <text x={c.x} y={c.y + 13} class="nd-sub nd-center">{c.id === 'in' ? 'clients' : 'external services'}</text>
        </g>
      {/each}

      <!-- node columns -->
      {#each nodes as n}
        <g class="nd-layer" class:dim={!lit(n.tags)}
          onpointerenter={() => enter(n.id)} onpointerleave={leave} onclick={() => toggle(n.id)} role="button" tabindex="-1">
          <rect x={n.x} y={COL_TOP} width={COL_W} height={COL_BOTTOM - COL_TOP} rx="12" class="nd-col" />
          <text x={n.x + COL_W / 2} y={COL_TOP - 10} class="nd-label nd-center">{n.name}</text>
        </g>
      {/each}

      <!-- tunnels -->
      {#each tunnels as t}
        <g class="nd-layer" class:dim={!lit(t.tags)}
          onpointerenter={() => enter('wg')} onpointerleave={leave} onclick={() => toggle('wg')} role="button" tabindex="-1">
          <path d={t.d} class="nd-tube-hit" />
          <path d={t.d} class="nd-tube" />
          <path d={t.d} class="nd-tube-core" />
        </g>
      {/each}
      <text x={(hostX(A) + hostX(C)) / 2} y={U_BAND.y + U_BAND.h + 16} class="nd-sub nd-center nd-tiny nd-tube-label" class:dim={!lit(['underlay', 'wg', 'A', 'B', 'C', 'flow:ew', 'flow:ingress'])}>WireGuard tunnels: one device per node, one peer per remote node</text>

      <!-- hosts -->
      {#each nodes as n}
        <g class="nd-layer" class:dim={!lit(n.tags)}
          onpointerenter={() => enter(n.id)} onpointerleave={leave} onclick={() => toggle(n.id)} role="button" tabindex="-1">
          <circle cx={hostX(n)} cy={HOST_Y} r="6" class="nd-host" />
          <text x={hostX(n)} y={HOST_Y + 25} class="nd-sub nd-center nd-tiny">host address</text>
          <text x={hostX(n)} y={HOST_Y + 39} class="nd-mono nd-sub nd-center">{n.host}</text>
        </g>
      {/each}

      <!-- workloads -->
      {#each nodes as n}
        {#each n.workloads as w}
          <g class="nd-layer nd-wl" class:dim={!lit(w.tags)} class:proxy={w.id === 'proxy'}
            onpointerenter={() => enter(n.id)} onpointerleave={leave} onclick={() => toggle(n.id)} role="button" tabindex="-1">
            <rect x={cubeX(n, w)} y={cubeTop(w)} width={cubeW(w)} height={cubeHeight(w)} rx="7" class="nd-cube" />
            {#if w.id === 'proxy'}
              <text x={cubeCX(n, w)} y={PROXY_Y + 19} class="nd-label nd-center nd-small">{w.name}</text>
              <text x={cubeCX(n, w)} y={PROXY_Y + 33} class="nd-sub nd-center nd-tiny">{w.addr}</text>
              <text x={cubeCX(n, w)} y={PROXY_Y + PROXY_H - 30} class="nd-sub nd-center nd-tiny nd-ink-underlay">listens on</text>
              <text x={cubeCX(n, w)} y={PROXY_Y + PROXY_H - 18} class="nd-sub nd-center nd-tiny nd-ink-underlay">host address</text>
              <text x={cubeCX(n, w)} y={PROXY_Y + PROXY_H - 6} class="nd-mono nd-sub nd-center nd-tiny nd-ink-underlay">:443</text>
            {:else}
              <text x={cubeCX(n, w)} y={CUBE_Y + 19} class="nd-label nd-center nd-small">{w.name}</text>
              <text x={cubeCX(n, w)} y={CUBE_Y + 34} class="nd-mono nd-sub nd-center nd-tiny">{w.addr}</text>
            {/if}
          </g>
        {/each}
      {/each}

      <!-- flows -->
      {#each flows as f}
        <g class="nd-layer" class:dim={!lit([f.tag])}
          onpointerenter={() => enter(f.tag)} onpointerleave={leave} onclick={() => toggle(f.tag)} role="button" tabindex="-1">
          {#each f.segs as s}
            <path d={s.d} class="nd-flow-hit" />
            <path d={s.d} class={`nd-flow nd-flow-${s.kind}`} marker-end={s.arrow ? `url(#nd-arrow-${s.kind})` : undefined} />
          {/each}
          {#each f.motion as m}
            <circle r="4.5" class={`nd-packet nd-fill-${m.kind}`} filter="url(#nd-glow)" visibility="hidden">
              <set attributeName="visibility" to="visible" begin={m.begin} />
              <animateMotion dur={`${m.dur}s`} begin={m.begin} repeatCount="indefinite" path={m.d} calcMode="linear" />
            </circle>
          {/each}
        </g>
      {/each}

      <!-- step markers for the selected flow -->
      {#if activeFlow}
        <g class="nd-steps">
          {#each activeFlow.steps as s, i}
            <circle cx={s.x} cy={s.y} r="9" class={`nd-step nd-step-${activeFlow.id}`} />
            <text x={s.x} y={s.y + 3.5} class="nd-step-num nd-center">{i + 1}</text>
          {/each}
        </g>
      {/if}
    </svg>
  </div>

  <ul class="nd-legend" role="list">
      {#each legend as item}
        <li>
          <button type="button" class:active={active === item.key}
            onpointerenter={() => enter(item.key)} onpointerleave={leave} onclick={() => toggle(item.key)}>
            <span class={`nd-swatch nd-swatch-${item.swatch}`}></span>{item.label}
          </button>
        </li>
      {/each}
  </ul>
  <div class="nd-detail">
    {#if activeFlow}
      <h4>{activeFlow.title}{#if pinned}<span class="nd-hint">pinned · click again to release</span>{/if}</h4>
      <ol class="nd-step-cards">
        {#each activeFlow.steps as s, i}
          <li>
            <span class={`nd-step-badge nd-step-${activeFlow.id}`}>{i + 1}</span>
            <p>{s.text}</p>
          </li>
        {/each}
      </ol>
    {:else}
      <h4>{detail.title}{#if pinned}<span class="nd-hint">pinned · click again to release</span>{/if}</h4>
      <div class="nd-prose">
        {#each detail.body as line}<p>{line}</p>{/each}
      </div>
    {/if}
  </div>
</div>

<style>
  svg { width: 100%; height: auto; display: block; overflow: visible; font: 12px/1 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; }

  .nd-layer { transition: opacity 180ms ease; }
  .nd-layer.dim, .nd-tube-label.dim { opacity: 0.16; }
  .nd-layer.dim .nd-packet { visibility: hidden; }
  .nd-layer[role="button"] { cursor: pointer; outline: none; }

  .nd-ground { fill: var(--nd-underlay); fill-opacity: 0.06; stroke: var(--nd-underlay); stroke-opacity: 0.45; stroke-width: 1.25; stroke-dasharray: 6 4; }
  .nd-plane { fill: var(--nd-virtual); fill-opacity: 0.06; stroke: var(--nd-virtual); stroke-opacity: 0.45; stroke-width: 1.25; stroke-dasharray: 6 4; }

  .nd-col { fill: var(--nd-node); fill-opacity: 0.92; stroke: var(--nd-wg); stroke-opacity: 0.75; stroke-width: 1.25; filter: url(#nd-lift); }
  .nd-shadow { flood-color: #0f172a; flood-opacity: 0.16; }
  .nd-cube { fill: var(--nd-cube); stroke: var(--nd-virtual); stroke-width: 1.4; }
  .proxy .nd-cube { fill: url(#nd-bridge); stroke: var(--nd-wg); stroke-width: 1.4; }
  .nd-stop-virtual { stop-color: var(--nd-cube); }
  .nd-stop-underlay { stop-color: var(--nd-proxy-foot); }

  .nd-tube-hit { fill: none; stroke: transparent; stroke-width: 18; }
  .nd-tube { fill: none; stroke: var(--nd-wg); stroke-width: 10; stroke-linecap: round; opacity: 0.85; }
  .nd-tube-core { fill: none; stroke: var(--nd-wg-core); stroke-width: 5; stroke-linecap: round; }
  .nd-tube-label { transition: opacity 180ms ease; }

  .nd-host { fill: var(--nd-underlay); stroke: var(--card, #fff); stroke-width: 1.5; }

  .nd-flow-hit { fill: none; stroke: transparent; stroke-width: 14; }
  .nd-flow { fill: none; stroke-width: 2.4; stroke-linejoin: round; stroke-linecap: round; }
  .nd-flow-virtual { stroke: var(--nd-virtual); stroke-dasharray: 5 4; }
  .nd-flow-ingress { stroke: var(--nd-ingress); stroke-dasharray: 5 4; }
  .nd-flow-egress { stroke: var(--nd-egress); stroke-dasharray: 5 4; }
  .nd-fill-virtual { fill: var(--nd-virtual); }
  .nd-fill-ingress { fill: var(--nd-ingress); }
  .nd-fill-egress { fill: var(--nd-egress); }
  .nd-packet { stroke: var(--card, #fff); stroke-width: 1; }
  @media (prefers-reduced-motion: reduce) { .nd-packet { display: none; } }

  .nd-steps { pointer-events: none; }
  .nd-step { stroke: var(--card, #fff); stroke-width: 2; }
  .nd-step-ingress { fill: var(--nd-ingress); }
  .nd-step-ew { fill: var(--nd-virtual); }
  .nd-step-egress { fill: var(--nd-egress); }
  .nd-step-num { font-size: 10.5px; font-weight: 800; fill: #fff; }

  .nd-label { font-weight: 700; fill: var(--ink, #0f172a); }
  .nd-sub { font-size: 10.5px; fill: var(--sub, #475569); }
  .nd-small { font-size: 11px; }
  .nd-tiny { font-size: 9.5px; }
  .nd-role { font-weight: 400; }
  .nd-mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  .nd-center { text-anchor: middle; }
  .nd-ink-underlay { fill: var(--nd-underlay); }
  .nd-ink-virtual { fill: var(--nd-virtual); }
</style>
