<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';

  const typeDefinition = `type Asset root {
    id: u64
    fs: type AssetFs {
        key: string
        directory_id: ?AssetDirectory.id
    }
    space_id: Space.id
    size_bytes: u64
    sha256: [32]u8
    storage_key: string
}

type AssetDirectory root {
    id: u64
    space_id: Space.id
    key: string
    parent_id: ?AssetDirectory.id
}
`;
</script>

<svelte:head>
  <title>Asset — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Asset</h1>

<section id="content" class="page">
<p>Assets are versioned files that are mounted into containers, and have the following type definition:</p>

<CodeBlock code={typeDefinition} language="typedef" title="Asset" />

<p>An asset lives at a path in its space's asset folder tree, separate from the tree that holds secrets and config values: the key is a file name of up to 255 characters without slashes, unique among the assets and folders beside it. Each upload appends an immutable version recorded by its size and SHA-256 hash, and content is stored once per hash, so identical uploads share one copy. The storage key names that copy, which lives on the primary or in S3 according to the backup and large asset <a href="{base}/reference/system-settings/">settings</a>. A deployment references an asset by id and version, either as an environment variable holding the path of a read-only mount or as a mount at a path of its choosing. A reference pins one version, so a new upload reaches a container only when the deployment is saved again.</p>

<p>Renaming or moving an asset keeps its id and its version history, so references keep working. It can always be moved to the global space, and to another space only if every deployment that references it already lives there; folders cannot change space. Deleting a referenced asset is refused, and a folder can only be deleted when it is empty. The <a href="{base}/reference/deployment/#mounts">deployment reference</a> describes how assets are mounted.</p>
</section>
