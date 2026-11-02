<script lang="ts">
  import './network-diagram.css';

  type Kind = 'out' | 'in' | 'egress';
  type Box = { id: string; x: number; y: number; w: number; h: number; title: string; lines: string[]; tags: string[] };

  const NODE = { x: 30, y: 30, w: 956, h: 466 };
  const HOST = { x: 50, y: 216, w: 916, h: 260 };
  const PIPE_Y = 330;
  const PIPE_H = 92;
  const LANE_OUT = PIPE_Y + 20;
  const LANE_EGRESS = PIPE_Y + 34;
  const LANE_IN = PIPE_Y + 70;
  const UNDER = { x: 50, y: 518, w: 916, h: 40 };

  const containers = [
    { id: 'c1', name: 'api', x: 70, y: 66, w: 260, h: 124, ethX: 170, inbound: 'fd5a:…:1', outbound: 'fd5a:…:1:a1', tags: ['container', 'c1', 'flow:out', 'flow:in'] },
    { id: 'c2', name: 'web', x: 360, y: 66, w: 260, h: 124, ethX: 490, inbound: 'fd5a:…:2', outbound: 'fd5a:…:2:07', tags: ['container', 'c2', 'flow:egress'] },
  ];
  const ETH_H = 28;
  const ETH_W = 170;
  const ETH_PAD = 12;

  const pipe: Box[] = [
    { id: 'veth', x: 100, y: PIPE_Y, w: 140, h: PIPE_H, title: 'veth host ends', lines: ['one per container', 'gateway for I and O'], tags: ['host', 'veth', 'agent', 'flow:out', 'flow:in', 'flow:egress'] },
    { id: 'routing', x: 270, y: PIPE_Y, w: 170, h: PIPE_H, title: 'Routing table', lines: ['I, O → veth', 'remote prefixes → odwg0', 'cluster /48 → unreachable'], tags: ['host', 'routing', 'agent', 'flow:out', 'flow:in', 'flow:egress'] },
    { id: 'nft', x: 470, y: PIPE_Y, w: 190, h: PIPE_H, title: 'nftables', lines: ['source check per veth', 'policy by destination', 'DNAT published ports', 'masquerade egress'], tags: ['host', 'nft', 'agent', 'flow:out', 'flow:in', 'flow:egress'] },
    { id: 'wg', x: 690, y: PIPE_Y, w: 140, h: PIPE_H, title: 'odwg0 · WireGuard', lines: ['one peer per node', 'allowed-ips = its prefixes'], tags: ['host', 'wg', 'agent', 'flow:out', 'flow:in'] },
    { id: 'eth0', x: 860, y: PIPE_Y, w: 90, h: PIPE_H, title: 'eth0', lines: ['203.0.113.10', 'underlay interface'], tags: ['host', 'eth0', 'agent', 'flow:out', 'flow:in', 'flow:egress'] },
  ];
  const byId = Object.fromEntries(pipe.map((b) => [b.id, b]));
  const cx = (b: Box) => b.x + b.w / 2;
  const agent = { x: 700, y: 232, w: 250, h: 40 };

  const vethPaths = [
    `M170,${containers[0].y + containers[0].h - ETH_PAD} L170,${PIPE_Y}`,
    `M490,${containers[1].y + containers[1].h - ETH_PAD} L490,290 L200,290 L200,${PIPE_Y}`,
  ];
  const programPaths = pipe.map((b) => `M${agent.x + 60},${agent.y + agent.h} L${cx(b) + (b.id === 'eth0' ? -20 : 0)},${PIPE_Y}`);

  const gap = (a: Box, b: Box, y: number) => `M${a.x + a.w},${y} L${b.x},${y}`;
  const gapBack = (a: Box, b: Box, y: number) => `M${b.x},${y} L${a.x + a.w},${y}`;
  const e0 = byId.eth0;
  const outD = [
    `M178,${containers[0].y + containers[0].h - ETH_PAD} L178,${PIPE_Y}`,
    gap(byId.veth, byId.routing, LANE_OUT), gap(byId.routing, byId.nft, LANE_OUT), gap(byId.nft, byId.wg, LANE_OUT), gap(byId.wg, byId.eth0, LANE_OUT),
    `M${cx(e0) - 14},${PIPE_Y + PIPE_H} L${cx(e0) - 14},${UNDER.y}`,
  ];
  const inD = [
    `M${cx(e0) + 14},${UNDER.y} L${cx(e0) + 14},${PIPE_Y + PIPE_H}`,
    gapBack(byId.wg, byId.eth0, LANE_IN), gapBack(byId.nft, byId.wg, LANE_IN), gapBack(byId.routing, byId.nft, LANE_IN), gapBack(byId.veth, byId.routing, LANE_IN),
    `M162,${PIPE_Y} L162,${containers[0].y + containers[0].h - ETH_PAD}`,
  ];
  const egressD = [
    `M504,${containers[1].y + containers[1].h - ETH_PAD} L504,300 L208,300 L208,${PIPE_Y}`,
    gap(byId.veth, byId.routing, LANE_EGRESS), gap(byId.routing, byId.nft, LANE_EGRESS),
    `M${cx(byId.nft)},${PIPE_Y + PIPE_H} L${cx(byId.nft)},${PIPE_Y + PIPE_H + 30} L${cx(e0)},${PIPE_Y + PIPE_H + 30} L${cx(e0)},${UNDER.y}`,
  ];

  type Step = { x: number; y: number; text: string };
  type Flow = { id: string; tag: string; kind: Kind; title: string; d: string[]; steps: Step[] };
  const flows: Flow[] = [
    {
      id: 'out', tag: 'flow:out', kind: 'out', title: 'Outbound to another node',
      d: outD,
      steps: [
        { x: 178, y: 250, text: 'api connects from its outbound address to a workload address on another node.' },
        { x: cx(byId.routing), y: PIPE_Y - 2, text: 'The destination prefix is routed into odwg0.' },
        { x: cx(byId.nft), y: PIPE_Y - 2, text: 'The source address is checked against the veth it came from. Policy allows the destination.' },
        { x: cx(byId.wg), y: PIPE_Y - 2, text: 'WireGuard encrypts it for the peer that owns the prefix.' },
        { x: cx(e0) - 14, y: PIPE_Y + PIPE_H + 40, text: 'It leaves eth0 as UDP on the underlay.' },
      ],
    },
    {
      id: 'in', tag: 'flow:in', kind: 'in', title: 'Inbound from another node',
      d: inD,
      steps: [
        { x: cx(e0) + 14, y: PIPE_Y + PIPE_H + 40, text: 'UDP arrives on eth0 from a peer node.' },
        { x: cx(byId.wg), y: PIPE_Y + PIPE_H + 2, text: 'odwg0 decrypts it and drops it unless the source lies in that peer\'s allowed prefixes.' },
        { x: cx(byId.nft), y: PIPE_Y + PIPE_H + 2, text: 'The destination policy chain accepts or drops it.' },
        { x: cx(byId.routing), y: PIPE_Y + PIPE_H + 2, text: 'The host route for the inbound address points at the current run\'s veth.' },
        { x: 162, y: 250, text: 'api receives it on its stable inbound address.' },
      ],
    },
    {
      id: 'egress', tag: 'flow:egress', kind: 'egress', title: 'Egress to the internet',
      d: egressD,
      steps: [
        { x: 504, y: 240, text: 'web connects to an external host.' },
        { x: cx(byId.routing), y: PIPE_Y + PIPE_H + 2, text: 'The default route points at eth0.' },
        { x: cx(byId.nft), y: PIPE_Y + PIPE_H + 30, text: 'The flow leaves the cluster prefix, so it is masqueraded to the host address.' },
        { x: cx(e0), y: UNDER.y - 14, text: 'The internet sees 203.0.113.10. Replies map back through conntrack.' },
      ],
    },
  ];
  const flowByTag = Object.fromEntries(flows.map((f) => [f.tag, f]));

  const legend = [
    { key: 'container', label: 'Container namespace', swatch: 'container' },
    { key: 'host', label: 'Host namespace', swatch: 'host' },
    { key: 'veth', label: 'veth pair', swatch: 'veth' },
    { key: 'agent', label: 'Agent programs', swatch: 'agent' },
    { key: 'flow:out', label: 'Outbound to a node', swatch: 'ew' },
    { key: 'flow:in', label: 'Inbound from a node', swatch: 'in' },
    { key: 'flow:egress', label: 'Egress', swatch: 'egress' },
  ];

  const details: Record<string, { title: string; body: string[] }> = {
    _: { title: 'Inside one node', body: ['One host network namespace, one container namespace per deployment instance, and a veth pair between each of them.', 'Hover or click an item to explore it.'] },
    container: { title: 'Container network namespace', body: ['One per deployment instance. Its interface carries the stable inbound address and the run-scoped outbound address.', 'The process binds and connects here and sees nothing of the host.'] },
    c1: { title: 'api', body: ['Its own namespace with one interface. Inbound address fd5a:…:1, outbound address fd5a:…:1:a1.'] },
    c2: { title: 'web', body: ['Its own namespace with one interface. Inbound address fd5a:…:2, outbound address fd5a:…:2:07.'] },
    host: { title: 'Host network namespace', body: ['The node\'s main namespace, where the underlay interface lives.', 'Everything OpenDeploy programs lives here. Nothing is changed inside containers beyond their one interface.'] },
    veth: { title: 'veth pair', body: ['A virtual cable with one end in each namespace.', 'The host end is the container\'s gateway and the interface its source check is keyed on.'] },
    routing: { title: 'Routing table', body: ['Host routes for the inbound and outbound addresses point at the veth. Rollover flips the inbound route to the new run.', 'Remote prefixes point into odwg0. The cluster /48 is unreachable by default so unknown addresses never leak out.'] },
    nft: { title: 'nftables', body: ['Filter chains check the source address per veth and dispatch by destination to per-deployment policy chains.', 'NAT chains DNAT published ports and masquerade flows leaving the cluster prefix.'] },
    wg: { title: 'WireGuard device', body: ['One device per node, one peer per remote node, keys minted locally.', 'Each peer\'s allowed-ips are exactly the prefixes routed to it, so the kernel enforces node-level source attribution.'] },
    eth0: { title: 'Underlay interface', body: ['The interface holding the node\'s host address on the underlay.', 'Tunnel traffic and masqueraded egress leave here. Published ports are answered here.'] },
    agent: { title: 'OpenDeploy agent', body: ['Runs in the host namespace. Creates each container namespace and veth, then programs routes, nftables, and WireGuard over netlink.', 'It is never on the packet path.'] },
  };

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
    <svg viewBox="0 0 1016 568" role="img" aria-label="Inside one node. Two container network namespaces, each holding one interface, sit above the host network namespace. A veth pair joins each container to the host. In the host namespace the veth ends, routing table, nftables, WireGuard device, and underlay interface are arranged in the order packets pass through them, programmed by the OpenDeploy agent. Below the node is the underlay network.">
      <defs>
        {#each ['out', 'in', 'egress'] as k}
          <marker id={`ndn-arrow-${k}`} viewBox="0 0 10 10" refX="7" refY="5" markerWidth="6" markerHeight="6" orient="auto">
            <path d="M1,1 L8,5 L1,9 z" class={`ndn-fill-${k}`} />
          </marker>
        {/each}
      </defs>

      <!-- node outline -->
      <text x={NODE.x + NODE.w / 2} y={NODE.y - 10} class="nd-label nd-center">Node A</text>
      <rect x={NODE.x} y={NODE.y} width={NODE.w} height={NODE.h} rx="12" class="ndn-node" />

      <!-- host namespace -->
      <g class="nd-layer" class:dim={!lit(['host', 'agent', 'veth', 'routing', 'nft', 'wg', 'eth0', 'flow:out', 'flow:in', 'flow:egress'])}
        onpointerenter={() => enter('host')} onpointerleave={leave} onclick={() => toggle('host')} role="button" tabindex="-1">
        <rect x={HOST.x} y={HOST.y} width={HOST.w} height={HOST.h} rx="10" class="ndn-host" />
        <text x={HOST.x + 14} y={HOST.y + HOST.h - 12} class="nd-sub nd-tiny ndn-host-label">host network namespace</text>
      </g>

      <!-- underlay -->
      <g class="nd-layer" class:dim={!lit(['flow:out', 'flow:in', 'flow:egress', 'eth0'])}>
        <rect x={UNDER.x} y={UNDER.y} width={UNDER.w} height={UNDER.h} rx="8" class="ndn-underlay" />
        <text x={UNDER.x + 14} y={UNDER.y + 25} class="nd-label nd-ink-underlay">Underlay network</text>
        <text x={UNDER.x + UNDER.w - 14} y={UNDER.y + 25} class="nd-sub nd-ink-underlay ndn-end">other nodes over WireGuard · the internet</text>
      </g>

      <!-- containers -->
      {#each containers as c}
        <g class="nd-layer" class:dim={!lit(c.tags)}
          onpointerenter={() => enter(c.id)} onpointerleave={leave} onclick={() => toggle(c.id)} role="button" tabindex="-1">
          <rect x={c.x} y={c.y} width={c.w} height={c.h} rx="10" class="ndn-container" />
          <text x={c.x + 14} y={c.y + 24} class="nd-label">{c.name}</text>
          <text x={c.x + 14} y={c.y + 40} class="nd-sub nd-tiny">container network namespace</text>
          <text x={c.x + 14} y={c.y + 60} class="nd-sub nd-tiny">process binds and connects here</text>
          <rect x={c.ethX - ETH_W / 2} y={c.y + c.h - ETH_H - ETH_PAD} width={ETH_W} height={ETH_H} rx="6" class="ndn-chip" />
          <text x={c.ethX} y={c.y + c.h - ETH_H - ETH_PAD + 12} class="nd-label nd-center nd-tiny">eth0</text>
          <text x={c.ethX} y={c.y + c.h - ETH_H - ETH_PAD + 23} class="nd-mono nd-sub nd-center nd-tiny">I {c.inbound} · O {c.outbound}</text>
        </g>
      {/each}
      <text x={660} y={100} class="nd-sub nd-tiny">one namespace</text>
      <text x={660} y={113} class="nd-sub nd-tiny">per deployment instance</text>

      <!-- veth pairs -->
      {#each vethPaths as d, i}
        <g class="nd-layer" class:dim={!lit(['veth', containers[i].id, 'host'])}
          onpointerenter={() => enter('veth')} onpointerleave={leave} onclick={() => toggle('veth')} role="button" tabindex="-1">
          <path {d} class="ndn-veth-hit" />
          <path {d} class="ndn-veth" />
          <path {d} class="ndn-veth-core" />
        </g>
      {/each}
      <text x={205} y={252} class="nd-sub nd-tiny ndn-veth-label" class:dim={!lit(['veth', 'c1', 'host'])}>veth pair</text>

      <!-- agent -->
      <g class="nd-layer" class:dim={!lit(['agent', 'host'])}
        onpointerenter={() => enter('agent')} onpointerleave={leave} onclick={() => toggle('agent')} role="button" tabindex="-1">
        {#each programPaths as d}
          <path {d} class="ndn-program" />
        {/each}
        <rect x={agent.x} y={agent.y} width={agent.w} height={agent.h} rx="8" class="ndn-agent" />
        <text x={agent.x + 14} y={agent.y + 17} class="nd-label nd-small">OpenDeploy agent</text>
        <text x={agent.x + 14} y={agent.y + 31} class="nd-sub nd-tiny">programs the host namespace over netlink</text>
      </g>

      <!-- pipeline boxes -->
      {#each pipe as b}
        <g class="nd-layer" class:dim={!lit(b.tags)}
          onpointerenter={() => enter(b.id)} onpointerleave={leave} onclick={() => toggle(b.id)} role="button" tabindex="-1">
          <rect x={b.x} y={b.y} width={b.w} height={b.h} rx="8" class={`ndn-box ndn-box-${b.id}`} />
          <text x={cx(b)} y={b.y + 18} class="nd-label nd-center nd-small">{b.title}</text>
          {#each b.lines as line, i}
            <text x={cx(b)} y={b.y + 36 + i * 13} class="nd-sub nd-center nd-tiny">{line}</text>
          {/each}
        </g>
      {/each}

      <!-- flows -->
      {#each flows as f}
        <g class="nd-layer" class:dim={!lit([f.tag])}
          onpointerenter={() => enter(f.tag)} onpointerleave={leave} onclick={() => toggle(f.tag)} role="button" tabindex="-1">
          {#each f.d as d}
            <path {d} class="ndn-flow-hit" />
            <path {d} class={`ndn-flow ndn-flow-${f.kind}`} marker-end={`url(#ndn-arrow-${f.kind})`} />
          {/each}
        </g>
      {/each}

      {#if activeFlow}
        <g class="ndn-steps">
          {#each activeFlow.steps as s, i}
            <circle cx={s.x} cy={s.y} r="9" class={`ndn-step ndn-step-${activeFlow.kind}`} />
            <text x={s.x} y={s.y + 3.5} class="ndn-step-num nd-center">{i + 1}</text>
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
            <span class={`nd-step-badge ndn-step-${activeFlow.kind}`}>{i + 1}</span>
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
  .nd-layer.dim, .ndn-veth-label.dim { opacity: 0.16; }
  .nd-layer[role="button"] { cursor: pointer; outline: none; }
  .ndn-veth-label { transition: opacity 180ms ease; }

  .ndn-node { fill: none; stroke: var(--nd-wg); stroke-opacity: 0.45; stroke-width: 1; }
  .ndn-host { fill: var(--nd-slab); fill-opacity: 0.55; stroke: var(--nd-wg); stroke-opacity: 0.45; stroke-width: 1; }
  .ndn-host-label { font-weight: 700; }
  .ndn-underlay { fill: var(--nd-underlay-fill); stroke: var(--nd-underlay-line); stroke-width: 1.5; }
  .ndn-container { fill: var(--nd-virtual-fill); stroke: var(--nd-virtual-line); stroke-width: 1.5; }
  .ndn-chip { fill: var(--nd-cube); stroke: var(--nd-virtual); stroke-width: 1.2; }
  .ndn-box { fill: var(--card, #fff); stroke: var(--nd-wg); stroke-width: 1.2; }
  .ndn-box-eth0 { stroke: var(--nd-underlay); }
  .ndn-agent { fill: var(--card, #fff); stroke: var(--ink, #0f172a); stroke-width: 1.2; }
  .ndn-program { fill: none; stroke: var(--ink, #0f172a); stroke-opacity: 0.35; stroke-width: 1; stroke-dasharray: 2 3; }

  .ndn-veth-hit { fill: none; stroke: transparent; stroke-width: 18; }
  .ndn-veth { fill: none; stroke: var(--nd-wg); stroke-width: 8; stroke-linecap: round; stroke-linejoin: round; opacity: 0.85; }
  .ndn-veth-core { fill: none; stroke: var(--nd-wg-core); stroke-width: 4; stroke-linecap: round; stroke-linejoin: round; }

  .ndn-flow-hit { fill: none; stroke: transparent; stroke-width: 12; }
  .ndn-flow { fill: none; stroke-width: 2.2; stroke-linejoin: round; stroke-linecap: round; stroke-dasharray: 5 4; }
  .ndn-flow-out { stroke: var(--nd-virtual); }
  .ndn-flow-in { stroke: var(--nd-virtual); stroke-dasharray: 2 3; }
  .ndn-flow-egress { stroke: var(--nd-egress); }
  .ndn-fill-out, .ndn-fill-in { fill: var(--nd-virtual); }
  .ndn-fill-egress { fill: var(--nd-egress); }

  .ndn-steps { pointer-events: none; }
  .ndn-step { stroke: var(--card, #fff); stroke-width: 2; }
  .ndn-step-out, .ndn-step-in { fill: var(--nd-virtual); }
  .ndn-step-egress { fill: var(--nd-egress); }
  .ndn-step-num { font-size: 10.5px; font-weight: 800; fill: #fff; }

  .nd-label { font-weight: 700; fill: var(--ink, #0f172a); }
  .nd-sub { font-size: 10.5px; fill: var(--sub, #475569); }
  .nd-small { font-size: 11px; }
  .nd-tiny { font-size: 9.5px; }
  .nd-mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  .nd-center { text-anchor: middle; }
  .ndn-end { text-anchor: end; }
  .nd-ink-underlay { fill: var(--nd-underlay); }
</style>
