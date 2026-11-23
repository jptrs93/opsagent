<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';

  const typeDefinition = `type Config root {
    id: u64
    fs: type ConfigFs {
        key: string
        directory_id: ?ValueDirectory.id
    }
    space_id: Space.id
    value: string
}

type ValueDirectory root {
    id: u64
    space_id: Space.id
    key: string
    parent_id: ?ValueDirectory.id
}
`;
</script>

<svelte:head>
  <title>Config value — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Config value</h1>

<section id="content" class="page">
<p>Config values are plain text values that deployments receive as environment variables, and have the following type definition:</p>

<CodeBlock code={typeDefinition} language="typedef" title="Config" />

<p>A config value shares the folder tree of its space with secrets: the key is a file name of up to 255 characters without slashes, unique among the values and folders beside it, and an absent directory means the root of the space. Unlike a secret, the value is stored and shown in the clear to anyone who can view it, which makes it the right place for settings that are not sensitive. Every change appends a new version rather than editing in place, and a deployment reference pins one version, so a later change reaches a container only when the deployment is saved again. Config values can also back <a href="{base}/reference/system-settings/">system settings</a>.</p>

<p>Renaming or moving a config value keeps its id, so references keep working. It can always be moved to the global space, and to another space only if every deployment that references it already lives there. Deleting a referenced config value is refused. See <a href="{base}/guides/manage-secrets-and-config/">manage secrets and config</a> for the workflow.</p>
</section>
