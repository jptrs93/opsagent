<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';

  const useValues = `container {
  source {
    remote_image {
      image = "ghcr.io/acme/api:1.4.2"
    }
  }
  env_vars = {
    "DATABASE_PASSWORD" = secret("prod", "db/password")
    "LOG_LEVEL"         = config("prod", "api/log-level")
    "GOOGLE_CREDENTIALS" = asset("prod", "google/service-account.json")
  }
}
`;

  const pinned = `"DATABASE_PASSWORD" = secret("prod", "db/password", 3)
`;
</script>

<svelte:head>
  <title>Manage secrets and config — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Manage secrets and config</h1>
<p class="lead">Keep passwords, tokens, and settings out of deployment configs. A deployment names a value by its path, and the cluster hands the right version to the container.</p>

<!-- ============================================================ -->
<section id="create" class="page">
<h2>Create a value</h2>

<p>Open <strong>Secrets / Configs</strong>. Every space has its own tree of folders, and a value lives at a path inside it such as <code class="inl">db/password</code>. Pick the space and folder, then click <strong>New secret</strong> or <strong>New config</strong>, give it a name and a value, and save.</p>

<p>The two kinds differ in how they are handled, not in how they are used. A <strong>secret</strong> is encrypted before it is stored, is never sent to a browser unless you click <strong>Reveal</strong>, and reaches only the nodes that run a deployment referencing it. The editor can generate a random one for you. A <strong>config</strong> is a plain value, shown in the table, for anything that is not sensitive: a log level, a feature flag, a URL.</p>

<p>Files such as certificates, service account keys, or data a container reads at startup go on the <strong>Assets</strong> page instead. Assets have the same folders, versions, and references as values, but are mounted into the container rather than passed as text.</p>
</section>

<!-- ============================================================ -->
<section id="reference" class="page">
<h2>Use it in a deployment</h2>

<p>Reference a value from the deployment's environment variables. The container sees ordinary variables and needs no OpenDeploy awareness.</p>

<CodeBlock code={useValues} language="hcl" />

<p>A secret or config reference puts the content in the variable. An asset reference mounts the file read-only and puts its path in the variable; an asset can also be mounted at a path of your choosing from the container's mounts, as the <a href="{base}/reference/deployment/#mounts">deployment reference</a> describes. A deployment may reference values from its own space or from the global space, and nothing else.</p>

<p>Saving the deployment pins each reference to the version that is newest at that moment, so a running deployment is reproducible: a change to the value later does not reach the container on its own. The editor hides the number while the pinned version is still the newest and shows it once it is not.</p>

<CodeBlock code={pinned} language="hcl" />

<p>Adding a value is not enough to start a deployment that references a secret while the <a href="#locked">secrets store is locked</a>; such a deployment fails to start rather than starting without it.</p>
</section>

<!-- ============================================================ -->
<section id="rotate" class="page">
<h2>Change a value</h2>

<p>A value is never changed in place. Select it, click <strong>Edit</strong>, and save the new content, which becomes the next version; the old versions stay listed in the details pane and anything pinned to them keeps working. If deployments reference the value, the editor offers <strong>Update referencing deployments</strong>. Leave it on and every one of them is saved again pinned to the new version, which rolls them over like any other change. Leave it off to adopt the new version deployment by deployment, by saving each when you are ready.</p>

<p>Renaming or moving a value to another folder changes nothing for the deployments that use it: a reference is stored by identity, and the path in the editor updates to match. A value can always be moved to the global space. Moving it to any other space is refused unless every deployment that references it already lives there.</p>
</section>

<!-- ============================================================ -->
<section id="locked" class="page">
<h2>If the store is locked</h2>

<p>Secrets are sealed with a key that lives on the primary machine. When that key is missing, after a <a href="{base}/guides/back-up-and-restore/">restore onto a new machine</a>, the Secrets / Configs page shows <strong>Secrets store is locked</strong>. Configs stay readable and the cluster keeps running, but secrets cannot be listed, revealed, or written, and a deployment that references one does not start. Enter the recovery code in the banner and click <strong>Unlock</strong>; the primary re-establishes its local key and later restarts need nothing.</p>

<p>The recovery code is generated under <strong>Settings</strong>, <strong>Secrets recovery</strong>, and is shown once. Generate it right after installing and keep it with your other credentials: without it a locked store cannot be opened again.</p>
</section>
