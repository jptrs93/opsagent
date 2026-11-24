type SpaceDomainPolicy@22 root {
    id@1: u64 [key]
    name@2: string [max_length = 64, min_length = 1]
    disabled@3: bool
    rule@4: type SpaceDomainRule {
        effect@1: type SpaceDomainAllow@1 {
        } | type SpaceDomainDeny@2 {
        }
        selector@2: type SpaceDomainSelector {
            spaces@1: AuthzSpaceSelector
            domains@2: type DomainSelector {
                exact_domains@1: ?[]string [min_items = 1]
                all_domains_excluding@2: ?[]string
                laws {
                    one_or_the_other {
                        assert exact_domains is present && all_domains_excluding is absent || exact_domains is absent && all_domains_excluding is present
                    }
                }
            }
        }
    }
    laws {
        domain_policy_name {
            unique (name)
        }
    }
}
