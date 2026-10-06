package legacyconv

import (
	"fmt"
	"math"
	"slices"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/apigenold"
)

// AuthzGrantTemplate converts a rule template (item 2) with its untyped
// selectors mapped per item 14. Each argument's kind is the position that
// uses it; an argument used in two positions of different kinds, or by no
// rule, is refused.
func AuthzGrantTemplate(old *apigenold.AuthzRuleTemplate) (*apigen.AuthzGrantTemplate, error) {
	c := &conv{}
	if old == nil {
		c.refuse("AuthzRuleTemplate", "", "nil payload")
		return checked("AuthzGrantTemplate", (*apigen.AuthzGrantTemplate)(nil), c.err)
	}
	out := &apigen.AuthzGrantTemplate{ID: c.id("AuthzRuleTemplate", "id", old.ID), Name: old.Name, Builtin: old.Builtin}
	if old.Spec == nil {
		c.refuse("AuthzRuleTemplate", "spec", "unset")
	} else {
		out.Spec = c.templateSpec(old.Spec)
	}
	return checked("AuthzGrantTemplate", out, c.err)
}

func (c *conv) templateSpec(old *apigenold.AuthzRuleTemplateSpec) apigen.AuthzGrantTemplateSpec {
	var out apigen.AuthzGrantTemplateSpec
	kinds := map[uint32]apigen.AuthzArgumentKind{}
	for i, r := range old.Rules {
		typ := fmt.Sprintf("AuthzRuleTemplateSpec.rules[%d]", i)
		if r == nil {
			c.refuse(typ, "", "nil entry")
			continue
		}
		out.Rules = append(out.Rules, c.templateRule(typ, r, kinds))
	}
	for _, a := range old.Arguments {
		if a == nil {
			c.refuse("AuthzRuleTemplateSpec", "arguments", "nil entry")
			continue
		}
		id := c.u32("AuthzTemplateArgument", "id", a.ID)
		kind, ok := kinds[id]
		if !ok {
			c.refuse("AuthzTemplateArgument", "kind", "argument %d (%q) is used by no rule, so its kind cannot be derived", id, a.Name)
		}
		out.Arguments = append(out.Arguments, apigen.AuthzTemplateArgument{ID: id, Name: a.Name, Kind: kind})
	}
	return out
}

func allowEffect(delegationAllowed bool) apigen.AuthzEffect {
	return apigen.AuthzEffect{Value: apigen.AuthzEffectValueOneof{Allow: &apigen.AuthzAllow{DelegationAllowed: delegationAllowed}}}
}

// position is the typed reading of one old selector: an argument, every value
// but the exclusions, or exactly the listed values.
type position struct {
	argument uint32
	all      bool
	values   []int64
}

func (c *conv) position(typ string, old *apigenold.AuthzSelector, allowArgument bool) (position, bool) {
	if old == nil {
		c.refuse(typ, "", "selector is unset and matches nothing")
		return position{}, false
	}
	if old.ArgumentID != 0 {
		if !allowArgument {
			c.refuse(typ, "argument_id", "an argument outside a template matches nothing")
			return position{}, false
		}
		if old.Wildcard || len(old.Include) > 0 || len(old.Exclude) > 0 {
			c.refuse(typ, "argument_id", "an argument combined with wildcard, include, or exclude has no typed selector form")
			return position{}, false
		}
		return position{argument: c.u32(typ, "argument_id", old.ArgumentID)}, true
	}
	if old.Wildcard {
		return position{all: true, values: distinct(old.Exclude)}, true
	}
	var values []int64
	for _, v := range old.Include {
		if !slices.Contains(old.Exclude, v) && !slices.Contains(values, v) {
			values = append(values, v)
		}
	}
	if len(values) == 0 {
		c.refuse(typ, "include", "the selector matches nothing: no wildcard and an empty or fully excluded include list")
		return position{}, false
	}
	return position{values: values}, true
}

func distinct(vs []int64) []int64 {
	var out []int64
	for _, v := range vs {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// refKind is the single entity kind an entity-type position names, which
// types the entity refs beside it.
func refKind(types position, ok bool) (apigen.AuthzEntityKind, bool) {
	if !ok || types.all || types.argument != 0 || len(types.values) != 1 {
		return apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_UNSPECIFIED, false
	}
	return apigen.AuthzEntityKind(types.values[0]), true
}

func (c *conv) enum32(typ, field string, v int64) int32 {
	if v < 0 || v > math.MaxInt32 {
		c.refuse(typ, field, "value %d is not an enum case", v)
		return 0
	}
	return int32(v)
}

func (c *conv) verbs(typ, field string, vs []int64) []apigen.AuthzVerb {
	out := make([]apigen.AuthzVerb, len(vs))
	for i, v := range vs {
		out[i] = apigen.AuthzVerb(c.enum32(typ, field, v))
	}
	return out
}

func (c *conv) entityKinds(typ, field string, vs []int64) []apigen.AuthzEntityKind {
	out := make([]apigen.AuthzEntityKind, len(vs))
	for i, v := range vs {
		out[i] = apigen.AuthzEntityKind(c.enum32(typ, field, v))
	}
	return out
}

func (c *conv) ids64(typ, field string, vs []int64) []uint64 {
	out := make([]uint64, len(vs))
	for i, v := range vs {
		out[i] = c.id(typ, field, v)
	}
	return out
}

func (c *conv) entityRefs(typ, field string, vs []int64, kind apigen.AuthzEntityKind, kindKnown bool) []apigen.AuthzEntityRef {
	out := make([]apigen.AuthzEntityRef, len(vs))
	for i, v := range vs {
		out[i] = c.entityRef(typ, field, kind, kindKnown, v)
	}
	return out
}

func (c *conv) entityRef(typ, field string, kind apigen.AuthzEntityKind, kindKnown bool, id int64) apigen.AuthzEntityRef {
	if !kindKnown {
		c.refuse(typ, field, "the entity_types position must name exactly one kind to type the entity refs")
		return apigen.AuthzEntityRef{}
	}
	v := c.id(typ, field, id)
	var target apigen.AuthzEntityRefTargetValueOneof
	switch kind {
	case apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SPACE:
		target.Space = &v
	case apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT:
		target.Deployment = &v
	case apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET:
		target.Secret = &v
	case apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_CONFIG:
		target.Config = &v
	case apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ASSET:
		target.Asset = &v
	case apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_NODE:
		target.Node = &v
	case apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_CLUSTER:
		target.SystemConfig = &v
	case apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_USER:
		target.User = &v
	case apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ACCESS:
		c.refuse(typ, field, "an ACCESS entity ref names one of three target kinds (grant template, grant, global rule) and cannot be typed")
	default:
		c.refuse(typ, field, "entity kind %d cannot type an entity ref", kind)
	}
	return apigen.AuthzEntityRef{Target: apigen.AuthzEntityRefTarget{Value: target}}
}

func (c *conv) permissionSelector(typ string, p position) apigen.AuthzPermissionSelector {
	list := apigen.Some(apigen.AuthzVerbList{Values: c.verbs(typ, "permissions", p.values)})
	if p.all {
		return apigen.AuthzPermissionSelector{AllVerbsExcluding: list}
	}
	return apigen.AuthzPermissionSelector{ExactVerbs: list}
}

func (c *conv) spaceSelector(typ string, p position) apigen.AuthzSpaceSelector {
	list := apigen.Some(apigen.SpaceIdList{Values: c.ids64(typ, "spaces", p.values)})
	if p.all {
		return apigen.AuthzSpaceSelector{AllSpacesExcluding: list}
	}
	return apigen.AuthzSpaceSelector{ExactSpaces: list}
}

func (c *conv) entityTypeSelector(typ string, p position) apigen.AuthzEntityTypeSelector {
	list := apigen.Some(apigen.AuthzEntityKindList{Values: c.entityKinds(typ, "entity_types", p.values)})
	if p.all {
		return apigen.AuthzEntityTypeSelector{AllEntityTypesExcluding: list}
	}
	return apigen.AuthzEntityTypeSelector{ExactEntityTypes: list}
}

func (c *conv) entityRefSelector(typ string, p position, kind apigen.AuthzEntityKind, kindKnown bool) apigen.AuthzEntityRefSelector {
	list := apigen.Some(apigen.AuthzEntityRefList{Values: c.entityRefs(typ, "entity_refs", p.values, kind, kindKnown)})
	if p.all {
		return apigen.AuthzEntityRefSelector{AllEntityRefsExcluding: list}
	}
	return apigen.AuthzEntityRefSelector{ExactEntityRefs: list}
}

func (c *conv) selector(typ string, permissions, spaces, entityTypes, entityRefs *apigenold.AuthzSelector) apigen.AuthzSelector {
	pp, pok := c.position(typ+".permissions", permissions, false)
	sp, sok := c.position(typ+".spaces", spaces, false)
	tp, tok := c.position(typ+".entity_types", entityTypes, false)
	rp, rok := c.position(typ+".entity_refs", entityRefs, false)
	var out apigen.AuthzSelector
	if pok {
		out.Permissions = c.permissionSelector(typ, pp)
	}
	if sok {
		out.Spaces = c.spaceSelector(typ, sp)
	}
	if tok {
		out.EntityTypes = c.entityTypeSelector(typ, tp)
	}
	if rok {
		kind, known := refKind(tp, tok)
		out.EntityRefs = c.entityRefSelector(typ, rp, kind, known)
	}
	return out
}

func (c *conv) rule(typ string, old *apigenold.AuthzRule) apigen.AuthzRule {
	if old == nil {
		c.refuse(typ, "", "unset rule")
		return apigen.AuthzRule{}
	}
	return apigen.AuthzRule{
		Effect:   allowEffect(old.DelegationAllowed),
		Selector: c.selector(typ, old.Permissions, old.Spaces, old.EntityTypes, old.EntityRefs),
	}
}

func (c *conv) noteArgument(typ string, kinds map[uint32]apigen.AuthzArgumentKind, id uint32, kind apigen.AuthzArgumentKind) {
	if prev, ok := kinds[id]; ok && prev != kind {
		c.refuse(typ, "argument_id", "argument %d is used as kind %d and as kind %d", id, prev, kind)
		return
	}
	kinds[id] = kind
}

func (c *conv) templateRule(typ string, old *apigenold.AuthzRule, kinds map[uint32]apigen.AuthzArgumentKind) apigen.AuthzTemplateRule {
	pp, pok := c.position(typ+".permissions", old.Permissions, true)
	sp, sok := c.position(typ+".spaces", old.Spaces, true)
	tp, tok := c.position(typ+".entity_types", old.EntityTypes, true)
	rp, rok := c.position(typ+".entity_refs", old.EntityRefs, true)
	var sel apigen.AuthzTemplateSelector
	if pok {
		if pp.argument != 0 {
			c.noteArgument(typ, kinds, pp.argument, apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_PERMISSION)
			sel.Permissions.Value.Argument = &apigen.AuthzArgument{ArgumentID: pp.argument}
		} else {
			s := c.permissionSelector(typ, pp)
			sel.Permissions.Value.Selector = &s
		}
	}
	if sok {
		if sp.argument != 0 {
			c.noteArgument(typ, kinds, sp.argument, apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_SPACE)
			sel.Spaces.Value.Argument = &apigen.AuthzArgument{ArgumentID: sp.argument}
		} else {
			s := c.spaceSelector(typ, sp)
			sel.Spaces.Value.Selector = &s
		}
	}
	if tok {
		if tp.argument != 0 {
			c.noteArgument(typ, kinds, tp.argument, apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_ENTITY_TYPE)
			sel.EntityTypes.Value.Argument = &apigen.AuthzArgument{ArgumentID: tp.argument}
		} else {
			s := c.entityTypeSelector(typ, tp)
			sel.EntityTypes.Value.Selector = &s
		}
	}
	if rok {
		if rp.argument != 0 {
			c.noteArgument(typ, kinds, rp.argument, apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_ENTITY_REF)
			sel.EntityRefs.Value.Argument = &apigen.AuthzArgument{ArgumentID: rp.argument}
		} else {
			kind, known := refKind(tp, tok)
			s := c.entityRefSelector(typ, rp, kind, known)
			sel.EntityRefs.Value.Selector = &s
		}
	}
	return apigen.AuthzTemplateRule{Effect: allowEffect(old.DelegationAllowed), Selector: sel}
}

// AuthzGlobalRule maps the old deny flag to the effect union: a deny keeps
// delegated_only, an allow keeps delegation_allowed; the flag the old
// evaluator ignored for that mode is dropped.
func AuthzGlobalRule(old *apigenold.AuthzGlobalRule) (*apigen.AuthzGlobalRule, error) {
	c := &conv{}
	if old == nil {
		c.refuse("AuthzGlobalRule", "", "nil payload")
		return checked("AuthzGlobalRule", (*apigen.AuthzGlobalRule)(nil), c.err)
	}
	out := &apigen.AuthzGlobalRule{ID: c.id("AuthzGlobalRule", "id", old.ID), Name: old.Name}
	if old.Spec == nil {
		c.refuse("AuthzGlobalRule", "spec", "unset")
		return checked("AuthzGlobalRule", out, c.err)
	}
	s := old.Spec
	effect := allowEffect(s.DelegationAllowed)
	if s.Deny {
		effect = apigen.AuthzEffect{Value: apigen.AuthzEffectValueOneof{Deny: &apigen.AuthzDeny{DelegatedOnly: s.DelegatedOnly}}}
	}
	out.Rule = apigen.AuthzRule{Effect: effect, Selector: c.selector("AuthzGlobalRuleSpec", s.Permissions, s.Spaces, s.EntityTypes, s.EntityRefs)}
	return checked("AuthzGlobalRule", out, c.err)
}

// AuthzGrant keeps the old precedence: a rule in the spec is a rule grant,
// otherwise the template id names a template grant whose bindings are typed
// by the converted template's argument kinds; an entity-ref binding takes its
// kind from the entity-type position of the rules that use the argument.
func AuthzGrant(old *apigenold.AuthzGrant, templates TemplateLookup) (*apigen.AuthzGrant, error) {
	c := &conv{}
	if old == nil {
		c.refuse("AuthzGrant", "", "nil payload")
		return checked("AuthzGrant", (*apigen.AuthzGrant)(nil), c.err)
	}
	out := &apigen.AuthzGrant{ID: c.id("AuthzGrant", "id", old.ID), UserID: c.id("AuthzGrant", "user_id", old.UserID)}
	switch {
	case old.Spec != nil && old.Spec.Rule != nil:
		r := c.rule("AuthzGrantSpec.rule", old.Spec.Rule)
		out.Grant = apigen.AuthzGrantSource{Value: apigen.AuthzGrantSourceValueOneof{Rule: &r}}
	case old.TemplateID != 0:
		templateID := c.id("AuthzGrant", "template_id", old.TemplateID)
		if templates == nil {
			c.refuse("AuthzGrant", "template_id", "no template lookup to type the bindings")
			break
		}
		t, ok := templates(templateID)
		if !ok || t == nil {
			c.refuse("AuthzGrant", "template_id", "template %d is unknown", templateID)
			break
		}
		var args []*apigenold.AuthzArgumentBinding
		if old.Spec != nil {
			args = old.Spec.Args
		}
		out.Grant = apigen.AuthzGrantSource{Value: apigen.AuthzGrantSourceValueOneof{Template: &apigen.AuthzTemplateGrant{
			TemplateID: templateID, Args: c.bindings(t, args),
		}}}
	default:
		c.refuse("AuthzGrant", "", "neither a rule nor a template id is set")
	}
	return checked("AuthzGrant", out, c.err)
}

func (c *conv) bindings(t *apigen.AuthzGrantTemplate, old []*apigenold.AuthzArgumentBinding) []apigen.AuthzArgumentBinding {
	var out []apigen.AuthzArgumentBinding
	for _, b := range old {
		if b == nil {
			c.refuse("AuthzGrantSpec", "args", "nil entry")
			continue
		}
		id := c.u32("AuthzArgumentBinding", "argument_id", b.ArgumentID)
		kind, ok := templateArgumentKind(t, id)
		if !ok {
			c.refuse("AuthzArgumentBinding", "argument_id", "template %d has no argument %d", t.ID, id)
			continue
		}
		var v apigen.AuthzArgumentValuesValueOneof
		switch kind {
		case apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_PERMISSION:
			v.Permissions = &apigen.AuthzPermissionValues{Values: c.verbs("AuthzArgumentBinding", "values", b.Values)}
		case apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_SPACE:
			v.Spaces = &apigen.AuthzSpaceValues{Values: c.ids64("AuthzArgumentBinding", "values", b.Values)}
		case apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_ENTITY_TYPE:
			v.EntityTypes = &apigen.AuthzEntityValues{Values: c.entityKinds("AuthzArgumentBinding", "values", b.Values)}
		case apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_ENTITY_REF:
			refKind, known := templateRefKind(t, id)
			v.EntityRefs = &apigen.AuthzReferenceValues{Values: c.entityRefs("AuthzArgumentBinding", "values", b.Values, refKind, known)}
		default:
			c.refuse("AuthzArgumentBinding", "argument_id", "template %d argument %d has kind %d", t.ID, id, kind)
		}
		out = append(out, apigen.AuthzArgumentBinding{ArgumentID: id, Values: apigen.AuthzArgumentValues{Value: v}})
	}
	return out
}

func templateArgumentKind(t *apigen.AuthzGrantTemplate, id uint32) (apigen.AuthzArgumentKind, bool) {
	for _, a := range t.Spec.Arguments {
		if a.ID == id {
			return a.Kind, true
		}
	}
	return apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_UNSPECIFIED, false
}

func templateRefKind(t *apigen.AuthzGrantTemplate, id uint32) (apigen.AuthzEntityKind, bool) {
	var kind apigen.AuthzEntityKind
	found := false
	for _, r := range t.Spec.Rules {
		a := r.Selector.EntityRefs.Value.Argument
		if a == nil || a.ArgumentID != id {
			continue
		}
		s := r.Selector.EntityTypes.Value.Selector
		if s == nil || !s.ExactEntityTypes.Present || len(s.ExactEntityTypes.Value.Values) != 1 {
			return apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_UNSPECIFIED, false
		}
		k := s.ExactEntityTypes.Value.Values[0]
		if found && k != kind {
			return apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_UNSPECIFIED, false
		}
		kind, found = k, true
	}
	return kind, found
}
