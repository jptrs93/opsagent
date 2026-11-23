<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';

  const userDefinition = `type User root {
    id: u64
    name: ?string
    email: ?string
    password_hash: ?string
    authentication: type UserAuthentication {
        web_authn_id: bytes
        credentials: []type WebAuthnCredential {
            id: bytes
            data: bytes
        }
    }
    activated_at: ?i64
}
`;

  const accessDefinition = `type AuthzGrant root {
    id: u64
    user_id: User.id
    grant: AuthzRule | type AuthzTemplateGrant {
        template_id: AuthzGrantTemplate.id
        args: []type AuthzArgumentBinding {
            argument_id: u32
            values: AuthzPermissionValues | AuthzSpaceValues | AuthzEntityValues | AuthzReferenceValues
        }
    }
}

type AuthzGlobalRule root {
    id: u64
    name: string
    rule: AuthzRule
}

type AuthzGrantTemplate root {
    id: u64
    name: string
    builtin: bool
    spec: type AuthzGrantTemplateSpec {
        arguments: []type AuthzTemplateArgument {
            id: u32
            name: string
            kind: enum AuthzArgumentKind {
                Permission
                Space
                EntityType
                EntityRef
            }
        }
        rules: []type AuthzTemplateRule {
            effect: AuthzAllow | AuthzDeny
            selector: type AuthzTemplateSelector {
                permissions: AuthzArgument | AuthzPermissionSelector
                spaces: AuthzArgument | AuthzSpaceSelector
                entity_types: AuthzArgument | AuthzEntityTypeSelector
                entity_refs: AuthzArgument | AuthzEntityRefSelector
            }
        }
    }
}

type AuthzRule {
    effect: AuthzAllow | AuthzDeny
    selector: type AuthzSelector {
        permissions: AuthzPermissionSelector
        spaces: AuthzSpaceSelector
        entity_types: AuthzEntityTypeSelector
        entity_refs: AuthzEntityRefSelector
    }
}

type AuthzAllow {
    delegation_allowed: bool
}

type AuthzDeny {
    delegated_only: bool
}

type AuthzArgument {
    argument_id: u32
}

type AuthzPermissionSelector {
    exact_verbs: ?[]AuthzVerb
    all_verbs_excluding: ?[]AuthzVerb
}

type AuthzSpaceSelector {
    exact_spaces: ?[]Space.id
    all_spaces_excluding: ?[]Space.id
}

type AuthzEntityTypeSelector {
    exact_entity_types: ?[]AuthzEntityKind
    all_entity_types_excluding: ?[]AuthzEntityKind
}

type AuthzEntityRefSelector {
    exact_entity_refs: ?[]AuthzEntityRef
    all_entity_refs_excluding: ?[]AuthzEntityRef
}

type AuthzPermissionValues {
    values: []AuthzVerb
}

type AuthzSpaceValues {
    values: []Space.id
}

type AuthzEntityValues {
    values: []AuthzEntityKind
}

type AuthzReferenceValues {
    values: []AuthzEntityRef
}

type AuthzEntityRef {
    target: Space.id | Deployment.id | Secret.id | Config.id | Asset.id | Node.id | SystemConfig.id | User.id | AuthzGrantTemplate.id | AuthzGrant.id | AuthzGlobalRule.id
}

enum AuthzVerb {
    Create
    Update
    Delete
    View
    ViewLogs
    Reveal
    UseHostMounts
    UseHostNetwork
    UsePrivilegedPorts
}

enum AuthzEntityKind {
    Space
    Deployment
    Secret
    Config
    Asset
    Node
    Cluster
    User
    Access
}
`;
</script>

<svelte:head>
  <title>User and access rule — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">User and access rule</h1>

<section id="content" class="page">
<p>Users are the people who sign in to the cluster, and have the following type definition:</p>

<CodeBlock code={userDefinition} language="typedef" title="User" />

<p>A user signs in with a name and password, and may register passkeys from their own account page; the cluster can be set to require a passkey, which turns password sign-in off. A user is created by an administrator with grants already attached, or invited with a single-use enrol link, and stays pending until their first full session. The name must be unique and is absent only while an invitee has yet to pick it. The password hash and passkey credentials are never sent to a browser. A user who is locked out is let back in with a recovery link issued from a root session opened with the master password; see <a href="{base}/design/access-control/">access control</a>.</p>

<p>What a user may do is decided by access rules, which have the following type definitions:</p>

<CodeBlock code={accessDefinition} language="typedef" title="Access rules" />

<p>Access is purely additive: a request is allowed only when some rule allows it, and everything else is denied. A rule pairs an effect with a selector of four positions, permissions, spaces, entity types, and entity references, each of which lists either exactly the values it covers or everything except the listed values, an empty exclusion being the plain wildcard. A grant attaches a rule to one user, either written out directly or by naming a template and binding its arguments; the built-in templates are cluster admin, with no arguments, and space admin, which takes the spaces it covers. Global rules are held by every user. When a request is checked, deny rules are considered first, from both the global rules and the user's grants, then the user's allow grants and the global allow rules. The allow and deny effects carry one flag each for delegated sessions, the tokens agents use on a user's behalf: an allow applies to them only when delegation is allowed, and a deny can be limited to them. No rule may deny the access entity type, so an administrator can always repair the rules. Two verbs guard host resources: creating or updating a deployment with host mounts or host networking needs the matching permission in the deployment's space.</p>
</section>
