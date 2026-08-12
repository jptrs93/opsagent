package authz

import "github.com/jptrs93/opsagent/backend/apigen"

const (
	ClusterAdminTemplateID int64 = 1
	SpaceAdminTemplateID   int64 = 2
)

const spaceAdminSpacesArgID int64 = 1

func builtinTemplates() []*apigen.AuthzRuleTemplateRecord {
	all := func() *apigen.AuthzSelector { return &apigen.AuthzSelector{Wildcard: true} }
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
	return []*apigen.AuthzRuleTemplateRecord{
		{
			ID:      ClusterAdminTemplateID,
			Name:    "cluster_admin",
			Builtin: true,
			Template: &apigen.AuthzRuleTemplate{Rules: []*apigen.AuthzRule{
				{
					Permissions: all(),
					Spaces:      all(),
					EntityTypes: all(),
					EntityRefs:  all(),
				},
				{
					Permissions:       &apigen.AuthzSelector{Wildcard: true, Exclude: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_REVEAL)}},
					Spaces:            &apigen.AuthzSelector{Wildcard: true, Exclude: []int64{0}},
					EntityTypes:       all(),
					EntityRefs:        all(),
					DelegationAllowed: true,
				},
				directoryRule(),
			}},
		},
		{
			ID:      SpaceAdminTemplateID,
			Name:    "space_admin",
			Builtin: true,
			Template: &apigen.AuthzRuleTemplate{
				Arguments: []*apigen.AuthzTemplateArgument{{ID: spaceAdminSpacesArgID, Name: "spaces"}},
				Rules: []*apigen.AuthzRule{
					{
						Permissions: all(),
						Spaces:      &apigen.AuthzSelector{ArgumentID: spaceAdminSpacesArgID},
						EntityTypes: all(),
						EntityRefs:  all(),
					},
					{
						Permissions:       &apigen.AuthzSelector{Wildcard: true, Exclude: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_REVEAL)}},
						Spaces:            &apigen.AuthzSelector{ArgumentID: spaceAdminSpacesArgID},
						EntityTypes:       all(),
						EntityRefs:        all(),
						DelegationAllowed: true,
					},
					directoryRule(),
				},
			},
		},
	}
}
