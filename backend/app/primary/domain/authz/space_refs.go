package authz

import (
	"context"
	"slices"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// SpaceReferences counts the live access rows that name one space by id:
// grants through a rule selector, an entity ref, or a template binding;
// templates through a literal selector in one of their rules; and global
// rules through their selector.
type SpaceReferences struct {
	Grants      int
	Templates   int
	GlobalRules int
}

func CountSpaceReferences(ctx context.Context, q *pq.Queries, spaceID uint64) (SpaceReferences, error) {
	var refs SpaceReferences
	grants, err := q.ListAuthzGrants(ctx)
	if err != nil {
		return refs, err
	}
	for _, row := range grants {
		grant, err := pq.AuthzGrantEntity(row)
		if err != nil {
			return refs, err
		}
		if grantNamesSpace(grant.Grant, spaceID) {
			refs.Grants++
		}
	}
	templates, err := q.ListAuthzGrantTemplates(ctx)
	if err != nil {
		return refs, err
	}
	for _, row := range templates {
		template, err := pq.AuthzGrantTemplateEntity(row)
		if err != nil {
			return refs, err
		}
		if slices.ContainsFunc(template.Spec.Rules, func(r apigen.AuthzTemplateRule) bool {
			return templateSelectorNamesSpace(r.Selector, spaceID)
		}) {
			refs.Templates++
		}
	}
	rules, err := q.ListAuthzGlobalRules(ctx)
	if err != nil {
		return refs, err
	}
	for _, row := range rules {
		rule, err := pq.AuthzGlobalRuleEntity(row)
		if err != nil {
			return refs, err
		}
		if selectorNamesSpace(rule.Rule.Selector, spaceID) {
			refs.GlobalRules++
		}
	}
	return refs, nil
}

func grantNamesSpace(source apigen.AuthzGrantSource, spaceID uint64) bool {
	if source.Value.Rule != nil {
		return selectorNamesSpace(source.Value.Rule.Selector, spaceID)
	}
	if source.Value.Template == nil {
		return false
	}
	for _, binding := range source.Value.Template.Args {
		v := binding.Values.Value
		if v.Spaces != nil && slices.Contains(v.Spaces.Values, spaceID) {
			return true
		}
		if v.EntityRefs != nil && entityRefsNameSpace(v.EntityRefs.Values, spaceID) {
			return true
		}
	}
	return false
}

func selectorNamesSpace(sel apigen.AuthzSelector, spaceID uint64) bool {
	return spaceSelectorNamesSpace(sel.Spaces, spaceID) || entityRefSelectorNamesSpace(sel.EntityRefs, spaceID)
}

func templateSelectorNamesSpace(sel apigen.AuthzTemplateSelector, spaceID uint64) bool {
	if s := sel.Spaces.Value.Selector; s != nil && spaceSelectorNamesSpace(*s, spaceID) {
		return true
	}
	if s := sel.EntityRefs.Value.Selector; s != nil && entityRefSelectorNamesSpace(*s, spaceID) {
		return true
	}
	return false
}

func spaceSelectorNamesSpace(sel apigen.AuthzSpaceSelector, spaceID uint64) bool {
	return slices.Contains(sel.ExactSpaces.Value.Values, spaceID) || slices.Contains(sel.AllSpacesExcluding.Value.Values, spaceID)
}

func entityRefSelectorNamesSpace(sel apigen.AuthzEntityRefSelector, spaceID uint64) bool {
	return entityRefsNameSpace(sel.ExactEntityRefs.Value.Values, spaceID) || entityRefsNameSpace(sel.AllEntityRefsExcluding.Value.Values, spaceID)
}

func entityRefsNameSpace(refs []apigen.AuthzEntityRef, spaceID uint64) bool {
	return slices.ContainsFunc(refs, func(ref apigen.AuthzEntityRef) bool {
		return ref.Target.Value.Space != nil && *ref.Target.Value.Space == spaceID
	})
}
