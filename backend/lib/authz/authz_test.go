package authz

import (
	"errors"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type memStore struct {
	templates map[int64]RuleTemplateRow
	grants    map[int64]GrantRow
	rules     map[int64]GlobalRuleRow
	kv        map[string][]byte
	nextID    int64
}

func newMemStore() *memStore {
	return &memStore{
		templates: make(map[int64]RuleTemplateRow),
		grants:    make(map[int64]GrantRow),
		rules:     make(map[int64]GlobalRuleRow),
		kv:        make(map[string][]byte),
	}
}

func (m *memStore) alloc() int64 {
	m.nextID++
	return m.nextID
}

func (m *memStore) ListAuthzRuleTemplates() ([]RuleTemplateRow, error) {
	out := make([]RuleTemplateRow, 0, len(m.templates))
	for _, row := range m.templates {
		out = append(out, row)
	}
	return out, nil
}

func (m *memStore) InsertAuthzRuleTemplate(row RuleTemplateRow) (int64, error) {
	row.ID = m.alloc()
	m.templates[row.ID] = row
	return row.ID, nil
}

func (m *memStore) UpdateAuthzRuleTemplate(id int64, name string, blob []byte, author, updatedAt int64) error {
	row := m.templates[id]
	row.Name, row.Blob = name, blob
	m.templates[id] = row
	return nil
}

func (m *memStore) DeleteAuthzRuleTemplate(id int64) error {
	row := m.templates[id]
	row.Deleted = true
	m.templates[id] = row
	return nil
}

func (m *memStore) UpsertAuthzRuleTemplate(id int64, name string, blob []byte) error {
	if id > m.nextID {
		m.nextID = id
	}
	m.templates[id] = RuleTemplateRow{ID: id, Name: name, Builtin: true, Blob: blob}
	return nil
}

func (m *memStore) ListAuthzGrants() ([]GrantRow, error) {
	out := make([]GrantRow, 0, len(m.grants))
	for _, row := range m.grants {
		out = append(out, row)
	}
	return out, nil
}

func (m *memStore) InsertAuthzGrant(row GrantRow) (int64, error) {
	row.ID = m.alloc()
	m.grants[row.ID] = row
	return row.ID, nil
}

// DeleteAuthzGrant soft-deletes in the real store; the map delete models the
// row disappearing from ListAuthzGrants either way.
func (m *memStore) DeleteAuthzGrant(id int64) error {
	delete(m.grants, id)
	return nil
}

func (m *memStore) ListAuthzGlobalRules() ([]GlobalRuleRow, error) {
	out := make([]GlobalRuleRow, 0, len(m.rules))
	for _, row := range m.rules {
		out = append(out, row)
	}
	return out, nil
}

func (m *memStore) InsertAuthzGlobalRule(row GlobalRuleRow) (int64, error) {
	row.ID = m.alloc()
	m.rules[row.ID] = row
	return row.ID, nil
}

func (m *memStore) DeleteAuthzGlobalRule(id int64) error {
	delete(m.rules, id)
	return nil
}

func (m *memStore) FetchLocalKV(key string) ([]byte, bool) {
	v, ok := m.kv[key]
	return v, ok
}

func (m *memStore) MustSetLocalKV(key string, value []byte) {
	m.kv[key] = value
}

func mustOpen(t *testing.T, store Store) *Service {
	t.Helper()
	s, err := Open(store)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func all() *apigen.AuthzSelector { return &apigen.AuthzSelector{Wildcard: true} }

func templateGrant(userID, templateID int64, args ...*apigen.AuthzArgumentBinding) *apigen.AuthzGrantRecord {
	return &apigen.AuthzGrantRecord{
		UserID:     userID,
		TemplateID: templateID,
		Grant:      &apigen.AuthzGrant{Args: args},
	}
}

func ruleGrant(userID int64, rule *apigen.AuthzRule) *apigen.AuthzGrantRecord {
	return &apigen.AuthzGrantRecord{
		UserID: userID,
		Grant:  &apigen.AuthzGrant{Rule: rule},
	}
}

func viewDeployment(space int64) RequestedAccess {
	return RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_VIEW,
		SpaceID:    space,
		EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_DEPLOYMENT,
		EntityID:   7,
	}
}

func TestBuiltinsSeededAndListed(t *testing.T) {
	s := mustOpen(t, newMemStore())
	templates := s.RuleTemplates()
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
	s := mustOpen(t, newMemStore())
	if s.HasAccess(1, viewDeployment(0)) {
		t.Fatal("user with no grants should have no access")
	}
}

func TestClusterAdminGrant(t *testing.T) {
	s := mustOpen(t, newMemStore())
	if _, err := s.CreateGrant(templateGrant(1, ClusterAdminTemplateID)); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	for _, req := range []RequestedAccess{
		viewDeployment(0),
		viewDeployment(9),
		{Verb: apigen.AuthzVerb_AUTHZ_VERB_REVEAL, SpaceID: 3, EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_SECRET, EntityID: 12},
		{Verb: apigen.AuthzVerb_AUTHZ_VERB_CREATE, SpaceID: 1, EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_DEPLOYMENT},
	} {
		if !s.HasAccess(1, req) {
			t.Fatalf("cluster_admin should allow %+v", req)
		}
	}
	if s.HasAccess(2, viewDeployment(1)) {
		t.Fatal("grant must not leak to another user")
	}
	if s.HasAccess(1, RequestedAccess{SpaceID: 1, EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_DEPLOYMENT}) {
		t.Fatal("unknown verb must never match")
	}
	if s.HasAccess(1, RequestedAccess{Verb: apigen.AuthzVerb_AUTHZ_VERB_VIEW, SpaceID: 1}) {
		t.Fatal("unknown entity type must never match")
	}
}

func TestSpaceAdminArgumentBinding(t *testing.T) {
	s := mustOpen(t, newMemStore())
	if _, err := s.CreateGrant(templateGrant(1, SpaceAdminTemplateID, &apigen.AuthzArgumentBinding{ArgumentID: 1, Values: []int64{2, 3}})); err != nil {
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
		EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_SECRET,
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
		EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_DEPLOYMENT,
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
	s := mustOpen(t, newMemStore())
	_, err := s.CreateGrant(ruleGrant(1, &apigen.AuthzRule{
		Permissions: all(),
		Spaces:      &apigen.AuthzSelector{Wildcard: true, Exclude: []int64{0}},
		EntityTypes: all(),
		EntityRefs:  all(),
	}))
	if err != nil {
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
	s := mustOpen(t, newMemStore())
	_, err := s.CreateGrant(ruleGrant(1, &apigen.AuthzRule{
		Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW)}},
		Spaces:      all(),
		EntityTypes: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzEntity_AUTHZ_ENTITY_DEPLOYMENT)}},
		EntityRefs:  &apigen.AuthzSelector{Include: []int64{7}},
	}))
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

func TestGrantValidation(t *testing.T) {
	s := mustOpen(t, newMemStore())
	cases := []struct {
		name  string
		grant *apigen.AuthzGrantRecord
	}{
		{"no user", templateGrant(0, ClusterAdminTemplateID)},
		{"neither form", &apigen.AuthzGrantRecord{UserID: 1}},
		{"both forms", &apigen.AuthzGrantRecord{UserID: 1, TemplateID: ClusterAdminTemplateID, Grant: &apigen.AuthzGrant{
			Rule: &apigen.AuthzRule{Permissions: all(), Spaces: all(), EntityTypes: all(), EntityRefs: all()}}}},
		{"unknown template", templateGrant(1, 99)},
		{"args without template argument", templateGrant(1, ClusterAdminTemplateID, &apigen.AuthzArgumentBinding{ArgumentID: 1, Values: []int64{1}})},
		{"missing bindings", templateGrant(1, SpaceAdminTemplateID)},
		{"empty binding values", templateGrant(1, SpaceAdminTemplateID, &apigen.AuthzArgumentBinding{ArgumentID: 1})},
		{"binding value out of domain", templateGrant(1, SpaceAdminTemplateID, &apigen.AuthzArgumentBinding{ArgumentID: 1, Values: []int64{70000}})},
		{"unknown argument id", templateGrant(1, SpaceAdminTemplateID, &apigen.AuthzArgumentBinding{ArgumentID: 9, Values: []int64{2}})},
		{"duplicate binding", templateGrant(1, SpaceAdminTemplateID,
			&apigen.AuthzArgumentBinding{ArgumentID: 1, Values: []int64{2}},
			&apigen.AuthzArgumentBinding{ArgumentID: 1, Values: []int64{3}})},
		{"direct rule with argument", ruleGrant(1, &apigen.AuthzRule{
			Permissions: all(), Spaces: &apigen.AuthzSelector{ArgumentID: 1}, EntityTypes: all(), EntityRefs: all()})},
		{"direct rule matching nothing", ruleGrant(1, &apigen.AuthzRule{
			Permissions: all(), Spaces: &apigen.AuthzSelector{}, EntityTypes: all(), EntityRefs: all()})},
		{"direct rule missing selector", ruleGrant(1, &apigen.AuthzRule{
			Permissions: all(), Spaces: all(), EntityTypes: all()})},
		{"direct rule invalid verb", ruleGrant(1, &apigen.AuthzRule{
			Permissions: &apigen.AuthzSelector{Include: []int64{99}}, Spaces: all(), EntityTypes: all(), EntityRefs: all()})},
	}
	for _, tc := range cases {
		if _, err := s.CreateGrant(tc.grant); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
	if len(s.Grants()) != 0 {
		t.Fatal("no grants should have been stored")
	}
}

func TestRuleTemplateCRUD(t *testing.T) {
	s := mustOpen(t, newMemStore())
	content := &apigen.AuthzRuleTemplate{
		Arguments: []*apigen.AuthzTemplateArgument{{ID: 1, Name: "spaces"}},
		Rules: []*apigen.AuthzRule{{
			Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW)}},
			Spaces:      &apigen.AuthzSelector{ArgumentID: 1},
			EntityTypes: all(),
			EntityRefs:  all(),
		}},
	}
	created, err := s.CreateRuleTemplate("deployer", content, 5)
	if err != nil {
		t.Fatalf("CreateRuleTemplate: %v", err)
	}
	if created.ID <= SpaceAdminTemplateID || created.Author != 5 || created.CreatedAt == 0 {
		t.Fatalf("unexpected created template: %+v", created)
	}

	if _, err := s.CreateRuleTemplate("deployer", content, 5); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name: expected ErrNameTaken, got %v", err)
	}
	if _, err := s.CreateRuleTemplate("Bad Name", content, 5); err == nil {
		t.Fatal("invalid name should be rejected")
	}
	if _, err := s.UpdateRuleTemplate(ClusterAdminTemplateID, "cluster_admin", content, 0); !errors.Is(err, ErrBuiltin) {
		t.Fatalf("builtin update: expected ErrBuiltin, got %v", err)
	}
	if err := s.DeleteRuleTemplate(SpaceAdminTemplateID); !errors.Is(err, ErrBuiltin) {
		t.Fatalf("builtin delete: expected ErrBuiltin, got %v", err)
	}

	grant, err := s.CreateGrant(templateGrant(1, created.ID, &apigen.AuthzArgumentBinding{ArgumentID: 1, Values: []int64{2}}))
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if !s.HasAccess(1, viewDeployment(2)) {
		t.Fatal("custom template grant should allow view in space 2")
	}
	if err := s.DeleteRuleTemplate(created.ID); !errors.Is(err, ErrTemplateInUse) {
		t.Fatalf("referenced delete: expected ErrTemplateInUse, got %v", err)
	}

	updated, err := s.UpdateRuleTemplate(created.ID, "release_manager", &apigen.AuthzRuleTemplate{
		Arguments: []*apigen.AuthzTemplateArgument{{ID: 1, Name: "spaces"}},
		Rules: []*apigen.AuthzRule{{
			Permissions: all(),
			Spaces:      &apigen.AuthzSelector{ArgumentID: 1},
			EntityTypes: all(),
			EntityRefs:  all(),
		}},
	}, 7)
	if err != nil {
		t.Fatalf("UpdateRuleTemplate: %v", err)
	}
	if updated.Name != "release_manager" || updated.Author != 5 {
		t.Fatalf("unexpected updated template: %+v", updated)
	}
	edit := viewDeployment(2)
	edit.Verb = apigen.AuthzVerb_AUTHZ_VERB_UPDATE
	if !s.HasAccess(1, edit) {
		t.Fatal("template edits should apply to existing grants immediately")
	}

	if err := s.DeleteGrant(2, grant.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete scoped to another user: expected ErrNotFound, got %v", err)
	}
	if err := s.DeleteGrant(1, grant.ID); err != nil {
		t.Fatalf("DeleteGrant: %v", err)
	}
	if s.HasAccess(1, viewDeployment(2)) {
		t.Fatal("deleting the grant should drop access")
	}
	if err := s.DeleteRuleTemplate(created.ID); err != nil {
		t.Fatalf("DeleteRuleTemplate: %v", err)
	}
	if _, err := s.RuleTemplate(created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted template lookup: expected ErrNotFound, got %v", err)
	}
	if _, err := s.CreateRuleTemplate("release_manager", content, 5); err != nil {
		t.Fatalf("name should be reusable after delete: %v", err)
	}
}

func TestTemplateValidation(t *testing.T) {
	s := mustOpen(t, newMemStore())
	arg := func(id int64, name string) *apigen.AuthzTemplateArgument {
		return &apigen.AuthzTemplateArgument{ID: id, Name: name}
	}
	wildcardRule := func() *apigen.AuthzRule {
		return &apigen.AuthzRule{Permissions: all(), Spaces: all(), EntityTypes: all(), EntityRefs: all()}
	}
	argSpacesRule := func(id int64) *apigen.AuthzRule {
		return &apigen.AuthzRule{Permissions: all(), Spaces: &apigen.AuthzSelector{ArgumentID: id}, EntityTypes: all(), EntityRefs: all()}
	}
	cases := []struct {
		name    string
		content *apigen.AuthzRuleTemplate
	}{
		{"nil content", nil},
		{"no rules", &apigen.AuthzRuleTemplate{}},
		{"undeclared argument", &apigen.AuthzRuleTemplate{
			Rules: []*apigen.AuthzRule{argSpacesRule(1)}}},
		{"unused argument", &apigen.AuthzRuleTemplate{
			Arguments: []*apigen.AuthzTemplateArgument{arg(1, "spaces")},
			Rules:     []*apigen.AuthzRule{wildcardRule()}}},
		{"argument in two position kinds", &apigen.AuthzRuleTemplate{
			Arguments: []*apigen.AuthzTemplateArgument{arg(1, "xs")},
			Rules: []*apigen.AuthzRule{{
				Permissions: &apigen.AuthzSelector{ArgumentID: 1},
				Spaces:      &apigen.AuthzSelector{ArgumentID: 1},
				EntityTypes: all(),
				EntityRefs:  all(),
			}}}},
		{"duplicate argument id", &apigen.AuthzRuleTemplate{
			Arguments: []*apigen.AuthzTemplateArgument{arg(1, "a"), arg(1, "b")},
			Rules:     []*apigen.AuthzRule{argSpacesRule(1)}}},
		{"duplicate argument name", &apigen.AuthzRuleTemplate{
			Arguments: []*apigen.AuthzTemplateArgument{arg(1, "a"), arg(2, "a")},
			Rules:     []*apigen.AuthzRule{argSpacesRule(1), argSpacesRule(2)}}},
		{"invalid argument name", &apigen.AuthzRuleTemplate{
			Arguments: []*apigen.AuthzTemplateArgument{arg(1, "Bad Name")},
			Rules:     []*apigen.AuthzRule{argSpacesRule(1)}}},
		{"invalid argument id", &apigen.AuthzRuleTemplate{
			Arguments: []*apigen.AuthzTemplateArgument{arg(0, "spaces")},
			Rules:     []*apigen.AuthzRule{argSpacesRule(1)}}},
	}
	for _, tc := range cases {
		if _, err := s.CreateRuleTemplate("t1", tc.content, 1); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
	if len(s.RuleTemplates()) != 2 {
		t.Fatal("no templates should have been stored")
	}
}

func TestUpdateTemplateSignatureGuard(t *testing.T) {
	s := mustOpen(t, newMemStore())
	created, err := s.CreateRuleTemplate("deployer", &apigen.AuthzRuleTemplate{
		Arguments: []*apigen.AuthzTemplateArgument{{ID: 1, Name: "spaces"}},
		Rules: []*apigen.AuthzRule{{
			Permissions: all(),
			Spaces:      &apigen.AuthzSelector{ArgumentID: 1},
			EntityTypes: all(),
			EntityRefs:  all(),
		}},
	}, 1)
	if err != nil {
		t.Fatalf("CreateRuleTemplate: %v", err)
	}
	grant, err := s.CreateGrant(templateGrant(1, created.ID, &apigen.AuthzArgumentBinding{ArgumentID: 1, Values: []int64{2}}))
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	changed := &apigen.AuthzRuleTemplate{
		Arguments: []*apigen.AuthzTemplateArgument{{ID: 2, Name: "spaces"}},
		Rules: []*apigen.AuthzRule{{
			Permissions: all(),
			Spaces:      &apigen.AuthzSelector{ArgumentID: 2},
			EntityTypes: all(),
			EntityRefs:  all(),
		}},
	}
	if _, err := s.UpdateRuleTemplate(created.ID, "deployer", changed, 0); err == nil {
		t.Fatal("changing the argument signature must be rejected while grants bind it")
	}
	renamed := &apigen.AuthzRuleTemplate{
		Arguments: []*apigen.AuthzTemplateArgument{{ID: 1, Name: "space_ids"}},
		Rules: []*apigen.AuthzRule{{
			Permissions: all(),
			Spaces:      &apigen.AuthzSelector{ArgumentID: 1},
			EntityTypes: all(),
			EntityRefs:  all(),
		}},
	}
	if _, err := s.UpdateRuleTemplate(created.ID, "deployer", renamed, 0); err != nil {
		t.Fatalf("renaming an argument must not invalidate bindings: %v", err)
	}
	if !s.HasAccess(1, viewDeployment(2)) {
		t.Fatal("grant should still resolve after the argument rename")
	}
	if err := s.DeleteGrant(1, grant.ID); err != nil {
		t.Fatalf("DeleteGrant: %v", err)
	}
	if _, err := s.UpdateRuleTemplate(created.ID, "deployer", changed, 0); err != nil {
		t.Fatalf("signature change should be allowed once no grants bind it: %v", err)
	}
}

func TestGlobalRuleOverridesAllow(t *testing.T) {
	s := mustOpen(t, newMemStore())
	if _, err := s.CreateGrant(templateGrant(1, ClusterAdminTemplateID)); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	rule, err := s.CreateGlobalRule("no_prod_reveal", &apigen.AuthzGlobalRule{
		Deny:        true,
		Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_REVEAL)}},
		Spaces:      &apigen.AuthzSelector{Include: []int64{3}},
		EntityTypes: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzEntity_AUTHZ_ENTITY_SECRET)}},
		EntityRefs:  all(),
	}, 1)
	if err != nil {
		t.Fatalf("CreateGlobalRule: %v", err)
	}
	reveal := RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_REVEAL,
		SpaceID:    3,
		EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_SECRET,
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
	if err := s.DeleteGlobalRule(rule.ID); err != nil {
		t.Fatalf("DeleteGlobalRule: %v", err)
	}
	if !s.HasAccess(1, reveal) {
		t.Fatal("deleting the global rule should restore access")
	}
	if err := s.DeleteGlobalRule(rule.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: expected ErrNotFound, got %v", err)
	}
}

func TestClusterAdminDelegationLimits(t *testing.T) {
	s := mustOpen(t, newMemStore())
	if _, err := s.CreateGrant(templateGrant(1, ClusterAdminTemplateID)); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	direct := RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_REVEAL,
		SpaceID:    0,
		EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_SECRET,
		EntityID:   4,
	}
	if !s.HasAccess(1, direct) {
		t.Fatal("direct access should cover reveal in the opendeploy space")
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
		EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_DEPLOYMENT,
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
	s := mustOpen(t, newMemStore())
	if _, err := s.CreateGrant(ruleGrant(1, &apigen.AuthzRule{
		Permissions: all(), Spaces: all(), EntityTypes: all(), EntityRefs: all(),
		DelegationAllowed: true,
	})); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if _, err := s.CreateGlobalRule("no_agent_reveal", &apigen.AuthzGlobalRule{
		Deny:          true,
		Permissions:   &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_REVEAL)}},
		Spaces:        all(),
		EntityTypes:   &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzEntity_AUTHZ_ENTITY_SECRET)}},
		EntityRefs:    all(),
		DelegatedOnly: true,
	}, 1); err != nil {
		t.Fatalf("CreateGlobalRule: %v", err)
	}
	reveal := RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_REVEAL,
		SpaceID:    2,
		EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_SECRET,
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
	s := mustOpen(t, newMemStore())
	if _, err := s.CreateGrant(ruleGrant(1, &apigen.AuthzRule{
		Permissions: all(), Spaces: all(), EntityTypes: all(), EntityRefs: all(),
	})); err != nil {
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

	if _, err := s.CreateGrant(templateGrant(2, ClusterAdminTemplateID)); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if !s.HasAccess(2, req) {
		t.Fatal("cluster_admin allows delegation")
	}
}

func TestGlobalRuleAccessCarveOut(t *testing.T) {
	s := mustOpen(t, newMemStore())
	if _, err := s.CreateGrant(templateGrant(1, ClusterAdminTemplateID)); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if _, err := s.CreateGlobalRule("deny_everything", &apigen.AuthzGlobalRule{
		Deny:        true,
		Permissions: all(),
		Spaces:      all(),
		EntityTypes: all(),
		EntityRefs:  all(),
	}, 1); err != nil {
		t.Fatalf("CreateGlobalRule: %v", err)
	}
	if s.HasAccess(1, viewDeployment(1)) {
		t.Fatal("deny-everything rule should deny deployments")
	}
	access := RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_UPDATE,
		SpaceID:    0,
		EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_ACCESS,
	}
	if !s.HasAccess(1, access) {
		t.Fatal("ACCESS checks must skip global rules so the rule stays removable")
	}
}

func TestGlobalRuleValidation(t *testing.T) {
	s := mustOpen(t, newMemStore())
	cases := []struct {
		name     string
		ruleName string
		rule     *apigen.AuthzGlobalRule
	}{
		{"nil", "p", nil},
		{"no name", "", &apigen.AuthzGlobalRule{
			Permissions: all(), Spaces: all(), EntityTypes: all(), EntityRefs: all()}},
		{"missing selector", "p", &apigen.AuthzGlobalRule{
			Permissions: all(), Spaces: all(), EntityTypes: all()}},
		{"argument", "p", &apigen.AuthzGlobalRule{
			Permissions: all(), Spaces: &apigen.AuthzSelector{ArgumentID: 1}, EntityTypes: all(), EntityRefs: all()}},
		{"matches nothing", "p", &apigen.AuthzGlobalRule{
			Permissions: all(), Spaces: &apigen.AuthzSelector{}, EntityTypes: all(), EntityRefs: all()}},
		{"denies access entity", "p", &apigen.AuthzGlobalRule{
			Permissions: all(), Spaces: all(),
			EntityTypes: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzEntity_AUTHZ_ENTITY_ACCESS)}},
			EntityRefs:  all(), Deny: true}},
		{"invalid space", "p", &apigen.AuthzGlobalRule{
			Permissions: all(), Spaces: &apigen.AuthzSelector{Include: []int64{70000}}, EntityTypes: all(), EntityRefs: all()}},
		{"delegated_only on allow", "p", &apigen.AuthzGlobalRule{
			Permissions: all(), Spaces: all(), EntityTypes: all(), EntityRefs: all(),
			DelegatedOnly: true}},
		{"delegation_allowed on deny", "p", &apigen.AuthzGlobalRule{
			Permissions: all(), Spaces: all(), EntityTypes: all(), EntityRefs: all(),
			Deny: true, DelegationAllowed: true}},
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
	if _, err := s.CreateGlobalRule("access_allow", &apigen.AuthzGlobalRule{
		Permissions: all(), Spaces: all(),
		EntityTypes: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzEntity_AUTHZ_ENTITY_ACCESS)}},
		EntityRefs:  all(),
	}, 1); err != nil {
		t.Fatalf("allow rule targeting access: %v", err)
	}
}

func TestReloadPreservesState(t *testing.T) {
	store := newMemStore()
	s := mustOpen(t, store)
	created, err := s.CreateRuleTemplate("deployer", &apigen.AuthzRuleTemplate{
		Arguments: []*apigen.AuthzTemplateArgument{{ID: 1, Name: "spaces"}},
		Rules: []*apigen.AuthzRule{{
			Permissions: all(),
			Spaces:      &apigen.AuthzSelector{ArgumentID: 1},
			EntityTypes: all(),
			EntityRefs:  all(),
		}},
	}, 5)
	if err != nil {
		t.Fatalf("CreateRuleTemplate: %v", err)
	}
	if _, err := s.CreateGrant(templateGrant(1, created.ID, &apigen.AuthzArgumentBinding{ArgumentID: 1, Values: []int64{3}})); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if _, err := s.CreateGlobalRule("no_deletes", &apigen.AuthzGlobalRule{
		Deny:        true,
		Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE)}},
		Spaces:      all(),
		EntityTypes: all(),
		EntityRefs:  all(),
	}, 5); err != nil {
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
	if len(reloaded.RuleTemplates()) != 3 {
		t.Fatalf("expected 3 templates after reload, got %d", len(reloaded.RuleTemplates()))
	}
	grants := reloaded.GrantsForUser(1)
	if len(grants) != 1 || grants[0].TemplateID != created.ID {
		t.Fatalf("unexpected grants after reload: %+v", grants)
	}
}

func TestSpaceVisible(t *testing.T) {
	s := mustOpen(t, newMemStore())
	binding := &apigen.AuthzArgumentBinding{ArgumentID: 1, Values: []int64{3}}
	if _, err := s.CreateGrant(templateGrant(1, SpaceAdminTemplateID, binding)); err != nil {
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
	if _, err := s.CreateGrant(ruleGrant(5, &apigen.AuthzRule{
		Permissions: all(),
		Spaces:      &apigen.AuthzSelector{Include: []int64{6}},
		EntityTypes: all(),
		EntityRefs:  all(),
	})); err != nil {
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
	s := mustOpen(t, newMemStore())
	admin, err := s.CreateGrant(templateGrant(1, ClusterAdminTemplateID))
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	limited, err := s.CreateGrant(templateGrant(2, SpaceAdminTemplateID, &apigen.AuthzArgumentBinding{ArgumentID: 1, Values: []int64{3}}))
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if err := s.DeleteGrant(1, admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("deleting the only admin grant: got %v, want ErrLastAdmin", err)
	}
	if err := s.DeleteGrant(2, limited.ID); err != nil {
		t.Fatalf("deleting a non-admin grant should be allowed: %v", err)
	}
	second, err := s.CreateGrant(templateGrant(2, ClusterAdminTemplateID))
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if err := s.DeleteGrant(1, admin.ID); err != nil {
		t.Fatalf("deleting an admin grant with another admin present: %v", err)
	}
	if err := s.DeleteGrant(2, second.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("the remaining admin grant must be protected: got %v, want ErrLastAdmin", err)
	}
}

func viewUser(userID int64) RequestedAccess {
	return RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_VIEW,
		SpaceID:    0,
		EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_USER,
		EntityID:   userID,
	}
}

func TestSeededDefaultUserVisibility(t *testing.T) {
	s := mustOpen(t, newMemStore())
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
	s := mustOpen(t, newMemStore())
	if _, err := s.CreateGlobalRule("no_roster", &apigen.AuthzGlobalRule{
		Deny:        true,
		Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW)}},
		Spaces:      all(),
		EntityTypes: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzEntity_AUTHZ_ENTITY_USER)}},
		EntityRefs:  all(),
	}, 1); err != nil {
		t.Fatalf("CreateGlobalRule: %v", err)
	}
	if s.HasAccess(9, viewUser(3)) {
		t.Fatal("a global deny must beat the seeded allow rule")
	}
	// A grant does not survive the deny either: denies stay first.
	if _, err := s.CreateGrant(templateGrant(1, ClusterAdminTemplateID)); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if s.HasAccess(1, viewUser(3)) {
		t.Fatal("a global deny must beat grants")
	}
}

func TestGlobalAllowDelegationFlag(t *testing.T) {
	s := mustOpen(t, newMemStore())
	if _, err := s.CreateGlobalRule("humans_view_deployments", &apigen.AuthzGlobalRule{
		Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW)}},
		Spaces:      &apigen.AuthzSelector{Include: []int64{2}},
		EntityTypes: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzEntity_AUTHZ_ENTITY_DEPLOYMENT)}},
		EntityRefs:  all(),
	}, 1); err != nil {
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
	store := newMemStore()
	s := mustOpen(t, store)
	rules := s.GlobalRules()
	if len(rules) != 1 || rules[0].Name != DefaultUserVisibilityRuleName {
		t.Fatalf("expected only the seeded rule, got %+v", rules)
	}
	if err := s.DeleteGlobalRule(rules[0].ID); err != nil {
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
