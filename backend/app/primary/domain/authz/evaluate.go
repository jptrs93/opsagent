package authz

import "github.com/jptrs93/opsagent/backend/apigen"

func bindingValues(bindings []*apigen.AuthzArgumentBinding, argumentID int64) []int64 {
	for _, b := range bindings {
		if b != nil && b.ArgumentID == argumentID {
			return b.Values
		}
	}
	return nil
}

func selectorMatches(sel *apigen.AuthzSelector, bindings []*apigen.AuthzArgumentBinding, v int64) bool {
	if sel == nil {
		return false
	}
	for _, e := range sel.Exclude {
		if e == v {
			return false
		}
	}
	if sel.Wildcard {
		return true
	}
	for _, in := range sel.Include {
		if in == v {
			return true
		}
	}
	if sel.ArgumentID != 0 {
		for _, b := range bindingValues(bindings, sel.ArgumentID) {
			if b == v {
				return true
			}
		}
	}
	return false
}

func ruleMatches(rule *apigen.AuthzRule, bindings []*apigen.AuthzArgumentBinding, req RequestedAccess) bool {
	if rule == nil {
		return false
	}
	if req.Delegated && !rule.DelegationAllowed {
		return false
	}
	return selectorMatches(rule.Permissions, bindings, int64(req.Verb)) &&
		selectorMatches(rule.Spaces, bindings, req.SpaceID) &&
		selectorMatches(rule.EntityTypes, bindings, int64(req.EntityType)) &&
		selectorMatches(rule.EntityRefs, bindings, req.EntityID)
}

func globalDenyMatches(r *apigen.AuthzGlobalRule, req RequestedAccess) bool {
	if r == nil || !r.Deny {
		return false
	}
	if r.DelegatedOnly && !req.Delegated {
		return false
	}
	return globalSelectorsMatch(r, req)
}

// globalAllowMatches evaluates an allow-mode global rule exactly as if every
// user held it as a grant: delegated requests need delegation_allowed, same as
// an ordinary rule.
func globalAllowMatches(r *apigen.AuthzGlobalRule, req RequestedAccess) bool {
	if r == nil || r.Deny {
		return false
	}
	if req.Delegated && !r.DelegationAllowed {
		return false
	}
	return globalSelectorsMatch(r, req)
}

func globalSelectorsMatch(r *apigen.AuthzGlobalRule, req RequestedAccess) bool {
	return selectorMatches(r.Permissions, nil, int64(req.Verb)) &&
		selectorMatches(r.Spaces, nil, req.SpaceID) &&
		selectorMatches(r.EntityTypes, nil, int64(req.EntityType)) &&
		selectorMatches(r.EntityRefs, nil, req.EntityID)
}

func ruleTouchesSpace(rule *apigen.AuthzRule, bindings []*apigen.AuthzArgumentBinding, spaceID int64, delegated bool) bool {
	if rule == nil || (delegated && !rule.DelegationAllowed) {
		return false
	}
	return selectorMatches(rule.Spaces, bindings, spaceID)
}

func (s *Service) grantTouchesSpaceLocked(g *apigen.AuthzGrantRecord, spaceID int64, delegated bool) bool {
	content := g.Grant
	if content == nil {
		return false
	}
	if content.Rule != nil {
		return ruleTouchesSpace(content.Rule, nil, spaceID, delegated)
	}
	t := s.templates[g.TemplateID]
	if t == nil || t.Deleted || t.Template == nil {
		return false
	}
	for _, rule := range t.Template.Rules {
		if ruleTouchesSpace(rule, content.Args, spaceID, delegated) {
			return true
		}
	}
	return false
}

func (s *Service) otherAdminGrantExistsLocked(excludeGrantID int64) bool {
	for _, grants := range s.grantsByUser {
		for _, g := range grants {
			if g.ID == excludeGrantID {
				continue
			}
			if s.grantMatchesLocked(g, adminAccess) {
				return true
			}
		}
	}
	return false
}

func (s *Service) grantMatchesLocked(g *apigen.AuthzGrantRecord, req RequestedAccess) bool {
	content := g.Grant
	if content == nil {
		return false
	}
	if content.Rule != nil {
		return ruleMatches(content.Rule, nil, req)
	}
	t := s.templates[g.TemplateID]
	if t == nil || t.Deleted || t.Template == nil {
		return false
	}
	for _, rule := range t.Template.Rules {
		if ruleMatches(rule, content.Args, req) {
			return true
		}
	}
	return false
}
