package authz

import "github.com/jptrs93/opsagent/backend/apigen"

const (
	ClusterAdminTemplateID uint64 = 1
	SpaceAdminTemplateID   uint64 = 2
)

const spaceAdminSpacesArgID uint32 = 1

// DefaultUserVisibilityRuleName names the seeded allow-mode global rule that
// makes the user roster visible to everyone, so a space-limited operator can
// resolve names for audit display. It is seeded exactly once: deletion leaves
// a tombstone row, so an admin who deletes it has opted out and a restart
// must not resurrect that decision.
const DefaultUserVisibilityRuleName = "default_user_visibility"

func allow(delegationAllowed bool) apigen.AuthzEffect {
	return apigen.AuthzEffect{Value: apigen.AuthzEffectValueOneof{Allow: &apigen.AuthzAllow{DelegationAllowed: delegationAllowed}}}
}

func allVerbsExcluding(vs ...apigen.AuthzVerb) apigen.AuthzPermissionSelector {
	return apigen.AuthzPermissionSelector{AllVerbsExcluding: apigen.Some(apigen.AuthzVerbList{Values: vs})}
}

func exactVerbs(vs ...apigen.AuthzVerb) apigen.AuthzPermissionSelector {
	return apigen.AuthzPermissionSelector{ExactVerbs: apigen.Some(apigen.AuthzVerbList{Values: vs})}
}

func allSpacesExcluding(ids ...uint64) apigen.AuthzSpaceSelector {
	return apigen.AuthzSpaceSelector{AllSpacesExcluding: apigen.Some(apigen.SpaceIdList{Values: ids})}
}

func exactSpaces(ids ...uint64) apigen.AuthzSpaceSelector {
	return apigen.AuthzSpaceSelector{ExactSpaces: apigen.Some(apigen.SpaceIdList{Values: ids})}
}

func allEntityTypesExcluding(ks ...apigen.AuthzEntityKind) apigen.AuthzEntityTypeSelector {
	return apigen.AuthzEntityTypeSelector{AllEntityTypesExcluding: apigen.Some(apigen.AuthzEntityKindList{Values: ks})}
}

func exactEntityTypes(ks ...apigen.AuthzEntityKind) apigen.AuthzEntityTypeSelector {
	return apigen.AuthzEntityTypeSelector{ExactEntityTypes: apigen.Some(apigen.AuthzEntityKindList{Values: ks})}
}

func allEntityRefs() apigen.AuthzEntityRefSelector {
	return apigen.AuthzEntityRefSelector{AllEntityRefsExcluding: apigen.Some(apigen.AuthzEntityRefList{})}
}

func templatePermissions(sel apigen.AuthzPermissionSelector) apigen.AuthzTemplatePermissionSelector {
	return apigen.AuthzTemplatePermissionSelector{Value: apigen.AuthzTemplatePermissionSelectorValueOneof{Selector: &sel}}
}

func templateSpaces(sel apigen.AuthzSpaceSelector) apigen.AuthzTemplateSpaceSelector {
	return apigen.AuthzTemplateSpaceSelector{Value: apigen.AuthzTemplateSpaceSelectorValueOneof{Selector: &sel}}
}

func templateSpacesArgument(id uint32) apigen.AuthzTemplateSpaceSelector {
	return apigen.AuthzTemplateSpaceSelector{Value: apigen.AuthzTemplateSpaceSelectorValueOneof{Argument: &apigen.AuthzArgument{ArgumentID: id}}}
}

func templateEntityTypes(sel apigen.AuthzEntityTypeSelector) apigen.AuthzTemplateEntityTypeSelector {
	return apigen.AuthzTemplateEntityTypeSelector{Value: apigen.AuthzTemplateEntityTypeSelectorValueOneof{Selector: &sel}}
}

func templateEntityRefs(sel apigen.AuthzEntityRefSelector) apigen.AuthzTemplateEntityRefSelector {
	return apigen.AuthzTemplateEntityRefSelector{Value: apigen.AuthzTemplateEntityRefSelectorValueOneof{Selector: &sel}}
}

func defaultUserVisibilityRule() *apigen.AuthzRule {
	return &apigen.AuthzRule{
		Effect: allow(true),
		Selector: apigen.AuthzSelector{
			Permissions: exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_VIEW),
			Spaces:      exactSpaces(0),
			EntityTypes: exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_USER),
			EntityRefs:  allEntityRefs(),
		},
	}
}

func builtinTemplates() []*apigen.AuthzGrantTemplate {
	withoutHostAccess := func() apigen.AuthzPermissionSelector {
		return allVerbsExcluding(
			apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_MOUNTS,
			apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_NETWORK,
		)
	}
	// delegableRules is what an agent session inherits from the grant. Secrets
	// are split out of the general rule and handed back view+create: an agent
	// can see that a secret exists, mint one, and wire it into a deployment,
	// but cannot read back, change, or destroy a value an operator owns —
	// reveal, edit, and delete stay human-only. Nothing outside these rules
	// narrows a delegated token, so this is the whole of what agents may do.
	// Agent sessions also lose view_logs: deployment logs can echo secret
	// values at runtime, which would sidestep the create-only secret boundary
	// below. Host access also requires an explicit grant for agent sessions.
	agentPerms := func() apigen.AuthzPermissionSelector {
		return allVerbsExcluding(
			apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_MOUNTS,
			apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_NETWORK,
			apigen.AuthzVerb_AUTHZ_VERB_VIEW_LOGS,
		)
	}
	delegableRules := func(spaces func() apigen.AuthzTemplateSpaceSelector) []apigen.AuthzTemplateRule {
		return []apigen.AuthzTemplateRule{
			{
				Effect: allow(true),
				Selector: apigen.AuthzTemplateSelector{
					Permissions: templatePermissions(agentPerms()),
					Spaces:      spaces(),
					EntityTypes: templateEntityTypes(allEntityTypesExcluding(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET)),
					EntityRefs:  templateEntityRefs(allEntityRefs()),
				},
			},
			{
				Effect: allow(true),
				Selector: apigen.AuthzTemplateSelector{
					Permissions: templatePermissions(exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_VIEW, apigen.AuthzVerb_AUTHZ_VERB_CREATE)),
					Spaces:      spaces(),
					EntityTypes: templateEntityTypes(exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET)),
					EntityRefs:  templateEntityRefs(allEntityRefs()),
				},
			},
		}
	}
	allSpaces := func() apigen.AuthzTemplateSpaceSelector { return templateSpaces(allSpacesExcluding()) }
	// clusterAdminSpaces is every space but 0: cluster-level entities stay
	// human-only even for a fully privileged agent.
	clusterAdminSpaces := func() apigen.AuthzTemplateSpaceSelector { return templateSpaces(allSpacesExcluding(0)) }
	spaceAdminSpaces := func() apigen.AuthzTemplateSpaceSelector { return templateSpacesArgument(spaceAdminSpacesArgID) }
	// Human cluster admins have host access everywhere. Space admins and both
	// roles' agent sessions require an additional grant for host access.
	templateRules := func(operatorPermissions apigen.AuthzPermissionSelector, operatorSpaces, delegableSpaces func() apigen.AuthzTemplateSpaceSelector) []apigen.AuthzTemplateRule {
		rules := []apigen.AuthzTemplateRule{{
			Effect: allow(false),
			Selector: apigen.AuthzTemplateSelector{
				Permissions: templatePermissions(operatorPermissions),
				Spaces:      operatorSpaces(),
				EntityTypes: templateEntityTypes(allEntityTypesExcluding()),
				EntityRefs:  templateEntityRefs(allEntityRefs()),
			},
		}}
		return append(rules, delegableRules(delegableSpaces)...)
	}
	return []*apigen.AuthzGrantTemplate{
		{
			ID:      ClusterAdminTemplateID,
			Name:    "cluster_admin",
			Builtin: true,
			Spec:    apigen.AuthzGrantTemplateSpec{Rules: templateRules(allVerbsExcluding(), allSpaces, clusterAdminSpaces)},
		},
		{
			ID:      SpaceAdminTemplateID,
			Name:    "space_admin",
			Builtin: true,
			Spec: apigen.AuthzGrantTemplateSpec{
				Arguments: []apigen.AuthzTemplateArgument{{ID: spaceAdminSpacesArgID, Name: "spaces", Kind: apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_SPACE}},
				Rules:     templateRules(withoutHostAccess(), spaceAdminSpaces, spaceAdminSpaces),
			},
		},
	}
}
