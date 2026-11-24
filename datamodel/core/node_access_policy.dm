type NodeAccessPolicy@23 root {
    id@1: u64 [key]
    name@2: string [max_length = 64, min_length = 1]
    disabled@3: bool
    builtin@4: enum NodeAccessBuiltin {
        None@0
        System@1
        Default@2
    }
    nodes@5: type NodeSelector {
        exact_nodes@1: ?[]Node.id [min_items = 1]
        all_nodes_excluding@2: ?[]Node.id
        laws {
            one_or_the_other {
                assert exact_nodes is present && all_nodes_excluding is absent || exact_nodes is absent && all_nodes_excluding is present
            }
        }
    }
    rule@6: type NodeAccessRule {
        effect@1: type NodeAccessAllow@1 {
        } | type NodeAccessDeny@2 {
        }
        selector@2: type NodeAccessSelector {
            permissions@1: type NodePermissionSelector {
                exact_permissions@1: ?[]enum NodePermission {
                    reserve @0
                    RunDeployments@1
                } [min_items = 1]
                all_permissions_excluding@2: ?[]NodePermission
                laws {
                    one_or_the_other {
                        assert exact_permissions is present && all_permissions_excluding is absent || exact_permissions is absent && all_permissions_excluding is present
                    }
                }
            }
            spaces@2: AuthzSpaceSelector
        }
    }
    laws {
        node_access_policy_name {
            unique (name)
        }
        system_builtin_stays_enabled {
            forbid builtin == System && disabled
        }
    }
}
