package authz

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func newTestStore(t *testing.T) *state.Service {
	t.Helper()
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mustOpen(t *testing.T, store *state.Service) *Service {
	t.Helper()
	s, err := Open(store)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func deny(delegatedOnly bool) apigen.AuthzEffect {
	return apigen.AuthzEffect{Value: apigen.AuthzEffectValueOneof{Deny: &apigen.AuthzDeny{DelegatedOnly: delegatedOnly}}}
}

func anySelector() apigen.AuthzSelector {
	return apigen.AuthzSelector{
		Permissions: allVerbsExcluding(),
		Spaces:      allSpacesExcluding(),
		EntityTypes: allEntityTypesExcluding(),
		EntityRefs:  allEntityRefs(),
	}
}

func allowRule(delegationAllowed bool, sel apigen.AuthzSelector) *apigen.AuthzRule {
	return &apigen.AuthzRule{Effect: allow(delegationAllowed), Selector: sel}
}

func denyRule(delegatedOnly bool, sel apigen.AuthzSelector) *apigen.AuthzRule {
	return &apigen.AuthzRule{Effect: deny(delegatedOnly), Selector: sel}
}

func anyTemplateSelector() apigen.AuthzTemplateSelector {
	return apigen.AuthzTemplateSelector{
		Permissions: templatePermissions(allVerbsExcluding()),
		Spaces:      templateSpaces(allSpacesExcluding()),
		EntityTypes: templateEntityTypes(allEntityTypesExcluding()),
		EntityRefs:  templateEntityRefs(allEntityRefs()),
	}
}

func spacesArgTemplateSelector(argID uint32) apigen.AuthzTemplateSelector {
	sel := anyTemplateSelector()
	sel.Spaces = templateSpacesArgument(argID)
	return sel
}

func templateRule(effect apigen.AuthzEffect, sel apigen.AuthzTemplateSelector) apigen.AuthzTemplateRule {
	return apigen.AuthzTemplateRule{Effect: effect, Selector: sel}
}

func spaceArg(id uint32, name string) apigen.AuthzTemplateArgument {
	return apigen.AuthzTemplateArgument{ID: id, Name: name, Kind: apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_SPACE}
}

func spacesTemplate(argID uint32, rules ...apigen.AuthzTemplateRule) *apigen.AuthzGrantTemplateSpec {
	return &apigen.AuthzGrantTemplateSpec{
		Arguments: []apigen.AuthzTemplateArgument{spaceArg(argID, "spaces")},
		Rules:     rules,
	}
}

func spaceBinding(argID uint32, ids ...uint64) apigen.AuthzArgumentBinding {
	return apigen.AuthzArgumentBinding{
		ArgumentID: argID,
		Values:     apigen.AuthzArgumentValues{Value: apigen.AuthzArgumentValuesValueOneof{Spaces: &apigen.AuthzSpaceValues{Values: ids}}},
	}
}

func templateSource(templateID uint64, args ...apigen.AuthzArgumentBinding) apigen.AuthzGrantSource {
	return apigen.AuthzGrantSource{Value: apigen.AuthzGrantSourceValueOneof{Template: &apigen.AuthzTemplateGrant{TemplateID: templateID, Args: args}}}
}

func templateGrant(userID, templateID uint64, args ...apigen.AuthzArgumentBinding) *apigen.AuthzGrant {
	return &apigen.AuthzGrant{UserID: userID, Grant: templateSource(templateID, args...)}
}

func ruleGrant(userID uint64, rule *apigen.AuthzRule) *apigen.AuthzGrant {
	return &apigen.AuthzGrant{UserID: userID, Grant: apigen.AuthzGrantSource{Value: apigen.AuthzGrantSourceValueOneof{Rule: rule}}}
}

func deploymentRef(id uint64) apigen.AuthzEntityRef {
	return apigen.AuthzEntityRef{Target: apigen.AuthzEntityRefTarget{Value: apigen.AuthzEntityRefTargetValueOneof{Deployment: &id}}}
}

func secretRef(id uint64) apigen.AuthzEntityRef {
	return apigen.AuthzEntityRef{Target: apigen.AuthzEntityRefTarget{Value: apigen.AuthzEntityRefTargetValueOneof{Secret: &id}}}
}

func exactRefs(refs ...apigen.AuthzEntityRef) apigen.AuthzEntityRefSelector {
	return apigen.AuthzEntityRefSelector{ExactEntityRefs: apigen.Some(apigen.AuthzEntityRefList{Values: refs})}
}

func viewDeployment(space uint64) RequestedAccess {
	return RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_VIEW,
		SpaceID:    space,
		EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT,
		EntityID:   7,
	}
}

func TestBuiltinsSeededAndListed(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	templates := s.GrantTemplates()
	if len(templates) != 2 {
		t.Fatalf("expected 2 builtin templates, got %d", len(templates))
	}
	if templates[0].Name != "cluster_admin" || !templates[0].Builtin || templates[0].ID != ClusterAdminTemplateID {
		t.Fatalf("unexpected first template: %+v", templates[0])
	}
	if templates[1].Name != "space_admin" || templates[1].ID != SpaceAdminTemplateID {
		t.Fatalf("unexpected second template: %+v", templates[1])
	}
}

func TestNoGrantsDeniesEverything(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	if s.HasAccess(1, viewDeployment(0)) {
		t.Fatal("user with no grants should have no access")
	}
}

func TestClusterAdminGrant(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	if _, err := s.CreateGrant(templateGrant(1, ClusterAdminTemplateID), 0); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	for _, req := range []RequestedAccess{
		viewDeployment(0),
		viewDeployment(9),
		{Verb: apigen.AuthzVerb_AUTHZ_VERB_REVEAL, SpaceID: 3, EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET, EntityID: 12},
		{Verb: apigen.AuthzVerb_AUTHZ_VERB_CREATE, SpaceID: 1, EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT},
	} {
		if !s.HasAccess(1, req) {
			t.Fatalf("cluster_admin should allow %+v", req)
		}
	}
	if s.HasAccess(2, viewDeployment(1)) {
		t.Fatal("grant must not leak to another user")
	}
	if s.HasAccess(1, RequestedAccess{SpaceID: 1, EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT}) {
		t.Fatal("unknown verb must never match")
	}
	if s.HasAccess(1, RequestedAccess{Verb: apigen.AuthzVerb_AUTHZ_VERB_VIEW, SpaceID: 1}) {
		t.Fatal("unknown entity type must never match")
	}
}

func TestSpaceAdminArgumentBinding(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	if _, err := s.CreateGrant(templateGrant(1, SpaceAdminTemplateID, spaceBinding(1, 2, 3)), 0); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if !s.HasAccess(1, viewDeployment(2)) || !s.HasAccess(1, viewDeployment(3)) {
		t.Fatal("bound spaces should be allowed")
	}
	if s.HasAccess(1, viewDeployment(0)) || s.HasAccess(1, viewDeployment(4)) {
		t.Fatal("unbound spaces should be denied")
	}
	delegated := viewDeployment(2)
	delegated.Delegated = true
	if !s.HasAccess(1, delegated) {
		t.Fatal("delegated view in a bound space should be allowed")
	}
	reveal := RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_REVEAL,
		SpaceID:    2,
		EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET,
		EntityID:   4,
	}
	if !s.HasAccess(1, reveal) {
		t.Fatal("direct reveal in a bound space should be allowed")
	}
	reveal.Delegated = true
	if s.HasAccess(1, reveal) {
		t.Fatal("delegated reveal must be denied")
	}
	logs := RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_VIEW_LOGS,
		SpaceID:    2,
		EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT,
		EntityID:   7,
	}
	if !s.HasAccess(1, logs) {
		t.Fatal("direct view_logs in a bound space should be allowed")
	}
	logs.Delegated = true
	if s.HasAccess(1, logs) {
		t.Fatal("delegated view_logs must be denied")
	}
}

func TestDirectRuleWithExclusion(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	sel := anySelector()
	sel.Spaces = allSpacesExcluding(0)
	if _, err := s.CreateGrant(ruleGrant(1, allowRule(false, sel)), 0); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if s.HasAccess(1, viewDeployment(0)) {
		t.Fatal("excluded space 0 should be denied")
	}
	if !s.HasAccess(1, viewDeployment(1)) || !s.HasAccess(1, viewDeployment(65535)) {
		t.Fatal("all other spaces should be allowed")
	}
}

func TestEntityRefSelector(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	_, err := s.CreateGrant(ruleGrant(1, allowRule(false, apigen.AuthzSelector{
		Permissions: exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_VIEW),
		Spaces:      allSpacesExcluding(),
		EntityTypes: exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT),
		EntityRefs:  exactRefs(deploymentRef(7)),
	})), 0)
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if !s.HasAccess(1, viewDeployment(5)) {
		t.Fatal("ref 7 should be allowed")
	}
	other := viewDeployment(5)
	other.EntityID = 8
	if s.HasAccess(1, other) {
		t.Fatal("ref 8 should be denied")
	}
	noTarget := viewDeployment(5)
	noTarget.EntityID = 0
	if s.HasAccess(1, noTarget) {
		t.Fatal("untargeted request should not match a ref-restricted rule")
	}
	edit := viewDeployment(5)
	edit.Verb = apigen.AuthzVerb_AUTHZ_VERB_UPDATE
	if s.HasAccess(1, edit) {
		t.Fatal("verbs outside the include list should be denied")
	}
}

func TestEntityRefCarriesItsKind(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	sel := anySelector()
	sel.EntityRefs = exactRefs(secretRef(7))
	if _, err := s.CreateGrant(ruleGrant(1, allowRule(false, sel)), 0); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if s.HasAccess(1, viewDeployment(5)) {
		t.Fatal("a secret ref must not match a deployment with the same id")
	}
	secret := viewDeployment(5)
	secret.EntityType = apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET
	if !s.HasAccess(1, secret) {
		t.Fatal("secret ref 7 should be allowed")
	}
	excluding := anySelector()
	excluding.EntityRefs = apigen.AuthzEntityRefSelector{AllEntityRefsExcluding: apigen.Some(apigen.AuthzEntityRefList{Values: []apigen.AuthzEntityRef{deploymentRef(7)}})}
	if _, err := s.CreateGrant(ruleGrant(2, allowRule(false, excluding)), 0); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if s.HasAccess(2, viewDeployment(5)) {
		t.Fatal("excluded deployment 7 should be denied")
	}
	if !s.HasAccess(2, secret) {
		t.Fatal("secret 7 is not the excluded deployment 7")
	}
}

func TestGrantValidation(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	withSpaces := func(sel apigen.AuthzSpaceSelector) *apigen.AuthzRule {
		r := anySelector()
		r.Spaces = sel
		return allowRule(false, r)
	}
	withPermissions := func(sel apigen.AuthzPermissionSelector) *apigen.AuthzRule {
		r := anySelector()
		r.Permissions = sel
		return allowRule(false, r)
	}
	withRefs := func(sel apigen.AuthzEntityRefSelector) *apigen.AuthzRule {
		r := anySelector()
		r.EntityRefs = sel
		return allowRule(false, r)
	}
	bothLists := apigen.AuthzSpaceSelector{
		ExactSpaces:        apigen.Some(apigen.SpaceIdList{Values: []uint64{1}}),
		AllSpacesExcluding: apigen.Some(apigen.SpaceIdList{}),
	}
	denyAccess := anySelector()
	denyAccess.EntityTypes = exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ACCESS)
	cases := []struct {
		name  string
		grant *apigen.AuthzGrant
	}{
		{"no user", templateGrant(0, ClusterAdminTemplateID)},
		{"neither form", &apigen.AuthzGrant{UserID: 1}},
		{"both forms", &apigen.AuthzGrant{UserID: 1, Grant: apigen.AuthzGrantSource{Value: apigen.AuthzGrantSourceValueOneof{
			Rule:     allowRule(false, anySelector()),
			Template: &apigen.AuthzTemplateGrant{TemplateID: ClusterAdminTemplateID},
		}}}},
		{"unknown template", templateGrant(1, 99)},
		{"args without template argument", templateGrant(1, ClusterAdminTemplateID, spaceBinding(1, 1))},
		{"missing bindings", templateGrant(1, SpaceAdminTemplateID)},
		{"empty binding values", templateGrant(1, SpaceAdminTemplateID, spaceBinding(1))},
		{"binding without values", templateGrant(1, SpaceAdminTemplateID, apigen.AuthzArgumentBinding{ArgumentID: 1})},
		{"binding of another kind", templateGrant(1, SpaceAdminTemplateID, apigen.AuthzArgumentBinding{
			ArgumentID: 1,
			Values:     apigen.AuthzArgumentValues{Value: apigen.AuthzArgumentValuesValueOneof{Permissions: &apigen.AuthzPermissionValues{Values: []apigen.AuthzVerb{apigen.AuthzVerb_AUTHZ_VERB_VIEW}}}},
		})},
		{"binding value out of domain", templateGrant(1, SpaceAdminTemplateID, spaceBinding(1, 70000))},
		{"unknown argument id", templateGrant(1, SpaceAdminTemplateID, spaceBinding(9, 2))},
		{"duplicate binding", templateGrant(1, SpaceAdminTemplateID, spaceBinding(1, 2), spaceBinding(1, 3))},
		{"direct rule without effect", ruleGrant(1, &apigen.AuthzRule{Selector: anySelector()})},
		{"direct rule matching nothing", ruleGrant(1, withSpaces(exactSpaces()))},
		{"direct rule missing selector", ruleGrant(1, withSpaces(apigen.AuthzSpaceSelector{}))},
		{"direct rule with both lists", ruleGrant(1, withSpaces(bothLists))},
		{"direct rule invalid verb", ruleGrant(1, withPermissions(exactVerbs(99)))},
		{"direct rule invalid ref", ruleGrant(1, withRefs(exactRefs(apigen.AuthzEntityRef{})))},
		{"direct rule zero ref", ruleGrant(1, withRefs(exactRefs(deploymentRef(0))))},
		{"direct deny of access", ruleGrant(1, denyRule(false, denyAccess))},
	}
	for _, tc := range cases {
		if _, err := s.CreateGrant(tc.grant, 0); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
	if len(s.Grants()) != 0 {
		t.Fatal("no grants should have been stored")
	}
}

func TestGrantTemplateCRUD(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	viewSel := spacesArgTemplateSelector(1)
	viewSel.Permissions = templatePermissions(exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_VIEW))
	content := spacesTemplate(1, templateRule(allow(false), viewSel))
	created, err := s.CreateGrantTemplate("deployer", content, 5)
	if err != nil {
		t.Fatalf("CreateGrantTemplate: %v", err)
	}
	if created.ID <= SpaceAdminTemplateID {
		t.Fatalf("unexpected created template: %+v", created)
	}

	if _, err := s.CreateGrantTemplate("deployer", content, 5); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name: expected ErrNameTaken, got %v", err)
	}
	if _, err := s.CreateGrantTemplate("Bad Name", content, 5); err == nil {
		t.Fatal("invalid name should be rejected")
	}
	if _, err := s.UpdateGrantTemplate(ClusterAdminTemplateID, "cluster_admin", content, 0); !errors.Is(err, ErrBuiltin) {
		t.Fatalf("builtin update: expected ErrBuiltin, got %v", err)
	}
	if err := s.DeleteGrantTemplate(SpaceAdminTemplateID, 0); !errors.Is(err, ErrBuiltin) {
		t.Fatalf("builtin delete: expected ErrBuiltin, got %v", err)
	}

	grant, err := s.CreateGrant(templateGrant(1, created.ID, spaceBinding(1, 2)), 0)
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if !s.HasAccess(1, viewDeployment(2)) {
		t.Fatal("custom template grant should allow view in space 2")
	}
	if err := s.DeleteGrantTemplate(created.ID, 0); !errors.Is(err, ErrTemplateInUse) {
		t.Fatalf("referenced delete: expected ErrTemplateInUse, got %v", err)
	}

	updated, err := s.UpdateGrantTemplate(created.ID, "release_manager", spacesTemplate(1, templateRule(allow(false), spacesArgTemplateSelector(1))), 7)
	if err != nil {
		t.Fatalf("UpdateGrantTemplate: %v", err)
	}
	if updated.Name != "release_manager" {
		t.Fatalf("unexpected updated template: %+v", updated)
	}
	edit := viewDeployment(2)
	edit.Verb = apigen.AuthzVerb_AUTHZ_VERB_UPDATE
	if !s.HasAccess(1, edit) {
		t.Fatal("template edits should apply to existing grants immediately")
	}

	if err := s.DeleteGrant(2, grant.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete scoped to another user: expected ErrNotFound, got %v", err)
	}
	if err := s.DeleteGrant(1, grant.ID, 0); err != nil {
		t.Fatalf("DeleteGrant: %v", err)
	}
	if s.HasAccess(1, viewDeployment(2)) {
		t.Fatal("deleting the grant should drop access")
	}
	if err := s.DeleteGrantTemplate(created.ID, 0); err != nil {
		t.Fatalf("DeleteGrantTemplate: %v", err)
	}
	if _, err := s.GrantTemplate(created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted template lookup: expected ErrNotFound, got %v", err)
	}
	if _, err := s.CreateGrantTemplate("release_manager", content, 5); err != nil {
		t.Fatalf("name should be reusable after delete: %v", err)
	}
}

func TestTemplateValidation(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	arg := func(id uint32, name string, kind apigen.AuthzArgumentKind) apigen.AuthzTemplateArgument {
		return apigen.AuthzTemplateArgument{ID: id, Name: name, Kind: kind}
	}
	space := apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_SPACE
	wildcardRule := func() apigen.AuthzTemplateRule { return templateRule(allow(false), anyTemplateSelector()) }
	argSpacesRule := func(id uint32) apigen.AuthzTemplateRule {
		return templateRule(allow(false), spacesArgTemplateSelector(id))
	}
	twoPositions := spacesArgTemplateSelector(1)
	twoPositions.Permissions = apigen.AuthzTemplatePermissionSelector{Value: apigen.AuthzTemplatePermissionSelectorValueOneof{Argument: &apigen.AuthzArgument{ArgumentID: 1}}}
	missingPosition := anyTemplateSelector()
	missingPosition.EntityRefs = apigen.AuthzTemplateEntityRefSelector{}
	denyAccess := anyTemplateSelector()
	denyAccess.EntityTypes = templateEntityTypes(exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ACCESS))
	cases := []struct {
		name    string
		content *apigen.AuthzGrantTemplateSpec
	}{
		{"nil content", nil},
		{"no rules", &apigen.AuthzGrantTemplateSpec{}},
		{"undeclared argument", &apigen.AuthzGrantTemplateSpec{
			Rules: []apigen.AuthzTemplateRule{argSpacesRule(1)}}},
		{"unused argument", &apigen.AuthzGrantTemplateSpec{
			Arguments: []apigen.AuthzTemplateArgument{arg(1, "spaces", space)},
			Rules:     []apigen.AuthzTemplateRule{wildcardRule()}}},
		{"argument kind differs from its position", &apigen.AuthzGrantTemplateSpec{
			Arguments: []apigen.AuthzTemplateArgument{arg(1, "verbs", apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_PERMISSION)},
			Rules:     []apigen.AuthzTemplateRule{argSpacesRule(1)}}},
		{"argument in two position kinds", &apigen.AuthzGrantTemplateSpec{
			Arguments: []apigen.AuthzTemplateArgument{arg(1, "xs", space)},
			Rules:     []apigen.AuthzTemplateRule{templateRule(allow(false), twoPositions)}}},
		{"duplicate argument id", &apigen.AuthzGrantTemplateSpec{
			Arguments: []apigen.AuthzTemplateArgument{arg(1, "a", space), arg(1, "b", space)},
			Rules:     []apigen.AuthzTemplateRule{argSpacesRule(1)}}},
		{"duplicate argument name", &apigen.AuthzGrantTemplateSpec{
			Arguments: []apigen.AuthzTemplateArgument{arg(1, "a", space), arg(2, "a", space)},
			Rules:     []apigen.AuthzTemplateRule{argSpacesRule(1), argSpacesRule(2)}}},
		{"invalid argument name", &apigen.AuthzGrantTemplateSpec{
			Arguments: []apigen.AuthzTemplateArgument{arg(1, "Bad Name", space)},
			Rules:     []apigen.AuthzTemplateRule{argSpacesRule(1)}}},
		{"invalid argument id", &apigen.AuthzGrantTemplateSpec{
			Arguments: []apigen.AuthzTemplateArgument{arg(0, "spaces", space)},
			Rules:     []apigen.AuthzTemplateRule{argSpacesRule(1)}}},
		{"invalid argument kind", &apigen.AuthzGrantTemplateSpec{
			Arguments: []apigen.AuthzTemplateArgument{arg(1, "spaces", 0)},
			Rules:     []apigen.AuthzTemplateRule{argSpacesRule(1)}}},
		{"missing position", &apigen.AuthzGrantTemplateSpec{
			Rules: []apigen.AuthzTemplateRule{templateRule(allow(false), missingPosition)}}},
		{"rule without effect", &apigen.AuthzGrantTemplateSpec{
			Rules: []apigen.AuthzTemplateRule{{Selector: anyTemplateSelector()}}}},
		{"deny of access", &apigen.AuthzGrantTemplateSpec{
			Rules: []apigen.AuthzTemplateRule{templateRule(deny(false), denyAccess)}}},
	}
	for _, tc := range cases {
		if _, err := s.CreateGrantTemplate("t1", tc.content, 1); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
	if len(s.GrantTemplates()) != 2 {
		t.Fatal("no templates should have been stored")
	}
}

func TestUpdateTemplateSignatureGuard(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	created, err := s.CreateGrantTemplate("deployer", spacesTemplate(1, templateRule(allow(false), spacesArgTemplateSelector(1))), 1)
	if err != nil {
		t.Fatalf("CreateGrantTemplate: %v", err)
	}
	grant, err := s.CreateGrant(templateGrant(1, created.ID, spaceBinding(1, 2)), 0)
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	changed := spacesTemplate(2, templateRule(allow(false), spacesArgTemplateSelector(2)))
	if _, err := s.UpdateGrantTemplate(created.ID, "deployer", changed, 0); err == nil {
		t.Fatal("changing the argument signature must be rejected while grants bind it")
	}
	renamed := spacesTemplate(1, templateRule(allow(false), spacesArgTemplateSelector(1)))
	renamed.Arguments[0].Name = "space_ids"
	if _, err := s.UpdateGrantTemplate(created.ID, "deployer", renamed, 0); err != nil {
		t.Fatalf("renaming an argument must not invalidate bindings: %v", err)
	}
	if !s.HasAccess(1, viewDeployment(2)) {
		t.Fatal("grant should still resolve after the argument rename")
	}
	if err := s.DeleteGrant(1, grant.ID, 0); err != nil {
		t.Fatalf("DeleteGrant: %v", err)
	}
	if _, err := s.UpdateGrantTemplate(created.ID, "deployer", changed, 0); err != nil {
		t.Fatalf("signature change should be allowed once no grants bind it: %v", err)
	}
}

func TestGlobalRuleOverridesAllow(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	if _, err := s.CreateGrant(templateGrant(1, ClusterAdminTemplateID), 0); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	rule, err := s.CreateGlobalRule("no_prod_reveal", denyRule(false, apigen.AuthzSelector{
		Permissions: exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_REVEAL),
		Spaces:      exactSpaces(3),
		EntityTypes: exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET),
		EntityRefs:  allEntityRefs(),
	}), 1)
	if err != nil {
		t.Fatalf("CreateGlobalRule: %v", err)
	}
	reveal := RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_REVEAL,
		SpaceID:    3,
		EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET,
		EntityID:   4,
	}
	if s.HasAccess(1, reveal) {
		t.Fatal("global rule should beat cluster_admin")
	}
	elsewhere := reveal
	elsewhere.SpaceID = 2
	if !s.HasAccess(1, elsewhere) {
		t.Fatal("global rule should be scoped to its selectors")
	}
	view := reveal
	view.Verb = apigen.AuthzVerb_AUTHZ_VERB_VIEW
	if !s.HasAccess(1, view) {
		t.Fatal("other verbs should be unaffected")
	}
	if err := s.DeleteGlobalRule(rule.ID, 0); err != nil {
		t.Fatalf("DeleteGlobalRule: %v", err)
	}
	if !s.HasAccess(1, reveal) {
		t.Fatal("deleting the global rule should restore access")
	}
	if err := s.DeleteGlobalRule(rule.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: expected ErrNotFound, got %v", err)
	}
}

func TestGrantDenyRuleOverridesAllow(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	for _, user := range []uint64{1, 2} {
		if _, err := s.CreateGrant(templateGrant(user, ClusterAdminTemplateID), 0); err != nil {
			t.Fatalf("CreateGrant: %v", err)
		}
	}
	denied, err := s.CreateGrant(ruleGrant(1, denyRule(false, apigen.AuthzSelector{
		Permissions: exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_REVEAL),
		Spaces:      exactSpaces(3),
		EntityTypes: exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET),
		EntityRefs:  allEntityRefs(),
	})), 0)
	if err != nil {
		t.Fatalf("CreateGrant deny: %v", err)
	}
	reveal := RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_REVEAL,
		SpaceID:    3,
		EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET,
		EntityID:   4,
	}
	if s.HasAccess(1, reveal) {
		t.Fatal("a deny grant rule should beat the user's cluster_admin grant")
	}
	if !s.HasAccess(2, reveal) {
		t.Fatal("a deny grant rule is scoped to its user")
	}
	if !s.SpaceVisible(1, 3, false) {
		t.Fatal("deny rules do not withdraw space visibility the allow grants")
	}
	if _, err := s.CreateGrant(ruleGrant(1, denyRule(false, anySelector())), 0); err != nil {
		t.Fatalf("CreateGrant deny everything: %v", err)
	}
	if !s.HasAccess(1, adminAccess) {
		t.Fatal("deny grant rules must not reach access management")
	}
	if err := s.DeleteGrant(1, denied.ID, 0); err != nil {
		t.Fatalf("DeleteGrant: %v", err)
	}
}

func TestClusterAdminDelegationLimits(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	if _, err := s.CreateGrant(templateGrant(1, ClusterAdminTemplateID), 0); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	direct := RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_REVEAL,
		SpaceID:    1,
		EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET,
		EntityID:   4,
	}
	if !s.HasAccess(1, direct) {
		t.Fatal("direct access should cover reveal in a user space")
	}
	delegated := viewDeployment(2)
	delegated.Delegated = true
	if !s.HasAccess(1, delegated) {
		t.Fatal("delegated access should be allowed outside the opendeploy space")
	}
	opendeploy := viewDeployment(0)
	opendeploy.Delegated = true
	if s.HasAccess(1, opendeploy) {
		t.Fatal("delegated access must not reach the opendeploy space")
	}
	reveal := direct
	reveal.SpaceID = 2
	reveal.Delegated = true
	if s.HasAccess(1, reveal) {
		t.Fatal("delegated access must not reveal secrets")
	}
	secretView := reveal
	secretView.Verb = apigen.AuthzVerb_AUTHZ_VERB_VIEW
	if !s.HasAccess(1, secretView) {
		t.Fatal("delegated access should view secret metadata")
	}
	secretCreate := reveal
	secretCreate.Verb = apigen.AuthzVerb_AUTHZ_VERB_CREATE
	secretCreate.EntityID = 0
	if !s.HasAccess(1, secretCreate) {
		t.Fatal("delegated access should create secrets")
	}
	logs := RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_VIEW_LOGS,
		SpaceID:    2,
		EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT,
		EntityID:   7,
	}
	if !s.HasAccess(1, logs) {
		t.Fatal("direct access should cover view_logs")
	}
	logs.Delegated = true
	if s.HasAccess(1, logs) {
		t.Fatal("delegated access must not view logs")
	}
}

func TestGlobalRuleDelegatedOnly(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	if _, err := s.CreateGrant(ruleGrant(1, allowRule(true, anySelector())), 0); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if _, err := s.CreateGlobalRule("no_agent_reveal", denyRule(true, apigen.AuthzSelector{
		Permissions: exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_REVEAL),
		Spaces:      allSpacesExcluding(),
		EntityTypes: exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET),
		EntityRefs:  allEntityRefs(),
	}), 1); err != nil {
		t.Fatalf("CreateGlobalRule: %v", err)
	}
	reveal := RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_REVEAL,
		SpaceID:    2,
		EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET,
		EntityID:   4,
	}
	if !s.HasAccess(1, reveal) {
		t.Fatal("delegated-only rule should not affect direct access")
	}
	reveal.Delegated = true
	if s.HasAccess(1, reveal) {
		t.Fatal("delegated access should be denied")
	}
}

func TestGrantDelegationFlag(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	if _, err := s.CreateGrant(ruleGrant(1, allowRule(false, anySelector())), 0); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	req := viewDeployment(2)
	if !s.HasAccess(1, req) {
		t.Fatal("direct access should be allowed")
	}
	req.Delegated = true
	if s.HasAccess(1, req) {
		t.Fatal("a rule without delegation_allowed must not satisfy delegated access")
	}

	if _, err := s.CreateGrant(templateGrant(2, ClusterAdminTemplateID), 0); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if !s.HasAccess(2, req) {
		t.Fatal("cluster_admin allows delegation")
	}
}

func TestGlobalRuleAccessCarveOut(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	if _, err := s.CreateGrant(templateGrant(1, ClusterAdminTemplateID), 0); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if _, err := s.CreateGlobalRule("deny_everything", denyRule(false, anySelector()), 1); err != nil {
		t.Fatalf("CreateGlobalRule: %v", err)
	}
	if s.HasAccess(1, viewDeployment(1)) {
		t.Fatal("deny-everything rule should deny deployments")
	}
	access := RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_UPDATE,
		SpaceID:    0,
		EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ACCESS,
	}
	if !s.HasAccess(1, access) {
		t.Fatal("ACCESS checks must skip global rules so the rule stays removable")
	}
}

func TestGlobalRuleValidation(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	withSpaces := func(sel apigen.AuthzSpaceSelector) *apigen.AuthzRule {
		r := anySelector()
		r.Spaces = sel
		return allowRule(false, r)
	}
	denyAccess := anySelector()
	denyAccess.EntityTypes = exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ACCESS)
	cases := []struct {
		name     string
		ruleName string
		rule     *apigen.AuthzRule
	}{
		{"nil", "p", nil},
		{"no name", "", allowRule(false, anySelector())},
		{"missing selector", "p", withSpaces(apigen.AuthzSpaceSelector{})},
		{"matches nothing", "p", withSpaces(exactSpaces())},
		{"denies access entity", "p", denyRule(false, denyAccess)},
		{"invalid space", "p", withSpaces(exactSpaces(70000))},
		{"no effect", "p", &apigen.AuthzRule{Selector: anySelector()}},
	}
	for _, tc := range cases {
		if _, err := s.CreateGlobalRule(tc.ruleName, tc.rule, 1); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
	if rules := s.GlobalRules(); len(rules) != 1 || rules[0].Name != DefaultUserVisibilityRuleName {
		t.Fatalf("only the seeded default rule should be stored, got %+v", rules)
	}
	// An allow rule targeting access is only additive, so the deny carve-out
	// does not apply to it.
	if _, err := s.CreateGlobalRule("access_allow", allowRule(false, denyAccess), 1); err != nil {
		t.Fatalf("allow rule targeting access: %v", err)
	}
}

func TestReloadPreservesState(t *testing.T) {
	store := newTestStore(t)
	s := mustOpen(t, store)
	created, err := s.CreateGrantTemplate("deployer", spacesTemplate(1, templateRule(allow(false), spacesArgTemplateSelector(1))), 5)
	if err != nil {
		t.Fatalf("CreateGrantTemplate: %v", err)
	}
	if _, err := s.CreateGrant(templateGrant(1, created.ID, spaceBinding(1, 3)), 0); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	noDeletes := anySelector()
	noDeletes.Permissions = exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_DELETE)
	if _, err := s.CreateGlobalRule("no_deletes", denyRule(false, noDeletes), 5); err != nil {
		t.Fatalf("CreateGlobalRule: %v", err)
	}

	reloaded := mustOpen(t, store)
	if !reloaded.HasAccess(1, viewDeployment(3)) {
		t.Fatal("access should survive a reload")
	}
	if reloaded.HasAccess(1, viewDeployment(4)) {
		t.Fatal("reload must not widen access")
	}
	del := viewDeployment(3)
	del.Verb = apigen.AuthzVerb_AUTHZ_VERB_DELETE
	if reloaded.HasAccess(1, del) {
		t.Fatal("global rule should survive a reload")
	}
	if len(reloaded.GlobalRules()) != 2 {
		t.Fatalf("expected the seeded default plus 1 created global rule after reload, got %d", len(reloaded.GlobalRules()))
	}
	if len(reloaded.GrantTemplates()) != 3 {
		t.Fatalf("expected 3 templates after reload, got %d", len(reloaded.GrantTemplates()))
	}
	grants := reloaded.GrantsForUser(1)
	if len(grants) != 1 || grants[0].Grant.Value.Template == nil || grants[0].Grant.Value.Template.TemplateID != created.ID {
		t.Fatalf("unexpected grants after reload: %+v", grants)
	}
}

func TestSpaceVisible(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	if _, err := s.CreateGrant(templateGrant(1, SpaceAdminTemplateID, spaceBinding(1, 3)), 0); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if !s.SpaceVisible(1, 3, false) {
		t.Fatal("granted space should be visible")
	}
	if !s.SpaceVisible(1, 0, false) {
		t.Fatal("space touched by the seeded default_user_visibility rule should be visible")
	}
	if s.SpaceVisible(1, 4, false) {
		t.Fatal("untouched space must not be visible")
	}
	if s.SpaceVisible(2, 3, false) {
		t.Fatal("visibility must not leak to another user")
	}
	if !s.SpaceVisible(1, 3, true) {
		t.Fatal("space_admin delegable rule should keep the space visible to agents")
	}
	sel := anySelector()
	sel.Spaces = exactSpaces(6)
	if _, err := s.CreateGrant(ruleGrant(5, allowRule(false, sel)), 0); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if !s.SpaceVisible(5, 6, false) {
		t.Fatal("direct grant should make its space visible")
	}
	if s.SpaceVisible(5, 6, true) {
		t.Fatal("non-delegable grant must not make the space visible to agents")
	}
}

func TestLastAdminGrantGuard(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	admin, err := s.CreateGrant(templateGrant(1, ClusterAdminTemplateID), 0)
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	limited, err := s.CreateGrant(templateGrant(2, SpaceAdminTemplateID, spaceBinding(1, 3)), 0)
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if err := s.DeleteGrant(1, admin.ID, 0); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("deleting the only admin grant: got %v, want ErrLastAdmin", err)
	}
	if err := s.DeleteGrant(2, limited.ID, 0); err != nil {
		t.Fatalf("deleting a non-admin grant should be allowed: %v", err)
	}
	second, err := s.CreateGrant(templateGrant(2, ClusterAdminTemplateID), 0)
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if err := s.DeleteGrant(1, admin.ID, 0); err != nil {
		t.Fatalf("deleting an admin grant with another admin present: %v", err)
	}
	if err := s.DeleteGrant(2, second.ID, 0); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("the remaining admin grant must be protected: got %v, want ErrLastAdmin", err)
	}
}

func viewUser(userID uint64) RequestedAccess {
	return RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_VIEW,
		SpaceID:    0,
		EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_USER,
		EntityID:   userID,
	}
}

func TestSeededDefaultUserVisibility(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	roster := viewUser(3)
	if !s.HasAccess(9, roster) {
		t.Fatal("a user with no grants should view the user roster")
	}
	delegated := roster
	delegated.Delegated = true
	if !s.HasAccess(9, delegated) {
		t.Fatal("the seeded rule extends to delegated sessions")
	}
	edit := roster
	edit.Verb = apigen.AuthzVerb_AUTHZ_VERB_UPDATE
	if s.HasAccess(9, edit) {
		t.Fatal("the seeded rule grants view only")
	}
	if s.HasAccess(9, viewDeployment(1)) {
		t.Fatal("the seeded rule must not grant anything beyond the roster")
	}
}

func TestGlobalDenyBeatsAllow(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	if _, err := s.CreateGlobalRule("no_roster", denyRule(false, apigen.AuthzSelector{
		Permissions: exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_VIEW),
		Spaces:      allSpacesExcluding(),
		EntityTypes: exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_USER),
		EntityRefs:  allEntityRefs(),
	}), 1); err != nil {
		t.Fatalf("CreateGlobalRule: %v", err)
	}
	if s.HasAccess(9, viewUser(3)) {
		t.Fatal("a global deny must beat the seeded allow rule")
	}
	// A grant does not survive the deny either: denies stay first.
	if _, err := s.CreateGrant(templateGrant(1, ClusterAdminTemplateID), 0); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if s.HasAccess(1, viewUser(3)) {
		t.Fatal("a global deny must beat grants")
	}
}

func TestGlobalAllowDelegationFlag(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	if _, err := s.CreateGlobalRule("humans_view_deployments", allowRule(false, apigen.AuthzSelector{
		Permissions: exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_VIEW),
		Spaces:      exactSpaces(2),
		EntityTypes: exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT),
		EntityRefs:  allEntityRefs(),
	}), 1); err != nil {
		t.Fatalf("CreateGlobalRule: %v", err)
	}
	direct := viewDeployment(2)
	if !s.HasAccess(9, direct) {
		t.Fatal("allow rule should grant direct access to everyone")
	}
	delegated := direct
	delegated.Delegated = true
	if s.HasAccess(9, delegated) {
		t.Fatal("allow rule without delegation_allowed must not reach agents")
	}
	if !s.SpaceVisible(9, 2, false) {
		t.Fatal("a space an allow rule touches should be visible")
	}
	if s.SpaceVisible(9, 2, true) {
		t.Fatal("space visibility through a non-delegable allow rule must not reach agents")
	}
}

func TestDefaultUserVisibilityDeleteIsFinal(t *testing.T) {
	store := newTestStore(t)
	s := mustOpen(t, store)
	rules := s.GlobalRules()
	if len(rules) != 1 || rules[0].Name != DefaultUserVisibilityRuleName {
		t.Fatalf("expected only the seeded rule, got %+v", rules)
	}
	if err := s.DeleteGlobalRule(rules[0].ID, 0); err != nil {
		t.Fatalf("DeleteGlobalRule: %v", err)
	}
	if s.HasAccess(9, viewUser(3)) {
		t.Fatal("roster access should end with the rule")
	}
	reloaded := mustOpen(t, store)
	if len(reloaded.GlobalRules()) != 0 {
		t.Fatal("a deleted seeded rule must not be re-asserted on reload")
	}
	if reloaded.HasAccess(9, viewUser(3)) {
		t.Fatal("roster access must stay revoked after reload")
	}
}

func TestSystemSpaceFenceBeatsEveryGrant(t *testing.T) {
	s := mustOpen(t, newTestStore(t))
	if _, err := s.CreateGrant(templateGrant(1, ClusterAdminTemplateID), 0); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		verb   apigen.AuthzVerb
		entity apigen.AuthzEntityKind
		space  uint64
		want   bool
	}{
		{apigen.AuthzVerb_AUTHZ_VERB_VIEW, apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET, 0, false},
		{apigen.AuthzVerb_AUTHZ_VERB_REVEAL, apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET, 0, false},
		{apigen.AuthzVerb_AUTHZ_VERB_VIEW, apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_CONFIG, 0, false},
		{apigen.AuthzVerb_AUTHZ_VERB_VIEW, apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ASSET, 0, false},
		{apigen.AuthzVerb_AUTHZ_VERB_CREATE, apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT, 0, false},
		{apigen.AuthzVerb_AUTHZ_VERB_VIEW, apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT, 0, true},
		{apigen.AuthzVerb_AUTHZ_VERB_VIEW_LOGS, apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT, 0, true},
		{apigen.AuthzVerb_AUTHZ_VERB_VIEW, apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_NODE, 0, true},
		{apigen.AuthzVerb_AUTHZ_VERB_CREATE, apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ACCESS, 0, true},
		{apigen.AuthzVerb_AUTHZ_VERB_VIEW, apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET, 1, true},
		{apigen.AuthzVerb_AUTHZ_VERB_CREATE, apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT, 1, true},
	}
	for _, c := range cases {
		got := s.HasAccess(1, RequestedAccess{Verb: c.verb, SpaceID: c.space, EntityType: c.entity})
		if got != c.want {
			t.Errorf("cluster admin %v %v in space %d = %v, want %v", c.verb, c.entity, c.space, got, c.want)
		}
	}
}

func sourceBlob(src apigen.AuthzGrantSource) []byte { return src.Encode() }
