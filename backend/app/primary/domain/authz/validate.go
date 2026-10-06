package authz

import (
	"fmt"
	"regexp"
	"slices"

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
		return invalidf("authz: invalid grant template name %q", name)
	}
	return nil
}

func validVerb(v apigen.AuthzVerb) bool {
	return v >= apigen.AuthzVerb_AUTHZ_VERB_CREATE && v <= apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_NETWORK
}

func validEntityKind(v apigen.AuthzEntityKind) bool {
	return v >= apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SPACE && v <= apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ACCESS
}

func validArgumentKind(v apigen.AuthzArgumentKind) bool {
	return v >= apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_PERMISSION && v <= apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_ENTITY_REF
}

func validSpaceID(v uint64) bool { return v <= 65535 }

func validEntityRef(ref apigen.AuthzEntityRef) bool {
	_, id, ok := EntityRefTarget(ref)
	return ok && id > 0
}

func validateVerbs(vs []apigen.AuthzVerb) error {
	for _, v := range vs {
		if !validVerb(v) {
			return invalidf("invalid value %d", v)
		}
	}
	return nil
}

func validateSpaces(ids []uint64) error {
	for _, v := range ids {
		if !validSpaceID(v) {
			return invalidf("invalid value %d", v)
		}
	}
	return nil
}

func validateEntityKinds(ks []apigen.AuthzEntityKind) error {
	for _, v := range ks {
		if !validEntityKind(v) {
			return invalidf("invalid value %d", v)
		}
	}
	return nil
}

func validateEntityRefs(refs []apigen.AuthzEntityRef) error {
	for _, ref := range refs {
		if !validEntityRef(ref) {
			return invalidf("invalid entity ref")
		}
	}
	return nil
}

func oneOfTwo(exact, allExcluding bool, exactLen int) error {
	switch {
	case exact == allExcluding:
		return invalidf("selector must set exactly one of exact and all_excluding")
	case exact && exactLen == 0:
		return invalidf("selector matches nothing")
	}
	return nil
}

func validatePermissionSelector(sel apigen.AuthzPermissionSelector) error {
	if err := oneOfTwo(sel.ExactVerbs.Present, sel.AllVerbsExcluding.Present, len(sel.ExactVerbs.Value.Values)); err != nil {
		return err
	}
	if err := validateVerbs(sel.ExactVerbs.Value.Values); err != nil {
		return err
	}
	return validateVerbs(sel.AllVerbsExcluding.Value.Values)
}

func validateSpaceSelector(sel apigen.AuthzSpaceSelector) error {
	if err := oneOfTwo(sel.ExactSpaces.Present, sel.AllSpacesExcluding.Present, len(sel.ExactSpaces.Value.Values)); err != nil {
		return err
	}
	if err := validateSpaces(sel.ExactSpaces.Value.Values); err != nil {
		return err
	}
	return validateSpaces(sel.AllSpacesExcluding.Value.Values)
}

func validateEntityTypeSelector(sel apigen.AuthzEntityTypeSelector) error {
	if err := oneOfTwo(sel.ExactEntityTypes.Present, sel.AllEntityTypesExcluding.Present, len(sel.ExactEntityTypes.Value.Values)); err != nil {
		return err
	}
	if err := validateEntityKinds(sel.ExactEntityTypes.Value.Values); err != nil {
		return err
	}
	return validateEntityKinds(sel.AllEntityTypesExcluding.Value.Values)
}

func validateEntityRefSelector(sel apigen.AuthzEntityRefSelector) error {
	if err := oneOfTwo(sel.ExactEntityRefs.Present, sel.AllEntityRefsExcluding.Present, len(sel.ExactEntityRefs.Value.Values)); err != nil {
		return err
	}
	if err := validateEntityRefs(sel.ExactEntityRefs.Value.Values); err != nil {
		return err
	}
	return validateEntityRefs(sel.AllEntityRefsExcluding.Value.Values)
}

func validateSelector(sel apigen.AuthzSelector) error {
	if err := validateSpaceSelector(sel.Spaces); err != nil {
		return fmt.Errorf("spaces: %w", err)
	}
	if err := validateEntityTypeSelector(sel.EntityTypes); err != nil {
		return fmt.Errorf("entity_types: %w", err)
	}
	if err := validateEntityRefSelector(sel.EntityRefs); err != nil {
		return fmt.Errorf("entity_refs: %w", err)
	}
	if err := validatePermissionSelector(sel.Permissions); err != nil {
		return fmt.Errorf("permissions: %w", err)
	}
	return nil
}

func validateEffect(e apigen.AuthzEffect) error {
	if e.Value.Validate() != nil {
		return invalidf("effect must be exactly one of allow and deny")
	}
	return nil
}

func isDeny(e apigen.AuthzEffect) bool { return e.Value.Deny != nil }

// The access carve-out protects the repair path from denies; an allow that
// targets access only adds, so it is not restricted.
func deniesAccess(e apigen.AuthzEffect, sel apigen.AuthzEntityTypeSelector) bool {
	return isDeny(e) && slices.Contains(sel.ExactEntityTypes.Value.Values, apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ACCESS)
}

func validateRule(r *apigen.AuthzRule) error {
	if r == nil {
		return invalidf("rule is empty")
	}
	if err := validateEffect(r.Effect); err != nil {
		return err
	}
	if err := validateSelector(r.Selector); err != nil {
		return err
	}
	if deniesAccess(r.Effect, r.Selector.EntityTypes) {
		return invalidf("rules cannot deny the access entity")
	}
	return nil
}

func validateGlobalRule(name string, r *apigen.AuthzRule) error {
	if r == nil {
		return invalidf("authz: global rule is empty")
	}
	if name == "" || len(name) > maxNameLen || !nameRe.MatchString(name) {
		return invalidf("authz: invalid global rule name %q", name)
	}
	if err := validateRule(r); err != nil {
		return fmt.Errorf("authz: global rule: %w", err)
	}
	return nil
}

type argumentUse struct {
	id   uint32
	kind apigen.AuthzArgumentKind
	name string
}

func templateSelectorUses(sel apigen.AuthzTemplateSelector) ([]argumentUse, error) {
	var uses []argumentUse
	switch v := sel.Spaces.Value; {
	case v.Argument != nil:
		uses = append(uses, argumentUse{v.Argument.ArgumentID, apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_SPACE, "spaces"})
	case v.Selector != nil:
		if err := validateSpaceSelector(*v.Selector); err != nil {
			return nil, fmt.Errorf("spaces: %w", err)
		}
	default:
		return nil, invalidf("spaces: selector is missing")
	}
	switch v := sel.EntityTypes.Value; {
	case v.Argument != nil:
		uses = append(uses, argumentUse{v.Argument.ArgumentID, apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_ENTITY_TYPE, "entity_types"})
	case v.Selector != nil:
		if err := validateEntityTypeSelector(*v.Selector); err != nil {
			return nil, fmt.Errorf("entity_types: %w", err)
		}
	default:
		return nil, invalidf("entity_types: selector is missing")
	}
	switch v := sel.EntityRefs.Value; {
	case v.Argument != nil:
		uses = append(uses, argumentUse{v.Argument.ArgumentID, apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_ENTITY_REF, "entity_refs"})
	case v.Selector != nil:
		if err := validateEntityRefSelector(*v.Selector); err != nil {
			return nil, fmt.Errorf("entity_refs: %w", err)
		}
	default:
		return nil, invalidf("entity_refs: selector is missing")
	}
	switch v := sel.Permissions.Value; {
	case v.Argument != nil:
		uses = append(uses, argumentUse{v.Argument.ArgumentID, apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_PERMISSION, "permissions"})
	case v.Selector != nil:
		if err := validatePermissionSelector(*v.Selector); err != nil {
			return nil, fmt.Errorf("permissions: %w", err)
		}
	default:
		return nil, invalidf("permissions: selector is missing")
	}
	return uses, nil
}

func validateTemplate(name string, t *apigen.AuthzGrantTemplateSpec) error {
	if err := validateTemplateName(name); err != nil {
		return err
	}
	if t == nil {
		return invalidf("authz: grant template content is empty")
	}
	declared := make(map[uint32]apigen.AuthzArgumentKind, len(t.Arguments))
	argNames := make(map[string]bool, len(t.Arguments))
	for _, a := range t.Arguments {
		if a.ID == 0 {
			return invalidf("authz: invalid argument id")
		}
		if _, ok := declared[a.ID]; ok {
			return invalidf("authz: duplicate argument id %d", a.ID)
		}
		if a.Name == "" || len(a.Name) > maxNameLen || !nameRe.MatchString(a.Name) {
			return invalidf("authz: invalid argument name %q", a.Name)
		}
		if argNames[a.Name] {
			return invalidf("authz: duplicate argument name %q", a.Name)
		}
		if !validArgumentKind(a.Kind) {
			return invalidf("authz: argument %d has an invalid kind", a.ID)
		}
		declared[a.ID] = a.Kind
		argNames[a.Name] = true
	}
	if len(t.Rules) == 0 {
		return invalidf("authz: at least one rule is required")
	}
	used := make(map[uint32]bool, len(declared))
	for i := range t.Rules {
		rule := &t.Rules[i]
		if err := validateEffect(rule.Effect); err != nil {
			return fmt.Errorf("authz: rule %d: %w", i, err)
		}
		uses, err := templateSelectorUses(rule.Selector)
		if err != nil {
			return fmt.Errorf("authz: rule %d %w", i, err)
		}
		for _, u := range uses {
			kind, ok := declared[u.id]
			if !ok {
				return invalidf("authz: rule %d %s: undeclared argument %d", i, u.name, u.id)
			}
			if kind != u.kind {
				return invalidf("authz: rule %d %s: argument %d is declared with another kind", i, u.name, u.id)
			}
			used[u.id] = true
		}
		if s := rule.Selector.EntityTypes.Value.Selector; s != nil && deniesAccess(rule.Effect, *s) {
			return invalidf("authz: rule %d: rules cannot deny the access entity", i)
		}
	}
	for id := range declared {
		if !used[id] {
			return invalidf("authz: argument %d is declared but unused", id)
		}
	}
	return nil
}

func denyEntityTypeArguments(t *apigen.AuthzGrantTemplateSpec) map[uint32]bool {
	out := make(map[uint32]bool)
	for i := range t.Rules {
		rule := &t.Rules[i]
		if a := rule.Selector.EntityTypes.Value.Argument; a != nil && isDeny(rule.Effect) {
			out[a.ArgumentID] = true
		}
	}
	return out
}

func validateBindingValues(kind apigen.AuthzArgumentKind, v apigen.AuthzArgumentValuesValueOneof) error {
	switch kind {
	case apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_PERMISSION:
		if v.Permissions == nil {
			return invalidf("requires permission values")
		}
		if len(v.Permissions.Values) == 0 {
			return invalidf("requires values")
		}
		return validateVerbs(v.Permissions.Values)
	case apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_SPACE:
		if v.Spaces == nil {
			return invalidf("requires space values")
		}
		if len(v.Spaces.Values) == 0 {
			return invalidf("requires values")
		}
		return validateSpaces(v.Spaces.Values)
	case apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_ENTITY_TYPE:
		if v.EntityTypes == nil {
			return invalidf("requires entity type values")
		}
		if len(v.EntityTypes.Values) == 0 {
			return invalidf("requires values")
		}
		return validateEntityKinds(v.EntityTypes.Values)
	case apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_ENTITY_REF:
		if v.EntityRefs == nil {
			return invalidf("requires entity ref values")
		}
		if len(v.EntityRefs.Values) == 0 {
			return invalidf("requires values")
		}
		return validateEntityRefs(v.EntityRefs.Values)
	}
	return invalidf("invalid kind")
}

func validateArgs(t *apigen.AuthzGrantTemplate, bindings []apigen.AuthzArgumentBinding) error {
	if len(t.Spec.Arguments) == 0 {
		if len(bindings) != 0 {
			return invalidf("authz: grant template %s takes no arguments", t.Name)
		}
		return nil
	}
	declared := make(map[uint32]apigen.AuthzArgumentKind, len(t.Spec.Arguments))
	for _, a := range t.Spec.Arguments {
		declared[a.ID] = a.Kind
	}
	denyKinds := denyEntityTypeArguments(&t.Spec)
	seen := make(map[uint32]bool, len(bindings))
	for i := range bindings {
		b := &bindings[i]
		if b.ArgumentID == 0 {
			return invalidf("authz: binding is missing an argument id")
		}
		if seen[b.ArgumentID] {
			return invalidf("authz: duplicate binding for argument %d", b.ArgumentID)
		}
		kind, ok := declared[b.ArgumentID]
		if !ok {
			return invalidf("authz: grant template %s has no argument %d", t.Name, b.ArgumentID)
		}
		if err := validateBindingValues(kind, b.Values.Value); err != nil {
			return fmt.Errorf("authz: argument %d: %w", b.ArgumentID, err)
		}
		if denyKinds[b.ArgumentID] && slices.Contains(b.Values.Value.EntityTypes.Values, apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ACCESS) {
			return invalidf("authz: argument %d: rules cannot deny the access entity", b.ArgumentID)
		}
		seen[b.ArgumentID] = true
	}
	if len(seen) != len(declared) {
		return invalidf("authz: grant template %s requires bindings for all %d arguments", t.Name, len(declared))
	}
	return nil
}
