<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';

  const typeDefinition = `type SpaceDomainPolicy root {
    id: u64
    name: string
    disabled: bool
    rule: type SpaceDomainRule {
        effect: type SpaceDomainAllow {
        } | type SpaceDomainDeny {
        }
        selector: type SpaceDomainSelector {
            spaces: AuthzSpaceSelector
            domains: type DomainSelector {
                exact_domains: ?[]string
                all_domains_excluding: ?[]string
            }
        }
    }
}
`;

  const example = `{"name": "prod-names",
 "rule": {"effect": {"value": {"deny": {}}},
          "selector": {"spaces":  {"all_spaces_excluding": {"values": [3]}},
                       "domains": {"exact_domains": {"values": ["example.com", "*.example.com"]}}}}}
`;
</script>

<svelte:head>
  <title>Domain policy — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Domain policy</h1>

<section id="content" class="page">
<p>Domain policies decide which space may publish which ingress hostname, and have the following type definition:</p>

<CodeBlock code={typeDefinition} language="typedef" title="SpaceDomainPolicy" />

<p>A policy is written like an <a href="{base}/reference/user-and-access-rule/">access rule</a>: an effect, allow or deny, and a selector of spaces and domains. Each of the two sets exactly one list. The exact list covers the values it names, and the excluding list covers everything else, so an empty excluding list covers everything. A domain is a hostname, or <code>*.</code> and a hostname for every name below it at any depth. <code>*.example.com</code> covers <code>api.example.com</code> and <code>a.b.example.com</code> but not <code>example.com</code>, so a policy for a whole domain names both.</p>

<p>For one space and one hostname, an enabled deny that matches refuses it. Otherwise an enabled allow that matches permits it. A hostname that no allow covers is refused. A narrower allow does not override a broader deny, so a deny names the exact hostnames it protects. This policy keeps <code>example.com</code> and everything below it for space 3:</p>

<CodeBlock code={example} language="json" title="A deny for every other space" />

<p>A cluster starts with one policy, <code>default_ingress_domains</code>, which allows every space every hostname, so a first route needs no policy. While it is enabled any space can publish any hostname nobody has taken. To give a hostname to one space, add a deny for every other space as above, or disable the default and write an allow for each space. A disabled policy is ignored and can be enabled again.</p>

<p>Saving a deployment whose space is not allowed one of its ingress hostnames is refused, and so is moving such a deployment into a space that is not allowed it. A change to the policies, including disabling or deleting one, is refused when it would leave a hostname that a deployment already publishes without permission. The message lists those hostnames. Change or remove those routes first. Policies therefore never take a running route off the air.</p>

<p>Domain policies are cluster-wide records, managed on the Spaces page under Domain policies. Managing them requires the <code>domain_policy</code> permission, which cluster administrators hold. A policy that names a space blocks deleting that space. How routes claim hostnames is described under <a href="{base}/guides/expose-a-service/">expose a service</a>.</p>
</section>
