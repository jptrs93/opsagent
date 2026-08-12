package authz

import (
	"fmt"
	"regexp"

	"github.com/jptrs93/opsagent/backend/apigen"
)

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

const maxNameLen = 64

type invalidError struct{ msg string }

func (e *invalidError) Error() string { return e.msg }

func (e *invalidError) Is(target error) bool { return target == ErrInvalid }

func invalidf(format string, args ...any) error {
	return &invalidError{msg: fmt.Sprintf(format, args...)}
}

func validateTemplateName(name string) error {
	if name == "" || len(name) > maxNameLen || !nameRe.MatchString(name) {
		return invalidf("authz: invalid rule template name %q", name)
	}
	return nil
}

func validVerb(v int64) bool {
	return v >= int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW) && v <= int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE)
}

func validEntityType(v int64) bool {
	return v >= int64(apigen.AuthzEntity_AUTHZ_ENTITY_SPACE) && v <= int64(apigen.AuthzEntity_AUTHZ_ENTITY_ACCESS)
}

func validSpaceID(v int64) bool { return v >= 0 && v <= 65535 }

func validEntityRef(v int64) bool { return v > 0 }

type position struct {
	name     string
	valid    func(int64) bool
	selector func(*apigen.AuthzRule) *apigen.AuthzSelector
}

var positions = []position{
	{"spaces", validSpaceID, func(r *apigen.AuthzRule) *apigen.AuthzSelector { return r.Spaces }},
	{"entity_types", validEntityType, func(r *apigen.AuthzRule) *apigen.AuthzSelector { return r.EntityTypes }},
	{"entity_refs", validEntityRef, func(r *apigen.AuthzRule) *apigen.AuthzSelector { return r.EntityRefs }},
	{"permissions", validVerb, func(r *apigen.AuthzRule) *apigen.AuthzSelector { return r.Permissions }},
}

func validateTemplate(name string, t *apigen.AuthzRuleTemplate) error {
	if err := validateTemplateName(name); err != nil {
		return err
	}
	if t == nil {
		return invalidf("authz: rule template content is empty")
	}
	declared := make(map[int64]bool, len(t.Arguments))
	argNames := make(map[string]bool, len(t.Arguments))
	for _, a := range t.Arguments {
		if a == nil || a.ID <= 0 {
			return invalidf("authz: invalid argument id")
		}
		if declared[a.ID] {
			return invalidf("authz: duplicate argument id %d", a.ID)
		}
		if a.Name == "" || len(a.Name) > maxNameLen || !nameRe.MatchString(a.Name) {
			return invalidf("authz: invalid argument name %q", a.Name)
		}
		if argNames[a.Name] {
			return invalidf("authz: duplicate argument name %q", a.Name)
		}
		declared[a.ID] = true
		argNames[a.Name] = true
	}
	if err := validateRules(t.Rules, true); err != nil {
		return err
	}
	kinds := make(map[int64]string)
	for i, rule := range t.Rules {
		for _, pos := range positions {
			sel := pos.selector(rule)
			if sel == nil || sel.ArgumentID == 0 {
				continue
			}
			if !declared[sel.ArgumentID] {
				return invalidf("authz: rule %d %s: undeclared argument %d", i, pos.name, sel.ArgumentID)
			}
			if kind, ok := kinds[sel.ArgumentID]; ok && kind != pos.name {
				return invalidf("authz: argument %d used in both %s and %s positions", sel.ArgumentID, kind, pos.name)
			}
			kinds[sel.ArgumentID] = pos.name
		}
	}
	for id := range declared {
		if _, ok := kinds[id]; !ok {
			return invalidf("authz: argument %d is declared but unused", id)
		}
	}
	return nil
}

func templateSignature(t *apigen.AuthzRuleTemplate) map[int64]func(int64) bool {
	sig := make(map[int64]func(int64) bool)
	if t == nil {
		return sig
	}
	for _, rule := range t.Rules {
		if rule == nil {
			continue
		}
		for _, pos := range positions {
			if sel := pos.selector(rule); sel != nil && sel.ArgumentID != 0 {
				sig[sel.ArgumentID] = pos.valid
			}
		}
	}
	return sig
}

func validateRules(rules []*apigen.AuthzRule, allowArguments bool) error {
	if len(rules) == 0 {
		return invalidf("authz: at least one rule is required")
	}
	for i, rule := range rules {
		if rule == nil {
			return invalidf("authz: rule %d is empty", i)
		}
		for _, pos := range positions {
			if err := validateSelector(pos.selector(rule), pos.valid, allowArguments); err != nil {
				return fmt.Errorf("authz: rule %d %s: %w", i, pos.name, err)
			}
		}
	}
	return nil
}

func validateSelector(sel *apigen.AuthzSelector, valid func(int64) bool, allowArguments bool) error {
	if sel == nil {
		return invalidf("selector is missing")
	}
	if sel.ArgumentID != 0 {
		if !allowArguments {
			return invalidf("arguments are only valid in rule template rules")
		}
		if sel.ArgumentID < 0 {
			return invalidf("invalid argument id %d", sel.ArgumentID)
		}
	}
	if !sel.Wildcard && sel.ArgumentID == 0 && len(sel.Include) == 0 {
		return invalidf("selector matches nothing")
	}
	for _, v := range sel.Include {
		if !valid(v) {
			return invalidf("invalid value %d", v)
		}
	}
	for _, v := range sel.Exclude {
		if !valid(v) {
			return invalidf("invalid excluded value %d", v)
		}
	}
	return nil
}

func validateGlobalRule(name string, r *apigen.AuthzGlobalRule) error {
	if r == nil {
		return invalidf("authz: global rule is empty")
	}
	if name == "" || len(name) > maxNameLen || !nameRe.MatchString(name) {
		return invalidf("authz: invalid global rule name %q", name)
	}
	selectors := []struct {
		name  string
		sel   *apigen.AuthzSelector
		valid func(int64) bool
	}{
		{"spaces", r.Spaces, validSpaceID},
		{"entity_types", r.EntityTypes, validEntityType},
		{"entity_refs", r.EntityRefs, validEntityRef},
		{"permissions", r.Permissions, validVerb},
	}
	for _, item := range selectors {
		if err := validateSelector(item.sel, item.valid, false); err != nil {
			return fmt.Errorf("authz: global rule %s: %w", item.name, err)
		}
	}
	for _, v := range r.EntityTypes.Include {
		if v == int64(apigen.AuthzEntity_AUTHZ_ENTITY_ACCESS) {
			return invalidf("authz: global rules cannot target the access entity")
		}
	}
	return nil
}

func validateArgs(t *apigen.AuthzRuleTemplateRecord, bindings []*apigen.AuthzArgumentBinding) error {
	sig := templateSignature(t.Template)
	if len(sig) == 0 {
		if len(bindings) != 0 {
			return invalidf("authz: rule template %s takes no arguments", t.Name)
		}
		return nil
	}
	seen := make(map[int64]bool, len(bindings))
	for _, b := range bindings {
		if b == nil || b.ArgumentID == 0 {
			return invalidf("authz: binding is missing an argument id")
		}
		if seen[b.ArgumentID] {
			return invalidf("authz: duplicate binding for argument %d", b.ArgumentID)
		}
		valid, ok := sig[b.ArgumentID]
		if !ok {
			return invalidf("authz: rule template %s has no argument %d", t.Name, b.ArgumentID)
		}
		if len(b.Values) == 0 {
			return invalidf("authz: argument %d requires values", b.ArgumentID)
		}
		for _, v := range b.Values {
			if !valid(v) {
				return invalidf("authz: argument %d: invalid value %d", b.ArgumentID, v)
			}
		}
		seen[b.ArgumentID] = true
	}
	if len(seen) != len(sig) {
		return invalidf("authz: rule template %s requires bindings for all %d arguments", t.Name, len(sig))
	}
	return nil
}
