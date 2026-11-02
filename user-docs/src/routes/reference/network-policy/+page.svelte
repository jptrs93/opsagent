<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';

  const typeDefinition = `type NetworkPolicy root {
    id: u64
    action: enum NetworkPolicyAction {
        Allow
    }
    source: type NetworkPolicyPeer {
        target: type SpacePeer {
            space_id: Space.id
        } | type DeploymentPeer {
            deployment_id: Deployment.id
        }
    }
    destination: NetworkPolicy.NetworkPolicyPeer
    ports: []type NetPortMatch {
        protocol: enum NetProtocol {
            Tcp
            Udp
        }
        range: type PortRange {
            start: u32
            end: u32
        }
    }
}
`;
</script>

<svelte:head>
  <title>Network policy — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Network policy</h1>

<section id="content" class="page">
<p>Network policies open traffic between spaces that the default rules would drop, and have the following type definition:</p>

<CodeBlock code={typeDefinition} language="typedef" title="NetworkPolicy" />

<p>The source and the destination are each a whole space or a single deployment. A deployment peer follows its deployment, so moving the deployment to another space moves the policy with it. An empty port list opens every port and protocol; otherwise each entry opens one protocol over an inclusive range whose start and end lie between 1 and 65535 with the start no greater than the end. The only action is allow. A policy whose source and destination resolve to the same space is rejected, since traffic within a space is already allowed.</p>

<p>Policies are cluster-wide records rather than part of any deployment. Creating one requires update access on the destination's space and view access on the source's space. A policy takes effect without restarting either side, and revoking it closes fresh connections while leaving the same-space defaults untouched. A policy whose space or deployment has since been deleted is flagged as dangling and opens nothing. The defaults that policies build on are described under <a href="{base}/design/networking/#policy">network policy</a> in the networking design.</p>
</section>
