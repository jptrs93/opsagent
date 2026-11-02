<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';
  import { page } from '$app/state';
  import { replaceState } from '$app/navigation';
  import { onMount } from 'svelte';
  import {
    installHttp, installManaged, installManagedPrivate, installSelfManaged, installSelfSigned,
    installOutput, firstDeployment, checkIt, changeIt, uninstall,
  } from '$lib/getting-started-snippets';

  type Cert = 'managed' | 'managed-private' | 'self-managed' | 'self-signed';
  const certs: { id: Cert; label: string }[] = [
    { id: 'managed', label: 'OpenDeploy managed (most common)' },
    { id: 'managed-private', label: 'OpenDeploy managed, private address' },
    { id: 'self-managed', label: 'Self managed' },
    { id: 'self-signed', label: 'OpenDeploy self-signed' },
  ];
  let selected = $state<Cert>('managed');
  const activeIndex = $derived(certs.findIndex((cert) => cert.id === selected));

  function isCert(value: unknown): value is Cert {
    return certs.some((cert) => cert.id === value);
  }

  function select(cert: Cert) {
    selected = cert;
    try { localStorage.setItem('install-cert', cert); } catch {}
    const url = new URL(page.url);
    if (cert === 'managed') url.searchParams.delete('cert');
    else url.searchParams.set('cert', cert);
    try { replaceState(url, page.state); } catch {}
  }

  function navigate(event: KeyboardEvent, index: number) {
    let next: number;
    switch (event.key) {
      case 'ArrowRight': next = (index + 1) % certs.length; break;
      case 'ArrowLeft': next = (index - 1 + certs.length) % certs.length; break;
      case 'Home': next = 0; break;
      case 'End': next = certs.length - 1; break;
      default: return;
    }
    event.preventDefault();
    select(certs[next].id);
    const button = event.currentTarget as HTMLButtonElement;
    button.parentElement?.querySelectorAll<HTMLButtonElement>('[role="tab"]')[next]?.focus();
  }

  onMount(() => {
    const fromUrl = page.url.searchParams.get('cert');
    if (isCert(fromUrl)) { selected = fromUrl; return; }
    try {
      const stored = localStorage.getItem('install-cert');
      if (isCert(stored)) selected = stored;
    } catch {}
  });
</script>

<svelte:head>
  <title>Getting started — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Getting started</h1>
<p class="lead">Install OpenDeploy on one machine, sign in, and run your first container. About fifteen minutes once you have a hostname pointed at the machine, and less over plain HTTP.</p>

<!-- ============================================================ -->
<section id="install" class="page">
<h2>Install OpenDeploy</h2>

<h3 id="overview">Overview</h3>

<p>To install OpenDeploy, you install the OpenDeploy agent in primary mode on a chosen control plane machine. You can then enrol additional machines into your cluster as needed by independently installing the agent in secondary mode pointing at the primary machine, as described in <a href="{base}/guides/enrol-a-node/">Enrol a node</a>. Workloads can also run directly on the primary machine, so for initial testing you do not need to set up any secondary nodes at all.</p>

<p>Installation is done by the OpenDeploy installer through the wrapper shell script <code class="inl">scripts/install_primary.sh</code>. It downloads the release, checks its checksum, and runs the installer built into the binary. See <a href="#install-effects">What does the installation do?</a> to understand what the installer is actually doing.</p>

<p>In OpenDeploy, cluster configuration is application state: it is managed in the Web UI after installation and authored and versioned like any other resource. Install-time options therefore only set the initial value of the few settings that the first sign-in depends on. The one decision that matters is whether the Web UI is served over HTTP or HTTPS from the outset; an HTTP install can still be switched to HTTPS later under Settings. An example of each follows below.</p>

<h3 id="prerequisites">Prerequisites</h3>

<p>OpenDeploy runs on Linux with systemd on amd64 or arm64 and requires every machine to have outbound internet access. The installer needs sudo. These ports must be free on the primary machine, and reachable by whoever needs them:</p>

<table class="options">
  <colgroup><col class="c-opt" /><col class="c-def" /><col class="c-desc" /></colgroup>
  <thead><tr><th>Port</th><th>Used by</th><th>Must be reachable from</th></tr></thead>
  <tbody>
    <tr><td><code class="inl">443</code></td><td>Web UI over HTTPS</td><td>Your browser.</td></tr>
    <tr><td><code class="inl">80</code></td><td>Let's Encrypt validation</td><td>The internet, only with an OpenDeploy managed certificate.</td></tr>
    <tr><td><code class="inl">8080</code></td><td>Web UI over HTTP</td><td>Your browser, only with an HTTP-only install.</td></tr>
    <tr><td><code class="inl">9443</code>, <code class="inl">9444</code></td><td>Cluster and node enrolment</td><td>Every secondary node.</td></tr>
  </tbody>
</table>

<h3 id="install-http">Installing with HTTP only</h3>

<p>Install the primary with the Web UI served over plain HTTP:</p>

<CodeBlock code={installHttp} language="text" />

<p>The Web UI listens on port 8080 on every interface, so it is reached at <code class="inl">http://your-machine:8080</code>. See <a href="#install-options">further options</a> to change the port.</p>

<div class="callout warn">
  <div class="k">Not secure</div>
  <p>Plain HTTP leaves traffic and passwords unencrypted in transit. It is suitable for a demo, for an initial bootstrap where HTTPS is turned on afterwards under Settings and the master password rotated, or for a machine inside your own secured network. Browsers also offer passkeys over HTTP only on <code class="inl">localhost</code>.</p>
</div>

<h3 id="install-https">Installing with HTTPS (recommended)</h3>

<p>As a prerequisite you need a hostname whose DNS record points at the installation machine. You then decide how the TLS certificate is managed, which is generally one of:</p>

<ul>
  <li><strong>OpenDeploy managed</strong> (most common): certificates are obtained from Let's Encrypt and renewed automatically by OpenDeploy.</li>
  <li><strong>Self managed</strong>: you supply the certificate when you run the installer and replace it later under Settings.</li>
  <li><strong>OpenDeploy self-signed</strong>: OpenDeploy issues its own certificate from a local authority. Browsers do not trust it until you tell them to.</li>
</ul>

<p>Below are example commands for each scenario.</p>

<div class="install-tabs">
  <div class="tab-list" role="tablist" aria-label="Certificate management" aria-orientation="horizontal">
    {#each certs as cert, index}
      <button
        type="button"
        role="tab"
        id={`install-tab-${cert.id}`}
        aria-selected={index === activeIndex}
        aria-controls="install-panel"
        tabindex={index === activeIndex ? 0 : -1}
        onclick={() => select(cert.id)}
        onkeydown={(event) => navigate(event, index)}
      >{cert.label}</button>
    {/each}
  </div>

  <div id="install-panel" role="tabpanel" class:first-active={activeIndex === 0} aria-labelledby={`install-tab-${selected}`} tabindex="0">
    {#if selected === 'managed'}
      <p>This example assumes you have a domain name <code class="inl">opendeploy.example.com</code> that resolves to a public IP address such as <code class="inl">203.0.113.47</code>.</p>

      <CodeBlock code={installManaged} language="text" />

      <p>In this configuration a TLS certificate is obtained automatically from Let's Encrypt, which confirms the machine controls the hostname by connecting to it. Ports 80 and 443 must be reachable from the internet. The certificate is requested the first time a browser connects, so the first load takes a few seconds, and renewal is automatic from then on.</p>

      <p>The Web UI is reached at <code class="inl">https://opendeploy.example.com</code>. Worker nodes joining later connect on ports 9443 and 9444.</p>
    {:else if selected === 'managed-private'}
      <p>A public hostname can point at a private address, for example on a VPN, and still carry a certificate from Let's Encrypt. Let's Encrypt then confirms the hostname by looking up a DNS record rather than by connecting to the machine, so the machine never has to be reachable from the internet.</p>

      <p>OpenDeploy sets that record for you once it can talk to your DNS provider. <span class="pill p-plan">planned</span></p>

      <p>Until then, obtain the certificate with a DNS challenge using a tool such as certbot, and install with it as a self managed certificate. Renewal is yours: repeat the challenge and replace the certificate under Settings before it expires.</p>

      <CodeBlock code={installManagedPrivate} language="text" />
    {:else if selected === 'self-managed'}
      <p>If your organisation issues certificates, put the certificate chain and its private key in one PEM file and hand it to the installer. OpenDeploy serves it as-is.</p>

      <CodeBlock code={installSelfManaged} language="text" />

      <p>Renewal is yours: replace the certificate under Settings when a new one is issued. The hostname must resolve to the machine, but it does not have to be reachable from the internet.</p>
    {:else}
      <p>Without a certificate of your own, OpenDeploy creates a local certificate authority and issues the Web UI a certificate from it. The hostname only has to resolve to the machine, for example through an entry in your hosts file.</p>

      <CodeBlock code={installSelfSigned} language="text" />

      <p>Browsers warn about the certificate until they trust the authority. The installer prints the steps for each operating system and browser, and the sign-in page repeats them behind a <strong>Trust the CA</strong> button. Password sign-in works through the warning; passkeys need the authority trusted first.</p>
    {/if}
  </div>
</div>
<h3 id="install-options">Further install options</h3>

<p>Every option is passed to the install command after <code class="inl">bash -s --</code>, whichever way you install. Each one sets an initial value that can be changed later under <strong>Settings</strong>, so none of these choices is final.</p>

<table class="options">
  <colgroup><col class="c-opt" /><col class="c-def" /><col class="c-desc" /></colgroup>
  <thead><tr><th>Option</th><th>Default</th><th>What it sets</th></tr></thead>
  <tbody>
    <tr><td><code class="inl">--version vX.Y.Z</code></td><td><code class="inl">latest</code></td><td>The release to install.</td></tr>
    <tr><td><code class="inl">--http-only true</code></td><td><code class="inl">false</code></td><td>Serve the Web UI over plain HTTP instead of HTTPS.</td></tr>
    <tr><td><code class="inl">--web-listen host:port</code></td><td><code class="inl">:443</code> (<code class="inl">:8080</code> when <code class="inl">--http-only true</code>)</td><td>The Web UI listen address.</td></tr>
    <tr><td><code class="inl">--web-hosts opendeploy.example.com</code></td><td>required (<code class="inl">localhost</code> when <code class="inl">--http-only true</code>)</td><td>The hostname the Web UI is reached by. It names the certificate and the passkey relying party. Several can be given comma-separated when the UI answers to more than one name.</td></tr>
    <tr><td><code class="inl">--web-tls-cert-pem-file path</code></td><td>none</td><td>Serve your own certificate: a PEM file holding the certificate chain and private key. Without this or the next option the certificate comes from Let's Encrypt.</td></tr>
    <tr><td><code class="inl">--web-tls-self-signed true</code></td><td><code class="inl">false</code></td><td>Serve a certificate issued by a local authority that the installer generates. Cannot be combined with the option above.</td></tr>
    <tr><td><code class="inl">--require-passkey true</code></td><td><code class="inl">false</code></td><td>Turn password sign-in off so only passkeys open a session.</td></tr>
    <tr><td><code class="inl">--cluster-listen host:port</code></td><td><code class="inl">:9443</code></td><td>Where worker nodes connect once enrolled.</td></tr>
    <tr><td><code class="inl">--enrollment-listen host:port</code></td><td><code class="inl">:9444</code></td><td>Where new worker nodes enrol.</td></tr>
    <tr><td><code class="inl">--underlay-address addr</code></td><td>detected</td><td>The address other nodes reach this machine on for cross-node tunnels.</td></tr>
    <tr><td><code class="inl">--passkey-extra-origins a,b</code></td><td>none</td><td>Extra browser origins allowed for passkeys, such as a proxy with its own port.</td></tr>
    <tr><td><code class="inl">--restore-backup true</code></td><td><code class="inl">false</code></td><td>Restore the database from S3 before first boot, with <code class="inl">--restore-s3-bucket</code>, <code class="inl">--restore-s3-path</code>, <code class="inl">--restore-s3-region</code>, <code class="inl">--restore-s3-access-key-id</code>, <code class="inl">--restore-s3-secret-access-key</code>, an optional <code class="inl">--restore-s3-endpoint</code>, and the <code class="inl">--recovery-code</code> that unlocks it. See <a href="{base}/guides/back-up-and-restore/">Back up and restore</a>.</td></tr>
    <tr><td><code class="inl">--dry-run</code></td><td>off</td><td>Print what the installer would do without doing it.</td></tr>
  </tbody>
</table>
<h3 id="install-effects">What does the installation do?</h3>

<p>The installer makes these changes, in this order. Nothing is installed from the system package manager.</p>

<ul>
  <li><strong>A system user.</strong> Creates the <code class="inl">opendeploy</code> user and group. The agent runs as this user, with the Linux capabilities it needs for privileged ports, container mounts, and networking granted by its service unit.</li>
  <li><strong>Directories.</strong> Creates <code class="inl">/var/lib/opendeploy</code> for the agent's private data (database, secrets key, TLS material), plus sibling roots named <code class="inl">/var/lib/opendeploy-*</code> for assets, releases, volumes, build and run logs, the log archive, metrics, Nix stores, and container images.</li>
  <li><strong>The agent binary.</strong> Downloads the release into the releases root, verifies its SHA-256 checksum, and links it as <code class="inl">/var/lib/opendeploy/bin/opendeploy</code>. Later upgrades swap that link.</li>
  <li><strong>The primary state.</strong> Creates the database, the machine key that encrypts secrets, the cluster's IPv6 address prefix, the cluster certificate authority used for node-to-node TLS, and the master password, whose hash is stored and whose value is printed once.</li>
  <li><strong>Configuration files.</strong> Writes <code class="inl">/etc/opendeploy/env</code> for process-level settings and a sudoers drop-in at <code class="inl">/etc/sudoers.d/opendeploy</code> that lets the agent restart its own two services and nothing else.</li>
  <li><strong>A container runtime.</strong> Installs a pinned containerd and runc under the agent's own paths, with its own socket and image root, as <code class="inl">opendeploy-containerd.service</code>. Any Docker or system containerd already on the machine is left untouched.</li>
  <li><strong>The agent service.</strong> Installs, enables, and starts <code class="inl">opendeploy.service</code>, which starts after the runtime and restarts on failure.</li>
</ul>

<p>Once running, the agent manages network namespaces, virtual interfaces, routes, and nftables tables of its own for the containers it runs. <a href="#uninstall">Uninstalling</a> reverses all of the above and, with <code class="inl">--purge</code>, removes the data too.</p>

</section>

<!-- ============================================================ -->
<section id="sign-in" class="page">
<h2>Sign in</h2>

<p>Near the end of the installer's output you will see a master password and the Web UI address. The password is printed exactly once, so copy it now.</p>

<CodeBlock code={installOutput} language="text" />

<p>Open the Web UI address in your browser. Click <strong>Enrol or recover a user</strong>, paste the master password, and click <strong>Continue</strong>. Choose <strong>Enrol a new user</strong>, add the <strong>cluster_admin</strong> role under Privileges, enter your username, set a password now, and click <strong>Create and sign in</strong>. That creates your account and signs you in; it is a cluster administrator, so it can do everything.</p>

<p>The master password signs nobody in. It only opens this enrol-or-recover step, so keep it somewhere safe for when a user is locked out, or replace it under Settings.</p>

<div class="callout">
  <div class="k">Prefer a passkey?</div>
  <p>A passkey is the login your browser or password manager stores for you, unlocked with Touch ID, Windows Hello, or a security key. Once signed in, open <strong>Users &amp; roles</strong>, switch to the <strong>You</strong> tab, and click <strong>Add passkey</strong>. From then on you can sign in by approving a prompt, and Settings can require passkeys for everyone. Browsers offer passkeys on trusted HTTPS and on <code class="inl">localhost</code>.</p>
</div>
</section>

<!-- ============================================================ -->
<section id="look-around" class="page">
<h2>Look around</h2>

<p>Three things are already there.</p>

<ul>
  <li><strong>One node.</strong> The Nodes page lists the machine you installed on, named <code class="inl">primary</code>. You will refer to it by that name in a moment.</li>
  <li><strong>Two spaces.</strong> A space is a security boundary for deployments. <code class="inl">global</code> is the one for everything that should be reachable cluster-wide, and it is where your first deployment goes. <code class="inl">_system</code> belongs to OpenDeploy itself.</li>
  <li><strong>An empty Deployments page.</strong> A deployment is one container plus how it runs and where. Everything you configure lives on it.</li>
</ul>
</section>

<!-- ============================================================ -->
<section id="first-deployment" class="page">
<h2>Run your first deployment</h2>

<p>On the Deployments page click <strong>New deployment</strong>, then <strong>Code</strong> to switch the editor to its text form. Replace the contents with this and click <strong>Create</strong>.</p>

<CodeBlock code={firstDeployment} language="hcl" title="hello" copy />

<p>Reading it top to bottom: the deployment is called <code class="inl">hello</code> and lives in the global space. It runs the public nginx image. The node forwards its port 8081 to the container's port 80, so you can reach nginx from outside; that port must be free on the node and open in any firewall in front of it. It is set to run, on the node named primary.</p>

<p>The new row shows the deployment pulling the image, then starting, then running. The first pull takes a minute or two. Click the row to see its status and the version it is running.</p>

<p>Every field in that document is explained in the <a href="{base}/reference/deployment/">Deployment reference</a>.</p>
</section>

<!-- ============================================================ -->
<section id="check" class="page">
<h2>Check it works</h2>

<p>From your laptop, or from the machine itself, fetch the forwarded port. You should get nginx's welcome page back.</p>

<CodeBlock code={checkIt} language="text" />

<p>Back in the Web UI, the <strong>Logs</strong> page shows the request arriving in nginx's access log a moment later. Logs from every container are collected here without any configuration.</p>
</section>

<!-- ============================================================ -->
<section id="change" class="page">
<h2>Change it</h2>

<p>Open the deployment, switch to <strong>Code</strong> again, and change the image tag.</p>

<CodeBlock code={changeIt} language="hcl" />

<p>Click <strong>Update deployment</strong>. OpenDeploy pulls the new image, stops the old container, and starts the new one; the row reports each step. Every save is a new version of the deployment, and the history is kept, so you can see what changed and when. Zero-downtime replacement is a one-line change to the upgrade strategy, described under <a href="{base}/reference/deployment/#upgrade">container.upgrade</a>.</p>
</section>

<!-- ============================================================ -->
<section id="uninstall" class="page">
<h2>Uninstall</h2>

<p>The uninstall script stops the services, removes every container, the virtual network state, the units, the sudoers drop-in, and the binary. Without <code class="inl">--purge</code> it leaves the data directories and configuration in place so a reinstall picks up where it left off.</p>

<CodeBlock code={uninstall} language="text" />
</section>

<!-- ============================================================ -->
<section id="next" class="page">
<h2>Where next</h2>

<ul>
  <li><a href="{base}/guides/enrol-a-node/">Enrol a node</a> to add a second machine to the cluster.</li>
  <li><a href="{base}/guides/expose-a-service/">Expose a service</a> on a real hostname with an automatic certificate.</li>
  <li><a href="{base}/guides/deploy-from-nix-flake/">Deploy from a Nix flake</a> to build images from your own repository.</li>
  <li><a href="{base}/design/networking/">Networking design</a> for how deployments reach each other and the outside world.</li>
</ul>

</section>

<style>
  .install-tabs { min-width: 0; max-width: 100%; margin: 4px 0 0; }
  .tab-list { display: flex; gap: 4px; overflow-x: auto; position: relative; z-index: 1; }
  .tab-list button {
    flex-shrink: 0;
    padding: 9px 14px 10px;
    border: 1px solid transparent;
    border-bottom: 0;
    border-radius: 6px 6px 0 0;
    background: transparent;
    color: var(--sub);
    font: 600 14px/1.4 -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
    cursor: pointer;
  }
  .tab-list button:hover { color: var(--accent); }
  .tab-list button[aria-selected='false']:hover { background: var(--accent-soft); }
  .tab-list button[aria-selected='true'] { color: var(--accent); border-color: var(--line); background: var(--card); }
  .tab-list button:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
  [role='tabpanel'] {
    min-width: 0;
    margin-top: -1px;
    padding: 6px 16px 10px;
    border: 1px solid var(--line);
    border-radius: 6px;
    background: var(--card);
  }
  [role='tabpanel'].first-active { border-top-left-radius: 0; }
  [role='tabpanel']:focus { outline: none; }
  table.options { display: table; table-layout: fixed; width: 100%; }
  table.options .c-opt { width: 30%; }
  table.options .c-def { width: 18%; }
  table.options .c-desc { width: 52%; }
  table.options th { white-space: normal; }
  @media (max-width: 900px) {
    table.options { display: block; table-layout: auto; }
    table.options td:first-child, table.options td:nth-child(2) { white-space: nowrap; }
    table.options td:last-child { min-width: 260px; }
  }
</style>
