<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';

  const typeDefinition = `type Secret root {
    id: u64
    fs: type SecretFs {
        key: string
        directory_id: ?ValueDirectory.id
    }
    space_id: Space.id
    sealed: ?type SealedSecret {
        smk_version: u32
        ciphertext: bytes
        nonce: bytes
    }
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
  <title>Secret — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Secret</h1>

<section id="content" class="page">
<p>Secrets are encrypted values that deployments receive as environment variables, and have the following type definition:</p>

<CodeBlock code={typeDefinition} language="typedef" title="Secret" />

<p>A secret lives at a path in its space's folder tree: the key is a file name of up to 255 characters without slashes, unique among the secrets, config values, and folders beside it, and an absent directory means the root of the space. The content is sealed under the cluster's secrets master key before it is stored, is never sent to a browser unless a user with the reveal permission asks for it, and reaches only the nodes running a deployment that references it. The editor can generate a random value server-side. Every change appends a new version rather than editing in place, and a deployment reference pins one version, so a later change reaches a container only when the deployment is saved again.</p>

<p>Renaming or moving a secret keeps its id, so references keep working. A secret can always be moved to the global space, and to another space only if every deployment that references it already lives there. Deleting a referenced secret is refused. While the secrets store is locked, for example after a restore onto a new machine, deployments that reference secrets fail to start rather than start without them, until the store is unlocked with the recovery code. See <a href="{base}/guides/manage-secrets-and-config/">manage secrets and config</a> for the workflow.</p>
</section>
