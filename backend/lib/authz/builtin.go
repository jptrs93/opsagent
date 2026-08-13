package authz

import "github.com/jptrs93/opsagent/backend/apigen"

const (
	ClusterAdminTemplateID int64 = 1
	SpaceAdminTemplateID   int64 = 2
)

const spaceAdminSpacesArgID int64 = 1

func builtinTemplates() []*apigen.AuthzRuleTemplateRecord {
	all := func() *apigen.AuthzSelector { return &apigen.AuthzSelector{Wildcard: true} }
	// delegableRules is what an agent session inherits from the grant. Secrets
	// are split out of the general rule and handed back view+create: an agent
	// can see that a secret exists, mint one, and wire it into a deployment,
	// but cannot read back, change, or destroy a value an operator owns —
	// reveal, edit, and delete stay human-only. Nothing outside these rules
	// narrows a delegated token, so this is the whole of what agents may do.
	// Agent sessions also lose view_logs: deployment logs can echo secret
	// values at runtime, which would sidestep the create-only secret boundary
	// below.
	agentPerms := func() *apigen.AuthzSelector {
		return &apigen.AuthzSelector{Wildcard: true, Exclude: []int64{
			int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW_LOGS),
		}}
	}
	delegableRules := func(spaces func() *apigen.AuthzSelector) []*apigen.AuthzRule {
		return []*apigen.AuthzRule{
			{
				Permissions: agentPerms(),
				Spaces:      spaces(),
				EntityTypes: &apigen.AuthzSelector{Wildcard: true, Exclude: []int64{
					int64(apigen.AuthzEntity_AUTHZ_ENTITY_SECRET),
				}},
				EntityRefs:        all(),
				DelegationAllowed: true,
			},
			{
				Permissions: &apigen.AuthzSelector{Include: []int64{
					int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW),
					int64(apigen.AuthzVerb_AUTHZ_VERB_CREATE),
				}},
				Spaces: spaces(),
				EntityTypes: &apigen.AuthzSelector{Include: []int64{
					int64(apigen.AuthzEntity_AUTHZ_ENTITY_SECRET),
				}},
				EntityRefs:        all(),
				DelegationAllowed: true,
			},
		}
	}
	directoryRule := func() *apigen.AuthzRule {
		return &apigen.AuthzRule{
			Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW)}},
			Spaces:      &apigen.AuthzSelector{Include: []int64{0}},
			EntityTypes: &apigen.AuthzSelector{Include: []int64{
				int64(apigen.AuthzEntity_AUTHZ_ENTITY_NODE),
				int64(apigen.AuthzEntity_AUTHZ_ENTITY_USER),
			}},
			EntityRefs:        all(),
			DelegationAllowed: true,
		}
	}
	// clusterAdminSpaces is every space but 0: cluster-level entities stay
	// human-only even for a fully privileged agent.
	clusterAdminSpaces := func() *apigen.AuthzSelector {
		return &apigen.AuthzSelector{Wildcard: true, Exclude: []int64{0}}
	}
	spaceAdminSpaces := func() *apigen.AuthzSelector {
		return &apigen.AuthzSelector{ArgumentID: spaceAdminSpacesArgID}
	}
	// Both builtins have the same shape: everything in the operator's own spaces
	// for the human holding the grant, then the delegable subset for their
	// agents. They differ only in which spaces "their own" means.
	templateRules := func(operatorSpaces, delegableSpaces func() *apigen.AuthzSelector) []*apigen.AuthzRule {
		rules := []*apigen.AuthzRule{{
			Permissions: all(),
			Spaces:      operatorSpaces(),
			EntityTypes: all(),
			EntityRefs:  all(),
		}}
		rules = append(rules, delegableRules(delegableSpaces)...)
		return append(rules, directoryRule())
	}
	return []*apigen.AuthzRuleTemplateRecord{
		{
			ID:       ClusterAdminTemplateID,
			Name:     "cluster_admin",
			Builtin:  true,
			Template: &apigen.AuthzRuleTemplate{Rules: templateRules(all, clusterAdminSpaces)},
		},
		{
			ID:      SpaceAdminTemplateID,
			Name:    "space_admin",
			Builtin: true,
			Template: &apigen.AuthzRuleTemplate{
				Arguments: []*apigen.AuthzTemplateArgument{{ID: spaceAdminSpacesArgID, Name: "spaces"}},
				Rules:     templateRules(spaceAdminSpaces, spaceAdminSpaces),
			},
		},
	}
}
