type AuthzGrantTemplate@12 root {
    id@1: u64 [key]
    name@2: string [max_length = 64, min_length = 1]
    builtin@3: bool [mutable = false]
    spec@4: type AuthzGrantTemplateSpec {
        arguments@1: []type AuthzTemplateArgument {
            id@1: u32 [min = 1]
            name@2: string [max_length = 64, min_length = 1]
            kind@3: enum AuthzArgumentKind {
                Permission@1
                Space@2
                EntityType@3
                EntityRef@4
            }
        }
        rules@2: []AuthzTemplateRule [min_items = 1]
        laws {
            argument_ids_unique {
                assert all a in arguments : (count b in arguments where b.id == a.id) == 1
            }
            argument_names_unique {
                assert all a in arguments : (count b in arguments where b.name == a.name) == 1
            }
            arguments_declared {
                assert all r in rules where r.selector.permissions is AuthzArgument p : any a in arguments : a.id == p.argument_id && a.kind == Permission
                assert all r in rules where r.selector.spaces is AuthzArgument s : any a in arguments : a.id == s.argument_id && a.kind == Space
                assert all r in rules where r.selector.entity_types is AuthzArgument e : any a in arguments : a.id == e.argument_id && a.kind == EntityType
                assert all r in rules where r.selector.entity_refs is AuthzArgument f : any a in arguments : a.id == f.argument_id && a.kind == EntityRef
            }
            arguments_used {
                assert all a in arguments : any r in rules : r.selector.permissions is AuthzArgument p && p.argument_id == a.id || r.selector.spaces is AuthzArgument s && s.argument_id == a.id || r.selector.entity_types is AuthzArgument e && e.argument_id == a.id || r.selector.entity_refs is AuthzArgument f && f.argument_id == a.id
            }
        }
    }
    laws {
        authz_template_name {
            unique (name)
        }
        builtin_read_only on update {
            forbid before.builtin && (before.name != after.name || before.spec != after.spec)
        }
        builtin_undeletable on delete {
            forbid before.builtin
        }
    }
}

type AuthzGrant@13 root {
    id@1: u64 [key]
    user_id@2: User.id
    grant@3: AuthzRule@1 | type AuthzTemplateGrant@2 {
        template_id@1: AuthzGrantTemplate.id
        args@2: []type AuthzArgumentBinding {
            argument_id@1: u32 [min = 1]
            values@2: AuthzPermissionValues@1 | AuthzSpaceValues@2 | AuthzEntityValues@3 | AuthzReferenceValues@4
        }
        laws {
            every_argument_bound_once {
                assert all a in template_id->spec.arguments : (count b in args where b.argument_id == a.id) == 1
            }
            every_binding_declared {
                assert all b in args : any a in template_id->spec.arguments : b.argument_id == a.id
            }
            binding_types_match {
                assert all b in args : any a in template_id->spec.arguments : b.argument_id == a.id && (a.kind == Permission && b.values is AuthzPermissionValues || a.kind == Space && b.values is AuthzSpaceValues || a.kind == EntityType && b.values is AuthzEntityValues || a.kind == EntityRef && b.values is AuthzReferenceValues)
            }
            cannot_deny_access {
                forbid any b in args : b.values is AuthzEntityValues v && AuthzEntityKind.Access in v.values && (any r in template_id->spec.rules : r.effect is AuthzDeny && r.selector.entity_types is AuthzArgument a && a.argument_id == b.argument_id)
            }
        }
    }
}

type AuthzGlobalRule@14 root {
    id@1: u64 [key]
    name@2: string [max_length = 64, min_length = 1]
    rule@3: AuthzRule
}

type AuthzRule {
    effect@1: AuthzAllow@1 | AuthzDeny@2
    selector@2: AuthzSelector
    laws {
        cannot_deny_access {
            forbid effect is AuthzDeny && (selector.entity_types.all_entity_types_excluding is present && !(AuthzEntityKind.Access in selector.entity_types.all_entity_types_excluding ?? []) || AuthzEntityKind.Access in selector.entity_types.exact_entity_types ?? [])
        }
    }
}

type AuthzTemplateRule {
    effect@1: AuthzAllow@1 | AuthzDeny@2
    selector@2: AuthzTemplateSelector
    laws {
        cannot_deny_access {
            forbid effect is AuthzDeny && selector.entity_types is AuthzEntityTypeSelector s && (s.all_entity_types_excluding is present && !(AuthzEntityKind.Access in s.all_entity_types_excluding ?? []) || AuthzEntityKind.Access in s.exact_entity_types ?? [])
        }
    }
}

type AuthzAllow {
    delegation_allowed@1: bool
}

type AuthzDeny {
    delegated_only@1: bool
}

type AuthzSelector {
    permissions@1: AuthzPermissionSelector
    spaces@2: AuthzSpaceSelector
    entity_types@3: AuthzEntityTypeSelector
    entity_refs@4: AuthzEntityRefSelector
}

type AuthzTemplateSelector {
    permissions@1: AuthzArgument@1 | AuthzPermissionSelector@2
    spaces@2: AuthzArgument@1 | AuthzSpaceSelector@2
    entity_types@3: AuthzArgument@1 | AuthzEntityTypeSelector@2
    entity_refs@4: AuthzArgument@1 | AuthzEntityRefSelector@2
}

type AuthzArgument {
    argument_id@1: u32 [min = 1]
}

type AuthzPermissionSelector {
    exact_verbs@1: ?[]AuthzVerb [min_items = 1]
    all_verbs_excluding@2: ?[]AuthzVerb
    laws {
        one_or_the_other {
            assert exact_verbs is present && all_verbs_excluding is absent || exact_verbs is absent && all_verbs_excluding is present
        }
    }
}

type AuthzSpaceSelector {
    exact_spaces@1: ?[]Space.id [min_items = 1]
    all_spaces_excluding@2: ?[]Space.id
    laws {
        one_or_the_other {
            assert exact_spaces is present && all_spaces_excluding is absent || exact_spaces is absent && all_spaces_excluding is present
        }
    }
}

type AuthzEntityTypeSelector {
    exact_entity_types@1: ?[]AuthzEntityKind [min_items = 1]
    all_entity_types_excluding@2: ?[]AuthzEntityKind
    laws {
        one_or_the_other {
            assert exact_entity_types is present && all_entity_types_excluding is absent || exact_entity_types is absent && all_entity_types_excluding is present
        }
    }
}

type AuthzEntityRefSelector {
    exact_entity_refs@1: ?[]AuthzEntityRef [min_items = 1]
    all_entity_refs_excluding@2: ?[]AuthzEntityRef
    laws {
        one_or_the_other {
            assert exact_entity_refs is present && all_entity_refs_excluding is absent || exact_entity_refs is absent && all_entity_refs_excluding is present
        }
    }
}

type AuthzPermissionValues {
    values@1: []AuthzVerb [min_items = 1]
}

type AuthzSpaceValues {
    values@1: []Space.id [min_items = 1]
}

type AuthzEntityValues {
    values@1: []AuthzEntityKind [min_items = 1]
}

type AuthzReferenceValues {
    values@1: []AuthzEntityRef [min_items = 1]
}

enum AuthzVerb {
    Create@1
    Update@2
    Delete@3
    View@4
    ViewLogs@5
    Reveal@6
    UseHostMounts@7
    UseHostNetwork@8
}

enum AuthzEntityKind {
    Space@1
    Deployment@2
    Secret@3
    Config@4
    Asset@5
    Node@6
    Cluster@7
    User@8
    Access@9
}

type AuthzEntityRef {
    target@1: Space.id@1 | Deployment.id@2 | Secret.id@3 | Config.id@4 | Asset.id@5 | Node.id@6 | SystemConfig.id@7 | User.id@8 | AuthzGrantTemplate.id@9 | AuthzGrant.id@10 | AuthzGlobalRule.id@11
}
