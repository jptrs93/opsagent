# Standalone networking model

An interactive Svelte 5 + SVG illustration of OpenDeploy networking. This is an independent Vite project; it does not change the app or user docs.

```sh
cd networking-model
pnpm install
pnpm dev
```

## Portable artifact

```sh
pnpm check
pnpm build
open dist/networking-model.html
```

`dist/networking-model.html` is a self-contained offline file with compiled JavaScript and CSS embedded. It works directly from disk without a web server, fonts, CDN assets, or network requests. `dist/index.html` and `dist/assets/` are also available for conventional static hosting. Generated output is ignored by Git.

## Explore

- Switch between two and three nodes.
- Select a layer to emphasize the underlay, virtual IPv6, or public ingress.
- Hover, focus, or tap nodes and links to inspect them. SVG targets support Enter and Space; Escape clears the selected layer.
- Switch between an isometric cutaway and a plan view.
- Pause/resume packet animation. Reduced-motion preferences start the model paused.
- Step through the illustrated HTTPS request with the three story cards or walkthrough button.
- Export a static SVG of the current view, with styles embedded and moving packets removed.

## Reuse in the docs

The illustration is `src/NetworkDiagram.svelte`; its only local dependency is `src/model.js`. Its styling is scoped, and it needs no canvas or 3D runtime. Import those two files into a Svelte project, then render:

```svelte
<script>
  import NetworkDiagram from './NetworkDiagram.svelte';
</script>

<NetworkDiagram nodeCount={3} view="isometric" active="all" playing={false} />
```

Props: `nodeCount` (2 or 3), `view` (`isometric` or `flat`), `active` (`all`, `underlay`, `ipv6`, or `ingress`), `playing` (boolean), and `oninspect` (callback receiving a layer/node ID or `null`). Bind the component instance to call `exportSvg()`. The standalone app supplies the inspector, controls, and reduced-motion preference handling; an embedding host should also respect reduced motion when enabling playback.

## Architectural scope

Based on `user-docs/src/routes/networking/+page.svelte`: WireGuard is the cross-node underlay; one logical private IPv6 network spans all nodes; an ingress proxy can dial a remote backend using that private network. Violet shows transport, mint shows private traffic carried inside it, and amber shows the public connection up to the ingress proxy. The inner/outer paths represent encapsulation, not separate physical cables.

This example uses HTTPS ingress on node 01 and a backend on node 02. Workload roles, positions, packet timing, and the cluster boundary are illustrative. It does not model all ingress mechanisms, policy evaluation, routing tables, or live traffic. The isometric view is vector geometry rather than a rotatable 3D scene.
