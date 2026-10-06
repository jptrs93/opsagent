package authz

import (
	"slices"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// EntityRefTarget returns the kind and id an entity ref names. The three
// access-management targets all report the ACCESS kind, and the system
// config target reports CLUSTER.
func EntityRefTarget(ref apigen.AuthzEntityRef) (apigen.AuthzEntityKind, uint64, bool) {
	v := ref.Target.Value
	switch {
	case v.Space != nil:
		return apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SPACE, *v.Space, true
	case v.Deployment != nil:
		return apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT, *v.Deployment, true
	case v.Secret != nil:
		return apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET, *v.Secret, true
	case v.Config != nil:
		return apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_CONFIG, *v.Config, true
	case v.Asset != nil:
		return apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ASSET, *v.Asset, true
	case v.Node != nil:
		return apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_NODE, *v.Node, true
	case v.SystemConfig != nil:
		return apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_CLUSTER, *v.SystemConfig, true
	case v.User != nil:
		return apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_USER, *v.User, true
	case v.AuthzGrantTemplate != nil:
		return apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ACCESS, *v.AuthzGrantTemplate, true
	case v.AuthzGrant != nil:
		return apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ACCESS, *v.AuthzGrant, true
	case v.AuthzGlobalRule != nil:
		return apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ACCESS, *v.AuthzGlobalRule, true
	}
	return apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_UNSPECIFIED, 0, false
}

func refsContain(refs []apigen.AuthzEntityRef, kind apigen.AuthzEntityKind, id uint64) bool {
	for _, ref := range refs {
		if k, refID, ok := EntityRefTarget(ref); ok && k == kind && refID == id {
			return true
		}
	}
	return false
}

func verbMatches(sel apigen.AuthzPermissionSelector, v apigen.AuthzVerb) bool {
	if sel.ExactVerbs.Present {
		return slices.Contains(sel.ExactVerbs.Value.Values, v)
	}
	if sel.AllVerbsExcluding.Present {
		return !slices.Contains(sel.AllVerbsExcluding.Value.Values, v)
	}
	return false
}

func spaceMatches(sel apigen.AuthzSpaceSelector, id uint64) bool {
	if sel.ExactSpaces.Present {
		return slices.Contains(sel.ExactSpaces.Value.Values, id)
	}
	if sel.AllSpacesExcluding.Present {
		return !slices.Contains(sel.AllSpacesExcluding.Value.Values, id)
	}
	return false
}

func entityTypeMatches(sel apigen.AuthzEntityTypeSelector, k apigen.AuthzEntityKind) bool {
	if sel.ExactEntityTypes.Present {
		return slices.Contains(sel.ExactEntityTypes.Value.Values, k)
	}
	if sel.AllEntityTypesExcluding.Present {
		return !slices.Contains(sel.AllEntityTypesExcluding.Value.Values, k)
	}
	return false
}

func entityRefMatches(sel apigen.AuthzEntityRefSelector, kind apigen.AuthzEntityKind, id uint64) bool {
	if sel.ExactEntityRefs.Present {
		return refsContain(sel.ExactEntityRefs.Value.Values, kind, id)
	}
	if sel.AllEntityRefsExcluding.Present {
		return !refsContain(sel.AllEntityRefsExcluding.Value.Values, kind, id)
	}
	return false
}

func selectorMatches(sel apigen.AuthzSelector, req RequestedAccess) bool {
	return verbMatches(sel.Permissions, req.Verb) &&
		spaceMatches(sel.Spaces, req.SpaceID) &&
		entityTypeMatches(sel.EntityTypes, req.EntityType) &&
		entityRefMatches(sel.EntityRefs, req.EntityType, req.EntityID)
}

func binding(bindings []apigen.AuthzArgumentBinding, argumentID uint32) *apigen.AuthzArgumentBinding {
	for i := range bindings {
		if bindings[i].ArgumentID == argumentID {
			return &bindings[i]
		}
	}
	return nil
}

func templateVerbMatches(sel apigen.AuthzTemplatePermissionSelector, bindings []apigen.AuthzArgumentBinding, v apigen.AuthzVerb) bool {
	if a := sel.Value.Argument; a != nil {
		b := binding(bindings, a.ArgumentID)
		return b != nil && b.Values.Value.Permissions != nil && slices.Contains(b.Values.Value.Permissions.Values, v)
	}
	if s := sel.Value.Selector; s != nil {
		return verbMatches(*s, v)
	}
	return false
}

func templateSpaceMatches(sel apigen.AuthzTemplateSpaceSelector, bindings []apigen.AuthzArgumentBinding, id uint64) bool {
	if a := sel.Value.Argument; a != nil {
		b := binding(bindings, a.ArgumentID)
		return b != nil && b.Values.Value.Spaces != nil && slices.Contains(b.Values.Value.Spaces.Values, id)
	}
	if s := sel.Value.Selector; s != nil {
		return spaceMatches(*s, id)
	}
	return false
}

func templateEntityTypeMatches(sel apigen.AuthzTemplateEntityTypeSelector, bindings []apigen.AuthzArgumentBinding, k apigen.AuthzEntityKind) bool {
	if a := sel.Value.Argument; a != nil {
		b := binding(bindings, a.ArgumentID)
		return b != nil && b.Values.Value.EntityTypes != nil && slices.Contains(b.Values.Value.EntityTypes.Values, k)
	}
	if s := sel.Value.Selector; s != nil {
		return entityTypeMatches(*s, k)
	}
	return false
}

func templateEntityRefMatches(sel apigen.AuthzTemplateEntityRefSelector, bindings []apigen.AuthzArgumentBinding, kind apigen.AuthzEntityKind, id uint64) bool {
	if a := sel.Value.Argument; a != nil {
		b := binding(bindings, a.ArgumentID)
		return b != nil && b.Values.Value.EntityRefs != nil && refsContain(b.Values.Value.EntityRefs.Values, kind, id)
	}
	if s := sel.Value.Selector; s != nil {
		return entityRefMatches(*s, kind, id)
	}
	return false
}

func templateSelectorMatches(sel apigen.AuthzTemplateSelector, bindings []apigen.AuthzArgumentBinding, req RequestedAccess) bool {
	return templateVerbMatches(sel.Permissions, bindings, req.Verb) &&
		templateSpaceMatches(sel.Spaces, bindings, req.SpaceID) &&
		templateEntityTypeMatches(sel.EntityTypes, bindings, req.EntityType) &&
		templateEntityRefMatches(sel.EntityRefs, bindings, req.EntityType, req.EntityID)
}

// SystemSpaceAllows is the fence in front of every grant: space 0 holds
// OpenDeploy's own values, which no caller may see, touch, or reference, and
// its deployments are created by the primary alone. Cluster-scope checks for
// nodes, users, settings, and access management also name space 0 and are
// unaffected.
func SystemSpaceAllows(req RequestedAccess) bool {
	if req.SpaceID != 0 {
		return true
	}
	switch req.EntityType {
	case apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET, apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_CONFIG, apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ASSET:
		return false
	case apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT:
		return req.Verb != apigen.AuthzVerb_AUTHZ_VERB_CREATE
	}
	return true
}

func effectMatches(e apigen.AuthzEffect, req RequestedAccess, deny bool) bool {
	if deny {
		d := e.Value.Deny
		return d != nil && (req.Delegated || !d.DelegatedOnly)
	}
	a := e.Value.Allow
	return a != nil && (!req.Delegated || a.DelegationAllowed)
}

func ruleMatches(rule *apigen.AuthzRule, req RequestedAccess, deny bool) bool {
	return rule != nil && effectMatches(rule.Effect, req, deny) && selectorMatches(rule.Selector, req)
}

func templateRuleMatches(rule *apigen.AuthzTemplateRule, bindings []apigen.AuthzArgumentBinding, req RequestedAccess, deny bool) bool {
	return rule != nil && effectMatches(rule.Effect, req, deny) && templateSelectorMatches(rule.Selector, bindings, req)
}

func allowTouchesSpace(e apigen.AuthzEffect, delegated bool) bool {
	a := e.Value.Allow
	return a != nil && (!delegated || a.DelegationAllowed)
}

func (s *Service) grantTouchesSpaceLocked(g *apigen.AuthzGrant, spaceID uint64, delegated bool) bool {
	if rule := g.Grant.Value.Rule; rule != nil {
		return allowTouchesSpace(rule.Effect, delegated) && spaceMatches(rule.Selector.Spaces, spaceID)
	}
	tg := g.Grant.Value.Template
	if tg == nil {
		return false
	}
	t := s.templates[tg.TemplateID]
	if t == nil {
		return false
	}
	for i := range t.Spec.Rules {
		rule := &t.Spec.Rules[i]
		if allowTouchesSpace(rule.Effect, delegated) && templateSpaceMatches(rule.Selector.Spaces, tg.Args, spaceID) {
			return true
		}
	}
	return false
}

func (s *Service) otherAdminGrantExistsLocked(excludeGrantID uint64) bool {
	for _, grants := range s.grantsByUser {
		for _, g := range grants {
			if g.ID == excludeGrantID {
				continue
			}
			if s.grantMatchesLocked(g, adminAccess, false) {
				return true
			}
		}
	}
	return false
}

func (s *Service) grantMatchesLocked(g *apigen.AuthzGrant, req RequestedAccess, deny bool) bool {
	if rule := g.Grant.Value.Rule; rule != nil {
		return ruleMatches(rule, req, deny)
	}
	tg := g.Grant.Value.Template
	if tg == nil {
		return false
	}
	t := s.templates[tg.TemplateID]
	if t == nil {
		return false
	}
	for i := range t.Spec.Rules {
		if templateRuleMatches(&t.Spec.Rules[i], tg.Args, req, deny) {
			return true
		}
	}
	return false
}
