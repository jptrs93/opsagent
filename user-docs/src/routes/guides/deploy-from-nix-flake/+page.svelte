<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';

  const flake = `{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  inputs.nix2container.url = "github:nlewo/nix2container";
  inputs.nix2container.inputs.nixpkgs.follows = "nixpkgs";

  outputs = { nixpkgs, nix2container, ... }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems
        (system: f system (import nixpkgs { inherit system; }));
    in {
      packages = forAllSystems (system: pkgs:
        let
          n2c = nix2container.packages.\${system}.nix2container;
          app = pkgs.buildGoModule {
            pname = "api";
            version = "1.4.2";
            src = ./.;
            vendorHash = null;
          };
        in {
          image = n2c.buildImage {
            name = "acme/api";
            config.entrypoint = [ "\${app}/bin/api" ];
            maxLayers = 16;
          };
        });
    };
}
`;

  const deployment = `deployment {
  name  = "api"
  space = space("prod")

  container {
    source {
      nix_image_build {
        repo    = "github.com/acme/api"
        flake   = "flake.nix"
        target  = ".#image"
        version = "9f2c1d4e7b8a6c5d4e3f2a1b0c9d8e7f6a5b4c3d"
      }
    }
    env_vars = {
      "PORT" = "8080"
    }
  }

  scheduling {
    running = true
  }
}
`;
</script>

<svelte:head>
  <title>Deploy from a Nix flake — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Deploy from a Nix flake</h1>
<p class="lead">Build the container image on the node from a commit in your repository, with no registry in between. The default source is a published image; use this one when your project already builds with Nix.</p>

<!-- ============================================================ -->
<section id="flake" class="page">
<h2>What the flake must provide</h2>

<p>The flake output you name must be a <a href="https://github.com/nlewo/nix2container">nix2container</a> image: the build produces a description of the image's layers and config, which the node imports straight into its container runtime without building a tarball. Any flake that already does this works unchanged. A minimal one for a Go service looks like this.</p>

<CodeBlock code={flake} language="text" />

<p>List the systems your nodes run; the build happens on the node that will run the deployment, so an arm64 node needs an <code class="inl">aarch64-linux</code> output. The entrypoint, exposed port, and environment are whatever the image config says, exactly as with a published image.</p>
</section>

<!-- ============================================================ -->
<section id="deployment" class="page">
<h2>The deployment</h2>

<p>Where a deployment would normally name an image, name the repository, the flake file, the output, and the commit to build.</p>

<CodeBlock code={deployment} language="hcl" />

<p>The version is a full commit sha rather than a branch or tag, so what is running is never in doubt. You do not have to paste it: the editor's footer lets you pick a branch of the repository, and the version field then offers that branch's commits, newest first, with their messages. Before a save is accepted the primary fetches that exact commit and checks that it contains the flake file, so a typo fails at save time rather than at build time.</p>

<p>To ship a new commit, pick it in the version field and save. The node builds the new image and rolls the deployment over as it would for a new image tag. The fields are listed under <a href="{base}/reference/deployment/#source">nix_image_build</a> in the deployment reference.</p>
</section>

<!-- ============================================================ -->
<section id="private" class="page">
<h2>Private repositories</h2>

<p>A public GitHub repository needs nothing. For a private one, store a GitHub token with read access to the repository under <strong>Settings</strong>, <strong>Repository credentials</strong>. The primary uses it to list branches and commits and to verify the commit, and every node uses it to fetch the checkout it builds. The token is kept as a secret and is never written into the build.</p>
</section>

<!-- ============================================================ -->
<section id="build" class="page">
<h2>What happens on the node</h2>

<p>The first time a node is asked for a commit, it checks the repository out at that commit and runs the build in a short-lived build container with a Nix store that is kept per repository, so later builds reuse everything that did not change. A build can take minutes on a fresh store and seconds afterwards. The resulting image is cached on the node by repository, flake, output, and commit; a second instance of the same commit, or a rollback to it, starts without rebuilding.</p>

<p>While the build runs the deployment shows as preparing on the <strong>Deployments</strong> page, and <strong>Prepare output</strong> there opens the build log. One build runs at a time per repository on a node. Stores are garbage collected when they grow large and rebuilt from scratch after a week, so a build never depends on what an earlier one left behind. The build is described in more detail in the <a href="{base}/design/deployments/">deployments design</a>.</p>
</section>
