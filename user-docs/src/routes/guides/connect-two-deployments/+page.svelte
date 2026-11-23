<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';

  const consumer = `deployment {
  name  = "web"
  space = space("prod")

  container {
    source {
      remote_image {
        image = "ghcr.io/acme/web:2.1.0"
      }
    }
    env_vars = {
      "API_HOST" = address("prod", "api")
      "API_PORT" = "8080"
    }
  }
}
`;

  const dnsForms = `api.space-2.internal      # every running instance of api in space 2
0.api.space-2.internal    # instance 0 only
`;
</script>

<svelte:head>
  <title>Connect two deployments — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Connect two deployments</h1>
<p class="lead">Let one deployment call another over the cluster's private network, without publishing anything to the outside.</p>

<!-- ============================================================ -->
<section id="reference" class="page">
<h2>Reference the address</h2>

<p>Every container in virtual mode, which is the default, has a private IPv6 address that is fixed for the life of the deployment: it is computed from the space, the deployment, and the instance number, and survives restarts, upgrades, and moves between nodes. Nothing needs to be published; a deployment is reachable on whatever ports its process listens on.</p>

<p>The encouraged way to hand one deployment the address of another is a reference in its configuration. An <code class="inl">api</code> deployment in the space <code class="inl">prod</code> listens on port 8080, and a <code class="inl">web</code> deployment in the same space is told where to find it.</p>

<CodeBlock code={consumer} language="hcl" />

<p>The web container starts with the api's address in the variable, and the application dials it. The value is a bare IPv6 address, so put it in square brackets when building a URL from it. The reference is recorded as a dependency: the api cannot be deleted, moved to another space, or switched to host networking while the web deployment holds it. The target must be in the same space as the deployment or in the global space.</p>

<p>The two can run on different nodes. Traffic between nodes crosses an encrypted tunnel and the address means the same thing on both sides. If the web container logs a connection refused, the api instance is not up yet or listens on another port. The address layout is described under <a href="{base}/design/networking/#addressing">Addressing</a> in the networking design.</p>
</section>

<!-- ============================================================ -->
<section id="dns" class="page">
<h2>When a name is needed</h2>

<p>A reference needs configuration to live in. For the cases it cannot cover, discovering the instances of a deployment at runtime, or reaching one from something that holds no reference, every container can resolve the cluster's internal names. Names have two forms.</p>

<CodeBlock code={dnsForms} language="text" />

<p>The number is the space's id: the global space is 1, and spaces you create are numbered from 2 in the order they were created. Underscores in a deployment name become dashes. The first form answers with the addresses of every running instance; the second names one. A name resolves on any node, as long as the caller is allowed to reach the target, and a target with nothing running resolves to an empty answer rather than an error, so a client that retries finds it once it is up. See <a href="{base}/design/networking/#dns">DNS</a> for how resolution follows the deployment's state.</p>
</section>

<!-- ============================================================ -->
<section id="spaces" class="page">
<h2>Across spaces</h2>

<p>A space is the unit of trust, and the defaults need no configuration.</p>

<table>
  <thead><tr><th>Traffic</th><th>Default</th></tr></thead>
  <tbody>
    <tr><td>Between deployments in the same space</td><td>allowed</td></tr>
    <tr><td>To or from a deployment in the global space</td><td>allowed</td></tr>
    <tr><td>Between deployments in different spaces</td><td>dropped</td></tr>
    <tr><td>Any deployment to the internet</td><td>allowed</td></tr>
  </tbody>
</table>

<p>To let a deployment in one space call a deployment in another, create a policy on the <strong>Policies</strong> page. A policy allows a source to connect to a destination, each either a whole space or a single deployment, optionally narrowed to ports such as <code class="inl">tcp/8080</code>. Creating it needs update access on the destination's space: the receiving side consents to being reachable. Replies ride the connection, so one policy covers the round trip.</p>

<p>Until the policy exists, packets from the source to the destination are dropped, so a cross-space call times out. An address reference cannot cross a space boundary either, so a cross-space caller uses the destination's internal name or a literal address. A policy naming a deleted space or deployment allows nothing until it is removed. How the boundary is enforced is described under <a href="{base}/design/networking/#policy">Network policy</a>.</p>
</section>
