<script lang="ts">
  import './network-diagram.css';

  type Kind = 'update' | 'status' | 'refused';
  type Identity = {
    id: string; name: string; role: string; kind: 'node' | 'user'; x: number; port: number;
    holds: string[]; tags: string[];
  };

  const LOG = { x: 60, y: 40, w: 896, h: 92 };
  const CELL = { y: LOG.y + 12, w: 56, h: 38, gap: 8, x0: 240 };
  const LANE_IN = LOG.y + LOG.h - 22;
  const LANE_OUT = LOG.y + LOG.h - 10;
  const PRIMARY = { x: 383, y: 176, w: 250, h: 84 };
  const PX = PRIMARY.x + PRIMARY.w / 2;
  const SHELF_Y = 312;
  const IDENT = { y: 352, w: 190, h: 112 };
  const CHIP = { h: 44, pad: 12 };

  const cells = [
    { seq: '1' }, { seq: '2' }, { seq: '…' },
    { seq: '118' }, { seq: '119' }, { seq: '120' }, { seq: '121' }, { seq: '122' }, { seq: '123' }, { seq: '124' },
    { seq: '125', next: true },
  ];
  const cellX = (i: number) => CELL.x0 + i * (CELL.w + CELL.gap);
  const nextCell = cells.length - 1;
  const NEXT_CX = cellX(nextCell) + CELL.w / 2;

  const identities: Identity[] = [
    { id: 'B', name: 'Node B', role: 'secondary node', kind: 'node', x: 60, port: -60,
      holds: ['its scheduled instances', 'its network map'], tags: ['node', 'B', 'flow:update', 'flow:status'] },
    { id: 'C', name: 'Node C', role: 'secondary node', kind: 'node', x: 290, port: -20,
      holds: ['its scheduled instances', 'its network map'], tags: ['node', 'C', 'flow:status'] },
    { id: 'alice', name: 'Alice', role: 'user · browser', kind: 'user', x: 536, port: 20,
      holds: ['everything in spaces 1 and 2', 'her grants allow both'], tags: ['user', 'alice', 'flow:update', 'flow:status'] },
    { id: 'bob', name: 'Bob', role: 'user · browser', kind: 'user', x: 766, port: 60,
      holds: ['everything in space 2', 'his grants stop there'], tags: ['user', 'bob', 'flow:refused'] },
  ];
  const byId = Object.fromEntries(identities.map((n) => [n.id, n]));
  const cx = (n: Identity) => n.x + IDENT.w / 2;
  const portX = (n: Identity) => PX + n.port;
  const PRIMARY_BOTTOM = PRIMARY.y + PRIMARY.h;

  const session = (n: Identity) => `M${cx(n)},${IDENT.y} L${cx(n)},${SHELF_Y} L${portX(n)},${SHELF_Y} L${portX(n)},${PRIMARY_BOTTOM}`;
  const up = (n: Identity, off: number) =>
    `M${cx(n) + off},${IDENT.y} L${cx(n) + off},${SHELF_Y + off} L${portX(n) + off},${SHELF_Y + off} L${portX(n) + off},${PRIMARY_BOTTOM}`;
  const down = (n: Identity, off: number) =>
    `M${portX(n) + off},${PRIMARY_BOTTOM} L${portX(n) + off},${SHELF_Y + off} L${cx(n) + off},${SHELF_Y + off} L${cx(n) + off},${IDENT.y}`;
  const appendD = `M${PX - 5},${PRIMARY.y} L${PX - 5},${LANE_IN} L${NEXT_CX},${LANE_IN} L${NEXT_CX},${CELL.y + CELL.h}`;
  const readD = `M${NEXT_CX + 8},${CELL.y + CELL.h} L${NEXT_CX + 8},${LANE_OUT} L${PX + 5},${LANE_OUT} L${PX + 5},${PRIMARY.y}`;
  const logTubeD = `M${PX},${LOG.y + LOG.h} L${PX},${PRIMARY.y}`;

  type Step = { x: number; y: number; text: string };
  type Seg = { d: string; arrow?: boolean };
  type Flow = {
    id: Kind; tag: string; title: string; segs: Seg[];
    motion: { d: string; dur: number; begin: string }[]; steps: Step[];
  };

  const B = byId.B, C = byId.C, alice = byId.alice, bob = byId.bob;

  const flows: Flow[] = [
    {
      id: 'update', tag: 'flow:update', title: 'A user saves a change',
      segs: [
        { d: up(alice, -4), arrow: true }, { d: appendD, arrow: true }, { d: readD, arrow: true },
        { d: down(B, 4), arrow: true }, { d: down(alice, 4), arrow: true },
      ],
      motion: [
        { d: up(alice, -4), dur: 2.5, begin: '0s' },
        { d: appendD, dur: 2.5, begin: '2.5s' },
        { d: readD, dur: 2.5, begin: '5s' },
        { d: down(B, 4), dur: 2.5, begin: '7.5s' },
        { d: down(alice, 4), dur: 2.5, begin: '7.5s' },
      ],
      steps: [
        { x: cx(alice) - 4, y: IDENT.y - 22, text: 'Alice saves a deployment in space 1 from the web UI. Her browser sends the change to the primary with the version it was edited from.' },
        { x: portX(alice) - 4, y: PRIMARY_BOTTOM + 2, text: 'The primary checks her grants, that the version is still current, and that the change is valid against the state it has.' },
        { x: NEXT_CX, y: CELL.y + CELL.h + 4, text: 'The change is appended to the event log as the next event. Earlier versions stay in the log.' },
        { x: cx(B) + 4, y: IDENT.y - 22, text: 'Node B runs that deployment, so the primary pushes it the updated projection, and pushes Alice\'s browser the committed version. Nothing is pushed to Node C or Bob.' },
      ],
    },
    {
      id: 'status', tag: 'flow:status', title: 'A node reports readiness',
      segs: [{ d: up(C, -4), arrow: true }, { d: appendD, arrow: true }, { d: readD, arrow: true }, { d: down(B, 4), arrow: true }, { d: down(alice, 4), arrow: true }],
      motion: [
        { d: up(C, -4), dur: 2.5, begin: '0s' },
        { d: appendD, dur: 2.5, begin: '2.5s' },
        { d: readD, dur: 2.5, begin: '5s' },
        { d: down(B, 4), dur: 2.5, begin: '7.5s' },
        { d: down(alice, 4), dur: 2.5, begin: '7.5s' },
      ],
      steps: [
        { x: cx(C) - 4, y: IDENT.y - 22, text: 'A rollover candidate on Node C signals that it is ready, and Node C reports it. Nodes are identities too, and a status report is a change to the state.' },
        { x: portX(C) - 4, y: PRIMARY_BOTTOM + 2, text: 'The primary accepts it because the instance is scheduled on Node C. In the same commit it moves the old placement on Node B to draining and the candidate to serving.' },
        { x: NEXT_CX, y: CELL.y + CELL.h + 4, text: 'Both changes are appended to the log as one event.' },
        { x: cx(B) + 4, y: IDENT.y - 22, text: 'Node B\'s network map now routes the inbound address to Node C, so the primary pushes it an updated projection. Alice\'s browser is pushed the new instance states.' },
      ],
    },
    {
      id: 'refused', tag: 'flow:refused', title: 'A change is refused',
      segs: [{ d: up(bob, -4), arrow: true }, { d: down(bob, 4), arrow: true }],
      motion: [
        { d: up(bob, -4), dur: 2.5, begin: '0s' },
        { d: down(bob, 4), dur: 2.5, begin: '2.5s' },
      ],
      steps: [
        { x: cx(bob) - 70, y: SHELF_Y - 4, text: 'Bob tries to change a deployment in space 1, which his grants do not cover.' },
        { x: portX(bob) - 4, y: PRIMARY_BOTTOM + 2, text: 'The primary refuses it. Nothing is appended to the log and no other identity hears of it. A valid change made against a stale version is refused the same way.' },
        { x: cx(bob) + 4, y: IDENT.y - 22, text: 'Bob receives the error and his projection is unchanged.' },
      ],
    },
  ];
  const flowByTag = Object.fromEntries(flows.map((f) => [f.tag, f]));
  const flowTags = flows.map((f) => f.tag);

  const legend = [
    { key: 'log', label: 'Event log', swatch: 'log' },
    { key: 'primary', label: 'Primary node', swatch: 'primary' },
    { key: 'node', label: 'Secondary node', swatch: 'identity' },
    { key: 'user', label: 'User', swatch: 'identity' },
    { key: 'projection', label: 'Projection', swatch: 'projection' },
    { key: 'flow:update', label: 'A user saves a change', swatch: 'ew' },
    { key: 'flow:status', label: 'A node reports readiness', swatch: 'egress' },
    { key: 'flow:refused', label: 'A change is refused', swatch: 'ingress' },
  ];

  const details: Record<string, { title: string; body: string[] }> = {
    _: { title: 'Cluster state at a glance', body: ['The cluster state is a set of resource instances. Changes are appended as events to a log held by the primary, and every other identity holds only the part of the state that concerns it.', 'Hover or click an item to explore it.'] },
    log: { title: 'Event log', body: ['An append-only record of every change ever made, holding the full version history of every resource. One event holds all the changes made together. The current state is the log folded forward, and any earlier version can be read back.', 'This is the version control for your deployment configuration. Nothing is edited in place and nothing is lost.'] },
    primary: { title: 'Primary node', body: ['The one identity that stores the whole log. Every change from any identity arrives here, is checked against that identity\'s permissions and the current state, and is appended in order.', 'It pushes each connected identity the part of the state relevant to it, and every later change to that part as it is committed.'] },
    session: { title: 'Sessions', body: ['Every identity holds one long-lived session with the primary.', 'Changes travel up a session and projections are pushed down it. A dropped session is reopened with a fresh projection.'] },
    node: { title: 'Secondary node', body: ['A machine that runs workloads. It holds only its projection: the instances scheduled on it, the configuration those instances need, and its network map.', 'It never stores the log. A node also makes changes, reporting the status of its instances, within what its role allows.'] },
    B: { title: 'Node B', body: ['A secondary node holding only its own projection: its scheduled instances and its network map.', 'It runs Alice\'s deployment, so a change to that deployment is pushed to it, as is anything that changes its network map.'] },
    C: { title: 'Node C', body: ['A secondary node holding only its own projection: its scheduled instances and its network map.', 'It reports the status of the instances it runs, and those reports are changes to the state like any other.'] },
    user: { title: 'User', body: ['A person using the web UI or the API. Their browser holds the entities their grants allow them to view and is pushed each later change to those entities as it happens.', 'Changes they make go to the primary and are accepted only when their grants allow them.'] },
    alice: { title: 'Alice', body: ['Her grants cover spaces 1 and 2, so her browser holds everything in both and hears every change to them.'] },
    bob: { title: 'Bob', body: ['His grants cover space 2 only. His browser holds nothing from space 1 and hears nothing about it, and a change he tries to make there is refused.'] },
    projection: { title: 'Projection', body: ['What a non-primary identity holds: a partial copy of the state scoped to that identity. Nodes get the instances and network map for their machine; users get the entities their grants allow.', 'A projection is replaced from the primary whenever the identity reconnects or its permissions change, so losing it costs nothing.'] },
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
    <svg viewBox="0 20 1016 456" role="img" aria-label="The event log drawn as a strip of numbered events across the top, with the primary node below it holding the log. Beneath the primary are two secondary nodes and two users, each connected to the primary by a session and each holding a small projection of the state. A change travels from a user up to the primary, is appended to the log, and is pushed back down to the node and user it concerns; a node reporting that a rollover candidate is ready takes the same path and reaches the other node; a change the user is not allowed to make is turned back at the primary.">
      <defs>
        <filter id="ndc-glow" x="-50%" y="-50%" width="200%" height="200%">
          <feGaussianBlur stdDeviation="2.5" result="b" />
          <feMerge><feMergeNode in="b" /><feMergeNode in="SourceGraphic" /></feMerge>
        </filter>
        {#each ['update', 'status', 'refused'] as k}
          <marker id={`ndc-arrow-${k}`} viewBox="0 0 10 10" refX="7" refY="5" markerWidth="6" markerHeight="6" orient="auto">
            <path d="M1,1 L8,5 L1,9 z" class={`ndc-fill-${k}`} />
          </marker>
        {/each}
      </defs>

      <!-- event log -->
      <g class="nd-layer" class:dim={!lit(['log', 'flow:update', 'flow:status'])}
        onpointerenter={() => enter('log')} onpointerleave={leave} onclick={() => toggle('log')} role="button" tabindex="-1">
        <rect x={LOG.x} y={LOG.y} width={LOG.w} height={LOG.h} rx="10" class="ndc-log" />
        <text x={LOG.x + 16} y={LOG.y + 28} class="nd-label nd-ink-underlay">Event log</text>
        <text x={LOG.x + 16} y={LOG.y + 46} class="nd-sub nd-tiny nd-ink-underlay">append-only</text>
        <text x={LOG.x + 16} y={LOG.y + 60} class="nd-sub nd-tiny nd-ink-underlay">full version history</text>
        <text x={LOG.x + 16} y={LOG.y + 74} class="nd-sub nd-tiny nd-ink-underlay">held by the primary</text>
        {#each cells as c, i}
          <rect x={cellX(i)} y={CELL.y} width={CELL.w} height={CELL.h} rx="5" class="ndc-cell" class:next={c.next} />
          <text x={cellX(i) + CELL.w / 2} y={CELL.y + 24} class="nd-label nd-center nd-small nd-mono" class:ndc-next-ink={c.next}>{c.seq}</text>
        {/each}
      </g>

      <!-- primary and its log link -->
      <g class="nd-layer" class:dim={!lit(['log', 'primary', 'flow:update', 'flow:status'])}
        onpointerenter={() => enter('primary')} onpointerleave={leave} onclick={() => toggle('primary')} role="button" tabindex="-1">
        <path d={logTubeD} class="nd-tube-hit" />
        <path d={logTubeD} class="nd-tube" />
        <path d={logTubeD} class="nd-tube-core" />
      </g>
      <g class="nd-layer" class:dim={!lit(['primary', ...flowTags])}
        onpointerenter={() => enter('primary')} onpointerleave={leave} onclick={() => toggle('primary')} role="button" tabindex="-1">
        <rect x={PRIMARY.x} y={PRIMARY.y} width={PRIMARY.w} height={PRIMARY.h} rx="10" class="ndc-primary" />
        <text x={PRIMARY.x + 16} y={PRIMARY.y + 24} class="nd-label">Primary node</text>
        <text x={PRIMARY.x + 16} y={PRIMARY.y + 42} class="nd-sub nd-tiny">stores the whole event log</text>
        <text x={PRIMARY.x + 16} y={PRIMARY.y + 56} class="nd-sub nd-tiny">checks and appends every change</text>
        <text x={PRIMARY.x + 16} y={PRIMARY.y + 70} class="nd-sub nd-tiny">pushes each identity its projection</text>
      </g>

      <!-- sessions -->
      {#each identities as n}
        <g class="nd-layer" class:dim={!lit(['session', ...n.tags])}
          onpointerenter={() => enter('session')} onpointerleave={leave} onclick={() => toggle('session')} role="button" tabindex="-1">
          <path d={session(n)} class="nd-tube-hit" />
          <path d={session(n)} class="nd-tube" />
          <path d={session(n)} class="nd-tube-core" />
        </g>
      {/each}
      <text x={PRIMARY.x + PRIMARY.w + 14} y={PRIMARY_BOTTOM + 30} class="nd-sub nd-tiny nd-tube-label" class:dim={!lit(['session', 'node', 'user', 'B', 'C', 'alice', 'bob', ...flowTags])}>sessions: changes travel up, projections are pushed down</text>

      <!-- identities -->
      {#each identities as n}
        <g class="nd-layer" class:dim={!lit(n.tags)}
          onpointerenter={() => enter(n.id)} onpointerleave={leave} onclick={() => toggle(n.id)} role="button" tabindex="-1">
          <rect x={n.x} y={IDENT.y} width={IDENT.w} height={IDENT.h} rx="12" class={n.kind === 'node' ? 'ndc-node' : 'ndc-user'} />
          <text x={n.x + 14} y={IDENT.y + 24} class="nd-label">{n.name}</text>
          <text x={n.x + 14} y={IDENT.y + 40} class="nd-sub nd-tiny">{n.role}</text>
        </g>
        <g class="nd-layer" class:dim={!lit(['projection', ...n.tags])}
          onpointerenter={() => enter('projection')} onpointerleave={leave} onclick={() => toggle('projection')} role="button" tabindex="-1">
          <rect x={n.x + CHIP.pad} y={IDENT.y + IDENT.h - CHIP.h - CHIP.pad} width={IDENT.w - CHIP.pad * 2} height={CHIP.h} rx="6" class="ndc-chip" />
          <text x={n.x + CHIP.pad + 10} y={IDENT.y + IDENT.h - CHIP.h - CHIP.pad + 15} class="nd-label nd-tiny nd-ink-virtual">projection</text>
          <text x={n.x + CHIP.pad + 10} y={IDENT.y + IDENT.h - CHIP.h - CHIP.pad + 27} class="nd-sub nd-tiny">{n.holds[0]}</text>
          <text x={n.x + CHIP.pad + 10} y={IDENT.y + IDENT.h - CHIP.h - CHIP.pad + 38} class="nd-sub nd-tiny">{n.holds[1]}</text>
        </g>
      {/each}

      <!-- flows -->
      {#each flows as f}
        <g class="nd-layer" class:dim={!lit([f.tag])}
          onpointerenter={() => enter(f.tag)} onpointerleave={leave} onclick={() => toggle(f.tag)} role="button" tabindex="-1">
          {#each f.segs as s}
            <path d={s.d} class="nd-flow-hit" />
            <path d={s.d} class={`nd-flow ndc-flow-${f.id}`} marker-end={s.arrow ? `url(#ndc-arrow-${f.id})` : undefined} />
          {/each}
          {#each f.motion as m}
            <circle r="4.5" class={`nd-packet ndc-fill-${f.id}`} filter="url(#ndc-glow)" visibility="hidden">
              <set attributeName="visibility" to="visible" begin={m.begin} />
              <animateMotion dur={`${m.dur}s`} begin={m.begin} repeatCount="indefinite" path={m.d} calcMode="linear" />
            </circle>
          {/each}
        </g>
      {/each}

      <!-- step markers for the selected flow -->
      {#if activeFlow}
        <g class="ndc-steps">
          {#each activeFlow.steps as s, i}
            <circle cx={s.x} cy={s.y} r="9" class={`nd-step ndc-step-${activeFlow.id}`} />
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
            <span class={`nd-step-badge ndc-step-${activeFlow.id}`}>{i + 1}</span>
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

  .ndc-log { fill: var(--nd-underlay-fill); stroke: var(--nd-underlay-line); stroke-width: 1.5; }
  .ndc-cell { fill: var(--card, #fff); stroke: var(--nd-underlay-line); stroke-width: 1.2; }
  .ndc-cell.next { fill: transparent; stroke-dasharray: 4 3; }
  .ndc-next-ink { fill: var(--nd-underlay); }
  .ndc-primary { fill: var(--nd-slab); stroke: var(--nd-wg); stroke-width: 1.4; }
  .ndc-node { fill: var(--nd-slab); fill-opacity: 0.55; stroke: var(--nd-wg); stroke-opacity: 0.45; stroke-width: 1; }
  .ndc-user { fill: var(--nd-slab); fill-opacity: 0.55; stroke: var(--nd-wg); stroke-opacity: 0.45; stroke-width: 1; }
  .ndc-chip { fill: var(--nd-cube); stroke: var(--nd-virtual); stroke-width: 1.2; }

  .nd-tube-hit { fill: none; stroke: transparent; stroke-width: 18; }
  .nd-tube { fill: none; stroke: var(--nd-wg); stroke-width: 10; stroke-linecap: round; stroke-linejoin: round; opacity: 0.85; }
  .nd-tube-core { fill: none; stroke: var(--nd-wg-core); stroke-width: 5; stroke-linecap: round; stroke-linejoin: round; }
  .nd-tube-label { transition: opacity 180ms ease; }

  .nd-flow-hit { fill: none; stroke: transparent; stroke-width: 14; }
  .nd-flow { fill: none; stroke-width: 2.4; stroke-linejoin: round; stroke-linecap: round; stroke-dasharray: 5 4; }
  .ndc-flow-update { stroke: var(--nd-virtual); }
  .ndc-flow-status { stroke: var(--nd-egress); }
  .ndc-flow-refused { stroke: var(--nd-ingress); }
  .ndc-fill-update { fill: var(--nd-virtual); }
  .ndc-fill-status { fill: var(--nd-egress); }
  .ndc-fill-refused { fill: var(--nd-ingress); }
  .nd-packet { stroke: var(--card, #fff); stroke-width: 1; }
  @media (prefers-reduced-motion: reduce) { .nd-packet { display: none; } }

  .ndc-steps { pointer-events: none; }
  .nd-step { stroke: var(--card, #fff); stroke-width: 2; }
  .ndc-step-update { fill: var(--nd-virtual); }
  .ndc-step-status { fill: var(--nd-egress); }
  .ndc-step-refused { fill: var(--nd-ingress); }
  .nd-step-num { font-size: 10.5px; font-weight: 800; fill: #fff; }

  .nd-label { font-weight: 700; fill: var(--ink, #0f172a); }
  .nd-sub { font-size: 10.5px; fill: var(--sub, #475569); }
  .nd-small { font-size: 11px; }
  .nd-tiny { font-size: 9.5px; }
  .nd-mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  .nd-center { text-anchor: middle; }
  .nd-ink-underlay { fill: var(--nd-underlay); }
  .nd-ink-virtual { fill: var(--nd-virtual); }
</style>
