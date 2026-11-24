<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';

  const typeDefinition = `type NodeAccessPolicy root {
    id: u64
    name: string
    disabled: bool
    builtin: enum NodeAccessBuiltin {
        None
        System
        Default
    }
    nodes: type NodeSelector {
        exact_nodes: ?[]Node.id
        all_nodes_excluding: ?[]Node.id
    }
    rule: type NodeAccessRule {
        effect: type NodeAccessAllow {
        } | type NodeAccessDeny {
        }
        selector: type NodeAccessSelector {
            permissions: type NodePermissionSelector {
                exact_permissions: ?[]enum NodePermission {
                    RunDeployments
                }
                all_permissions_excluding: ?[]NodePermission
            }
            spaces: AuthzSpaceSelector
        }
    }
}
`;

  const example = `{"name": "edge_runs_edge_only",
 "nodes": {"exact_nodes": {"values": [5]}},
 "rule": {"effect": {"value": {"deny": {}}},
          "selector": {"permissions": {"exact_permissions": {"values": [1]}},
                       "spaces":      {"all_spaces_excluding": {"values": [4]}}}}}
`;
</script>

<svelte:head>
  <title>Node access policy — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Node access policy</h1>

<section id="content" class="page">
<p>Node access policies decide which node may run which space's deployments, and have the following type definition:</p>

<CodeBlock code={typeDefinition} language="typedef" title="NodeAccessPolicy" />

<p>A node is an identity, as a user is, and a policy gives the nodes it selects one rule. The rule is written like an <a href="{base}/reference/user-and-access-rule/">access rule</a>: an effect, allow or deny, and a selector of permissions and spaces. The nodes, the permissions, and the spaces each set exactly one list. The exact list covers the values it names, and the excluding list covers everything else, so an empty excluding list covers everything. There is one node permission, running deployments.</p>

<p>For one node and one space, an enabled deny that matches refuses it. Otherwise an enabled allow that matches permits it. What no allow covers is refused. A narrower allow does not override a broader deny. This policy keeps node 5 for space 4:</p>

<CodeBlock code={example} language="json" title="A deny for every other space" />

<p>A cluster holds two built-in policies, which cannot be edited or deleted. <code>system_everywhere</code> lets every node run the <code>_system</code> space, where OpenDeploy's own deployments live, and is always enabled. <code>all_spaces_everywhere</code> lets every node run every space, so a new node and a new space need no policy. To narrow a node, add a deny as above. To start from nothing, disable <code>all_spaces_everywhere</code> and write an allow for each node or group of nodes. A disabled policy is ignored and can be enabled again.</p>

<p>Placing a deployment on a node that may not run its space is refused, and so is moving a deployment into a space its node may not run. The message names the deny, or says that no policy allows it. A change to the policies, including disabling or deleting one, is refused when it would leave a deployment, running or stopped, on a node that may no longer run it. The message lists those deployments. Move or delete them first. Policies therefore never strand a deployment.</p>

<p>Node access policies are cluster-wide records, managed on the Nodes page under Access policies. Managing them requires the <code>access</code> permission, which cluster administrators hold. Everyone else sees the outcome: the Spaces column of the Nodes page and the node list of the deployment form show the spaces each node may run. A user who is limited to some spaces sees only the nodes that may run one of them. A policy that names a space blocks deleting that space.</p>
</section>
