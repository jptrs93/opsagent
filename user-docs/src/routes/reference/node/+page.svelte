<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';

  const typeDefinition = `type Node root {
    id: u64
    status: enum NodeLifecycleStatus {
        EnrollmentRequested
        EnrollmentCancelled
        EnrollmentRequestExpired
        MemberNormal
        MemberUnhealthy
        MemberDraining
        MemberMissing
        MemberEvicted
    }
    enrollment_requested_at: ?i64
    operator: type NodeOperator {
        name: string
        roles: []enum NodeRole {
            Primary
            Secondary
        }
        enrolled_time: ?i64
    }
    reported: type NodeReported {
        identifier: string
        underlay_address: IPv4Address | IPv6Address
        wg_public_key: string
        host_addresses: [](IPv4Address | IPv6Address)
        host_addresses_unknown: bool
    }
}

type NodeStatus root {
    node_id: Node.id
    updated_at: i64
    is_connected: bool
    last_connected_at: ?i64
    remote_address: string
    opendeploy_version: string
    runtime_versions: string
}
`;
</script>

<svelte:head>
  <title>Node — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Node</h1>

<section id="content" class="page">
<p>Nodes are the machines that run deployments, and have the following type definition:</p>

<CodeBlock code={typeDefinition} language="typedef" title="Node" />

<p>A node record is split between what the operator sets and what the machine reports. The operator gives it a name, which can be changed at any time. Which spaces a node runs deployments for is not part of the record: the <a href="{base}/reference/node-access-policy/">node access policies</a> decide it. The roles record whether the node is the primary, which holds the cluster state and the Web UI, or a secondary. The reported half comes from the machine itself: a stable identifier it generates, the address it is reached on for the encrypted tunnel between nodes and its WireGuard key, and the public addresses it has found on its interfaces, which ingress routes can bind. A separate status record tracks whether the node is currently connected, from where, and which agent and runtime versions it runs.</p>

<p>A node enters the cluster through an enrolment request that an operator accepts, cancels, or lets expire; see <a href="{base}/guides/enrol-a-node/">enrol a node</a>. As a member it is normal, unhealthy, or missing according to its connection, and an operator can mark it draining and back. Eviction is permanent: the node is removed from the cluster, its containers and data are wiped, and its identifier stays listed so the same machine cannot rejoin by accident. Deployments placed on a node name it in their scheduling, so move or stop them before evicting it.</p>
</section>
