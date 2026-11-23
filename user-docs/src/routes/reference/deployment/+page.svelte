<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { deploymentType, fullExample, minimalExample, listenExamples } from '$lib/deployment-reference-snippets';
  import { hostMode } from '$lib/networking-snippets';
  import { base } from '$app/paths';

  const functionSignatures = `// References, resolved by display name when the deployment is saved
space(name: string) -> Space.id
node(name: string) -> Node.id
deployment(space: string, name: string) -> Deployment.id
secret(space: string, path: string, version: ?u32) -> SecretRef
config(space: string, path: string, version: ?u32) -> ConfigRef
asset(space: string, path: string, version: ?u32) -> AssetRef
address(space: string, deployment: string) -> AddressEnv

// Mount sources
default_volume() -> MountSource
host_path(path: string) -> MountSource
issued_tls(options: ?IssuedTlsOptions) -> MountSource
mount(source: MountSource, path: string, options: ?MountOptions) -> Mount

// Ingress
acme() -> AcmeCertSource
scheduled_node() -> ListenNode
any_node() -> ListenNode
any_address() -> ListenAddress
ipv4() -> ListenAddress
ipv6() -> ListenAddress
`;
</script>

<svelte:head>
  <title>Deployment — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Deployment</h1>
<p class="lead">Deployments are how you define your workloads. Almost everything about a workload is defined on this one resource: its scheduling, environment, networking, ingress, and so on.</p>

<!-- ============================================================ -->
<section id="type" class="page">
<h2>Type definition</h2>

<CodeBlock code={deploymentType} language="typedef" title="Deployment" />
</section>

<!-- ============================================================ -->
<section id="code" class="page">
<h2>Configuration code</h2>

<p>Every block and field, with the defaults in comments. Only the identity, the source, and the scheduling block are required; the minimal form follows, and the rest of this page takes each block in turn.</p>

<CodeBlock code={fullExample} language="hcl" title="Every block" />

<CodeBlock code={minimalExample} language="hcl" title="Minimal deployment" />
</section>

<!-- ============================================================ -->
<section id="identity" class="page">
<h2>Identity</h2>

<p>The two attributes directly inside the deployment block name it and place it in a space. Together with the node it is scheduled on they form the identity that addresses, DNS names, and references are derived from.</p>

<table>
  <thead><tr><th>Field</th><th>Type</th><th>Default</th><th>Meaning</th></tr></thead>
  <tbody>
    <tr><td class="mono">name</td><td>string</td><td>required</td><td>Unique among deployments on the same node in the same space. Underscores become dashes in DNS names. Immutable after creation.</td></tr>
    <tr><td class="mono">space</td><td><code class="inl">space("name")</code></td><td>required</td><td>The space the deployment belongs to. Changing it is a space move: a rollover into the new space that requires create permission there and any host permissions in both spaces.</td></tr>
  </tbody>
</table>
</section>

<!-- ============================================================ -->
<section id="source" class="page">
<h2>container.source</h2>

<p>The source block holds exactly one of two variants. Both produce a container image; the version is part of the variant, not a separate attribute.</p>

<h3>remote_image</h3>

<table>
  <thead><tr><th>Field</th><th>Type</th><th>Default</th><th>Meaning</th></tr></thead>
  <tbody>
    <tr><td class="mono">image</td><td>string</td><td>required</td><td>A full image reference, <code class="inl">repository:tag</code> or <code class="inl">repository@sha256:…</code>. The tag or digest is the version and is required while the deployment is running.</td></tr>
  </tbody>
</table>

<h3>nix_image_build</h3>

<p>The agent clones the repository at the commit, runs the flake build in an isolated build container, and ingests the resulting image.</p>

<table>
  <thead><tr><th>Field</th><th>Type</th><th>Default</th><th>Meaning</th></tr></thead>
  <tbody>
    <tr><td class="mono">repo</td><td>string</td><td>required</td><td>The git repository, for example <code class="inl">github.com/acme/payments</code>. Private repositories use the node's configured GitHub token.</td></tr>
    <tr><td class="mono">flake</td><td>string</td><td>required</td><td>Path to a <code class="inl">flake.nix</code> relative to the repository root, staying inside it.</td></tr>
    <tr><td class="mono">target</td><td>string</td><td>the flake's default output</td><td>A local flake selector beginning with <code class="inl">.#</code>, such as <code class="inl">.#image</code>.</td></tr>
    <tr><td class="mono">version</td><td>string</td><td>required while running</td><td>A full 40-character commit sha. The primary verifies that the commit exists and contains the flake before accepting a running deployment.</td></tr>
  </tbody>
</table>
</section>

<!-- ============================================================ -->
<section id="process" class="page">
<h2>container.process</h2>

<p>Overrides for how the image's process starts. The whole block is optional, and every field defaults to what the image declares.</p>

<table>
  <thead><tr><th>Field</th><th>Type</th><th>Default</th><th>Meaning</th></tr></thead>
  <tbody>
    <tr><td class="mono">user</td><td>string</td><td>the image's user</td><td>The uid or user name the process runs as. Mounted files are owned by this user where OpenDeploy writes them.</td></tr>
    <tr><td class="mono">command</td><td>list of strings</td><td>the image's entrypoint</td><td>Replaces the entrypoint and arguments. Empty strings are dropped.</td></tr>
    <tr><td class="mono">working_dir</td><td>string</td><td>the image's working directory</td><td>Replaces the working directory.</td></tr>
  </tbody>
</table>
</section>

<!-- ============================================================ -->
<section id="env-vars" class="page">
<h2>container.env_vars</h2>

<p>An object of variable name to value. A value is either a quoted string or one of four reference functions. References are resolved on the node when the container starts, so a secret never leaves the cluster's encrypted store until then.</p>

<table>
  <thead><tr><th>Value</th><th>Resolves to</th><th>Notes</th></tr></thead>
  <tbody>
    <tr><td class="mono">"text"</td><td>The literal text.</td><td></td></tr>
    <tr><td class="mono">secret("space", "folder/name"[, version])</td><td>The secret's content.</td><td>The space must be the deployment's own or the global space. Without a version the latest at save time is pinned.</td></tr>
    <tr><td class="mono">config("space", "folder/name"[, version])</td><td>The config value's content.</td><td>Same locality and pinning rules as secrets.</td></tr>
    <tr><td class="mono">asset("space", "folder/key"[, version])</td><td>The path of the asset inside the container.</td><td>The asset is mounted read-only under <code class="inl">/opendeploy-env-assets</code> and the variable holds its path.</td></tr>
    <tr><td class="mono">address("space", "deployment")</td><td>The target's inbound IPv6 address.</td><td>The target must be in virtual mode and in the deployment's own or the global space, and cannot be the deployment itself. The reference is recorded as a dependency: the target cannot be deleted, moved to another space, or switched to host mode while it exists.</td></tr>
  </tbody>
</table>

<p>A reference without a version is resolved when the config is saved and stored pinned to the version that was newest then, so a saved config is always reproducible: a later version of a secret does not reach the container until the config is saved again. The editor hides the number while the pinned version is still the newest and shows it, as <code class="inl">secret("prod", "db/password", 3)</code>, once a newer one exists. The variable <code class="inl">OPENDEPLOY_READINESS_SOCK_PATH</code> is reserved when the upgrade strategy is rollover.</p>
</section>

<!-- ============================================================ -->
<section id="mounts" class="page">
<h2>container.mounts</h2>

<p>A list of <code class="inl">mount(source, "/container/path"[, options])</code> calls. Every container path must be a clean absolute path other than root. The source decides what is mounted and which options apply.</p>

<table>
  <thead><tr><th>Source</th><th>Mounts</th><th>Options</th><th>Notes</th></tr></thead>
  <tbody>
    <tr><td class="mono">default_volume()</td><td>The deployment's persistent volume, created on the node the first time it runs.</td><td>none</td><td>At most one, and the only place the volume's path is set. Omitting it disables the volume. The conventional path is <code class="inl">/data</code>. Writable.</td></tr>
    <tr><td class="mono">asset("space", "folder/key"[, version])</td><td>One versioned file.</td><td><code class="inl">executable = true</code></td><td>Read-only, or read-and-execute with the option. Same locality and pinning rules as asset env vars.</td></tr>
    <tr><td class="mono">deployment("space", "deployment")</td><td>Another deployment's default volume.</td><td><code class="inl">read_only = true</code></td><td>The other deployment must run on the same node and have its default volume enabled. Writable unless the option is set.</td></tr>
    <tr><td class="mono">host_path("/host/path")</td><td>A directory or file on the node.</td><td><code class="inl">read_only = true</code></td><td>Requires the <code class="inl">use_host_mounts</code> permission to create or update the deployment. Protected system directories, their parents, and their children are rejected. Writable unless the option is set.</td></tr>
    <tr><td class="mono">issued_tls([options])</td><td>A directory holding a certificate issued by the cluster's internal CA for this deployment.</td><td>inside the call: <code class="inl">extra_names = [...]</code>, <code class="inl">ca_only = true</code></td><td>At most one. Writes <code class="inl">ca.crt</code>, <code class="inl">public.crt</code>, and <code class="inl">private.key</code>, or only <code class="inl">ca.crt</code> with <code class="inl">ca_only</code>. Up to 16 extra names within the deployment's own identity. Renewed in place.</td></tr>
  </tbody>
</table>
</section>

<!-- ============================================================ -->
<section id="resources" class="page">
<h2>container.resources</h2>

<table>
  <thead><tr><th>Field</th><th>Type</th><th>Default</th><th>Meaning</th></tr></thead>
  <tbody>
    <tr><td class="mono">dev_shm_size_kb</td><td>integer ≥ 1</td><td>the container runtime's default</td><td>Size of the container's <code class="inl">/dev/shm</code> in KiB.</td></tr>
    <tr><td class="mono">file_descriptor_limit</td><td>integer ≥ 1</td><td>2048</td><td>The open-file limit applied to the process.</td></tr>
  </tbody>
</table>
</section>

<!-- ============================================================ -->
<section id="upgrade" class="page">
<h2>container.upgrade</h2>

<p>How a new version replaces the running one. The block is optional; without it the strategy is recreate.</p>

<table>
  <thead><tr><th>Field</th><th>Type</th><th>Default</th><th>Meaning</th></tr></thead>
  <tbody>
    <tr><td class="mono">strategy</td><td><code class="inl">"recreate"</code> or <code class="inl">"rollover"</code></td><td><code class="inl">"recreate"</code></td><td>Recreate stops the old container, then starts the new one. Rollover starts the new container beside the old one, waits for it to signal readiness, then switches inbound traffic and stops the old one.</td></tr>
    <tr><td class="mono">readiness_timeout_seconds</td><td>integer ≥ 1</td><td>600</td><td>Rollover only. How long the candidate may take to signal readiness across all of its start attempts. On expiry the candidate is stopped and the old container keeps serving.</td></tr>
  </tbody>
</table>

<p>A rollover candidate finds a Unix socket path in <code class="inl">OPENDEPLOY_READINESS_SOCK_PATH</code> and signals readiness by writing <code class="inl">ready</code> followed by a newline to it once it can serve. In virtual mode the switch is a route change on the node and no port contention exists. In host mode the candidate shares the host's ports, so it must defer binding them until after it has signalled. See <a href="{base}/design/networking/#rollover">Rollover</a> for what happens on the network.</p>
</section>

<!-- ============================================================ -->
<section id="network" class="page">
<h2>network</h2>

<p>The block is optional. Virtual mode is the default, so it exists to opt out with host mode or to declare ingress.</p>

<table>
  <thead><tr><th>Field</th><th>Type</th><th>Default</th><th>Meaning</th></tr></thead>
  <tbody>
    <tr><td class="mono">mode</td><td><code class="inl">"virtual"</code> or <code class="inl">"host"</code></td><td><code class="inl">"virtual"</code></td><td>Virtual gives the container its own network namespace and cluster addresses. Host runs it in the node's network namespace with no addresses of its own, no ingress, and no policy. Creating or updating a host-mode deployment requires the <code class="inl">use_host_network</code> permission.</td></tr>
    <tr><td class="mono">ingress</td><td>block</td><td>none</td><td>Published routes, virtual mode only. See below.</td></tr>
  </tbody>
</table>

<CodeBlock code={hostMode} language="hcl" title="Host network mode" />
</section>

<!-- ============================================================ -->
<section id="ingress" class="page">
<h2>network.ingress</h2>

<p>The ingress block holds any number of route blocks of three kinds. Each kind is selected by what the traffic is. All of them are served by the node's built-in proxy or programmed as kernel rules by the agent; see <a href="{base}/design/networking/#ingress">Ingress</a> for how they are routed.</p>

<h3>https</h3>

<p>The proxy terminates TLS on port 443, routes by hostname and path prefix, and redirects plain HTTP on port 80 to HTTPS. Many routes share the port.</p>

<table>
  <thead><tr><th>Field</th><th>Type</th><th>Default</th><th>Meaning</th></tr></thead>
  <tbody>
    <tr><td class="mono">hostname</td><td>string</td><td>required</td><td>A DNS host name. One route per hostname and path prefix.</td></tr>
    <tr><td class="mono">container_port</td><td>1 to 65535</td><td>required</td><td>The port the container listens on.</td></tr>
    <tr><td class="mono">path_prefix</td><td>string</td><td><code class="inl">"/"</code></td><td>A clean absolute path. The longest matching prefix wins among routes for the same hostname.</td></tr>
    <tr><td class="mono">strip_prefix</td><td>boolean</td><td>false</td><td>Remove the prefix from the path before forwarding.</td></tr>
    <tr><td class="mono">backend</td><td><code class="inl">"http1"</code> or <code class="inl">"h2c"</code></td><td><code class="inl">"http1"</code></td><td>The protocol spoken to the container.</td></tr>
    <tr><td class="mono">max_request_body_bytes</td><td>integer ≥ 1</td><td>unlimited</td><td>Requests with a larger body are rejected.</td></tr>
    <tr><td class="mono">flush_interval_ms</td><td>1 to 60000</td><td>the proxy's default</td><td>How often buffered response bytes are flushed to the client. Set it low for streaming responses.</td></tr>
    <tr><td class="mono">cert</td><td><code class="inl">acme()</code> or <code class="inl">secret("space", "folder/name"[, version])</code></td><td><code class="inl">acme()</code></td><td>Where the certificate comes from. ACME issues and renews one through the HTTP-01 challenge. A secret must hold a PEM certificate and key valid for the hostname.</td></tr>
    <tr><td class="mono">listen</td><td>block, repeatable</td><td>the scheduled node, every address</td><td>Where the route is published. See listen below.</td></tr>
  </tbody>
</table>

<h3>tls_passthrough</h3>

<p>The proxy reads the hostname from the TLS handshake and relays the raw stream to the container without decrypting it. The container owns its own certificate.</p>

<table>
  <thead><tr><th>Field</th><th>Type</th><th>Default</th><th>Meaning</th></tr></thead>
  <tbody>
    <tr><td class="mono">hostname</td><td>string</td><td>required</td><td>The SNI name to match. One route per hostname and host port.</td></tr>
    <tr><td class="mono">container_port</td><td>1 to 65535</td><td>required</td><td>The port the container listens on.</td></tr>
    <tr><td class="mono">host_port</td><td>1 to 65535</td><td>443</td><td>The port the proxy listens on. Ports 53 and 80 are reserved, and any other port below 1024 except 443 requires the <code class="inl">use_privileged_ports</code> permission. A hostname cannot have both an https and a passthrough route on port 443.</td></tr>
    <tr><td class="mono">listen</td><td>block, repeatable</td><td>the scheduled node, every address</td><td>See listen below.</td></tr>
  </tbody>
</table>

<h3>port_forward</h3>

<p>A kernel DNAT rule from a host port to the container, with no proxy in the path. For protocols that are not TLS.</p>

<table>
  <thead><tr><th>Field</th><th>Type</th><th>Default</th><th>Meaning</th></tr></thead>
  <tbody>
    <tr><td class="mono">protocol</td><td><code class="inl">"tcp"</code> or <code class="inl">"udp"</code></td><td>required</td><td></td></tr>
    <tr><td class="mono">container_port</td><td>1 to 65535</td><td>required</td><td>The port the container listens on.</td></tr>
    <tr><td class="mono">host_port</td><td>1 to 65535</td><td>container_port</td><td>The port opened on the node. One forward per node, protocol and host port: a port another deployment forwards, or one the platform listens on, is refused at save. A port below 1024 requires the <code class="inl">use_privileged_ports</code> permission. The forward answers on the node's addresses, not on <code class="inl">localhost</code>.</td></tr>
    <tr><td class="mono">allow</td><td>list of addresses or prefixes</td><td>everyone</td><td>Only these sources may connect. Others see the port as closed. Tightening the list does not cut established connections.</td></tr>
    <tr><td class="mono">deny</td><td>list of addresses or prefixes</td><td>none</td><td>Reserved. Defined to win over allow, but not enforced yet, so a config that sets it is rejected at save.</td></tr>
  </tbody>
</table>

<h3>listen</h3>

<p>An https or tls_passthrough route may carry listen blocks choosing the nodes and addresses it is published on. Each block is a node selector crossed with an address selector. No block means the node the deployment is scheduled on, on every address it has.</p>

<CodeBlock code={listenExamples} language="hcl" title="Listen selectors" />

<table>
  <thead><tr><th>Field</th><th>Values</th><th>Default</th><th>Meaning</th></tr></thead>
  <tbody>
    <tr><td class="mono">node</td><td><code class="inl">scheduled_node()</code>, <code class="inl">any_node()</code>, <code class="inl">node("name")</code></td><td><code class="inl">scheduled_node()</code></td><td>Which nodes publish the route. <code class="inl">any_node()</code> publishes on every node that can reach the backend over the virtual private network. A named node must currently be the scheduled node; naming another node is rejected at save until the proxy can dial backends across nodes.</td></tr>
    <tr><td class="mono">address</td><td><code class="inl">any_address()</code>, <code class="inl">ipv4()</code>, <code class="inl">ipv6()</code>, an address or prefix, or a list of them</td><td><code class="inl">any_address()</code></td><td>Which of the selected nodes' public addresses the route binds. A literal that no selected node has publishes nothing there.</td></tr>
  </tbody>
</table>

<p>The Web UI's own listener is a reserved claim in the same evaluation. A wildcard selector that would land on it is quietly skipped there; a literal selector that names it exactly is rejected at save. Two deployments whose routes expand to the same node, address, port, and hostname collide, and the second save is rejected.</p>
</section>

<!-- ============================================================ -->
<section id="scheduling" class="page">
<h2>scheduling</h2>

<p>Whether the deployment runs and where. The block is required.</p>

<table>
  <thead><tr><th>Field</th><th>Type</th><th>Default</th><th>Meaning</th></tr></thead>
  <tbody>
    <tr><td class="mono">running</td><td>boolean</td><td>required</td><td>False keeps the configuration and stops the container. A version may be empty only while this is false.</td></tr>
    <tr><td class="mono">dedicated_nodes.nodes</td><td>list of <code class="inl">node("name")</code></td><td>required</td><td>The node the deployment runs on. Exactly one entry today. Immutable after creation: moving a deployment is a new deployment. The node must allow the deployment's space and must not be draining.</td></tr>
  </tbody>
</table>
</section>

<!-- ============================================================ -->
<section id="functions" class="page">
<h2>Functions</h2>

<p>Functions are how the configuration code refers to other resources and names a few values that have no literal form. Each is described in the section where it is used; this is the complete list.</p>

<CodeBlock code={functionSignatures} language="typedef" title="Functions" />

<p>A reference function finds the named resource when the deployment is saved and the stored deployment holds its id, so renaming the resource does not change what the deployment points at, and the code shows the current name the next time it is opened. The versioned functions pin the newest version at save time unless one is given, and the editor shows the number only once a newer version exists. How functions are evaluated is explained under <a href="{base}/design/configuration-code/">configuration code</a>.</p>
</section>

<!-- ============================================================ -->
<section id="related" class="page">
<h2>Related design</h2>

<ul>
  <li><a href="{base}/design/networking/#addressing">Addressing</a>: how a deployment's addresses are derived from its space, id, and ordinal, and what an <code class="inl">address()</code> reference resolves to.</li>
  <li><a href="{base}/design/networking/#rollover">Rollover</a>: what happens on the network when a candidate is promoted.</li>
  <li><a href="{base}/design/networking/#ingress">Ingress</a> and <a href="{base}/design/networking/#policy">Network policy</a>: how routes are published and which traffic is allowed.</li>
  <li><a href="{base}/design/deployments/">Deployments</a>: lifecycle, versions, and crash recovery.</li>
</ul>
</section>
