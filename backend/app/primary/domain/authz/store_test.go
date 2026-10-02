package authz

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestAuthzStoreRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := state.Open(dbPath)

	svc, err := Open(store)
	if err != nil {
		t.Fatalf("authz.Open: %v", err)
	}
	created, err := svc.CreateRuleTemplate("deployer", &apigen.AuthzRuleTemplateSpec{
		Arguments: []*apigen.AuthzTemplateArgument{{ID: 1, Name: "spaces"}},
		Rules: []*apigen.AuthzRule{{
			Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW)}},
			Spaces:      &apigen.AuthzSelector{ArgumentID: 1},
			EntityTypes: &apigen.AuthzSelector{Wildcard: true},
			EntityRefs:  &apigen.AuthzSelector{Wildcard: true},
		}},
	}, 1)
	if err != nil {
		t.Fatalf("CreateRuleTemplate: %v", err)
	}
	if created.ID <= SpaceAdminTemplateID {
		t.Fatalf("custom template id %d should follow the seeded builtins", created.ID)
	}
	grant, err := svc.CreateGrant(&apigen.AuthzGrant{
		UserID:     7,
		TemplateID: created.ID,
		Spec:       &apigen.AuthzGrantSpec{Args: []*apigen.AuthzArgumentBinding{{ArgumentID: 1, Values: []int64{2}}}},
	}, 1)
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if _, err := svc.CreateGrant(&apigen.AuthzGrant{
		UserID:     8,
		TemplateID: ClusterAdminTemplateID,
		Spec:       &apigen.AuthzGrantSpec{},
	}, 1); err != nil {
		t.Fatalf("CreateGrant builtin: %v", err)
	}
	rule, err := svc.CreateGlobalRule("no_reveal_space_2", &apigen.AuthzGlobalRuleSpec{
		Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_REVEAL)}},
		Spaces:      &apigen.AuthzSelector{Include: []int64{2}},
		EntityTypes: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzEntity_AUTHZ_ENTITY_SECRET)}},
		EntityRefs:  &apigen.AuthzSelector{Wildcard: true},
		Deny:        true,
	}, 1)
	if err != nil {
		t.Fatalf("CreateGlobalRule: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store = state.Open(dbPath)
	defer store.Close()
	svc, err = Open(store)
	if err != nil {
		t.Fatalf("authz.Open after reopen: %v", err)
	}
	req := RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_VIEW,
		SpaceID:    2,
		EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_DEPLOYMENT,
		EntityID:   4,
	}
	if !svc.HasAccess(7, req) {
		t.Fatal("template grant should survive a database reopen")
	}
	other := req
	other.SpaceID = 3
	if svc.HasAccess(7, other) {
		t.Fatal("reopen must not widen access")
	}
	if !svc.HasAccess(8, other) {
		t.Fatal("cluster_admin grant should survive a database reopen")
	}
	reveal := RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_REVEAL,
		SpaceID:    2,
		EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_SECRET,
		EntityID:   4,
	}
	if svc.HasAccess(8, reveal) {
		t.Fatal("global rule should survive a database reopen")
	}
	if err := svc.DeleteGlobalRule(rule.ID, 0); err != nil {
		t.Fatalf("DeleteGlobalRule: %v", err)
	}
	if !svc.HasAccess(8, reveal) {
		t.Fatal("deleting the global rule should restore cluster_admin access")
	}
	got, err := svc.Grant(7, grant.ID)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if got.UserID != 7 || got.TemplateID != created.ID || len(got.Spec.Args) != 1 {
		t.Fatalf("unexpected grant after reopen: %+v", got)
	}
	if row, err := store.Queries().GetAuthzGrant(t.Context(), grant.ID); err != nil || row.Author != 1 {
		t.Fatalf("grant row author = %d, %v, want the creating operator", row.Author, err)
	}
	if err := svc.DeleteGrant(7, grant.ID, 0); err != nil {
		t.Fatalf("DeleteGrant: %v", err)
	}
	if svc.HasAccess(7, req) {
		t.Fatal("deleted grant should drop access")
	}
	if err := svc.DeleteRuleTemplate(created.ID, 0); err != nil {
		t.Fatalf("DeleteRuleTemplate: %v", err)
	}
	if _, err := svc.CreateRuleTemplate("deployer", &apigen.AuthzRuleTemplateSpec{
		Rules: []*apigen.AuthzRule{{
			Permissions: &apigen.AuthzSelector{Wildcard: true},
			Spaces:      &apigen.AuthzSelector{Wildcard: true},
			EntityTypes: &apigen.AuthzSelector{Wildcard: true},
			EntityRefs:  &apigen.AuthzSelector{Wildcard: true},
		}},
	}, 1); err != nil {
		t.Fatalf("name should be reusable after delete: %v", err)
	}
}

func TestDeleteAuthzRowsWithEmptyBlobs(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()

	grantID, err := insertGrant(store, GrantRow{UserID: 7, TemplateID: 1, CreatedAt: 1000})
	if err != nil {
		t.Fatalf("InsertAuthzGrant: %v", err)
	}
	if err := deleteGrant(store, grantID, 0); err != nil {
		t.Fatalf("DeleteAuthzGrant with empty blob: %v", err)
	}
	templateID, err := insertRuleTemplate(store, RuleTemplateRow{Name: "empty", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("InsertAuthzRuleTemplate: %v", err)
	}
	if err := deleteRuleTemplate(store, templateID, 0); err != nil {
		t.Fatalf("DeleteAuthzRuleTemplate with empty blob: %v", err)
	}
	ruleID, err := insertGlobalRule(store, GlobalRuleRow{Name: "empty", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("InsertAuthzGlobalRule: %v", err)
	}
	if err := deleteGlobalRule(store, ruleID, 0); err != nil {
		t.Fatalf("DeleteAuthzGlobalRule with empty blob: %v", err)
	}
}

func TestDeletedSeededGlobalRuleStaysDeleted(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := state.Open(dbPath)

	if _, err := Open(store); err != nil {
		t.Fatalf("authz.Open: %v", err)
	}
	rules, err := listGlobalRules(store.Queries())
	if err != nil {
		t.Fatal(err)
	}
	var seededID int64
	for _, rule := range rules {
		if rule.Name == DefaultUserVisibilityRuleName {
			seededID = rule.ID
		}
	}
	if seededID == 0 {
		t.Fatalf("seeded rule missing from %+v", rules)
	}
	if err := deleteGlobalRule(store, seededID, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = state.Open(dbPath)
	defer store.Close()
	if _, err := Open(store); err != nil {
		t.Fatalf("authz.Open after delete: %v", err)
	}
	rules, err = listGlobalRules(store.Queries())
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules {
		if rule.Name == DefaultUserVisibilityRuleName {
			t.Fatalf("deleted seeded rule was resurrected: %+v", rule)
		}
	}

	db := sqlitedb.MustOpen(dbPath)
	defer db.Close()
	var tombstones int
	if err := db.QueryRow(`SELECT COUNT(*) FROM write_event_mutations WHERE entity_type = ? AND entity_id = ? AND op = 3`,
		int64(apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE), seededID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 1 {
		t.Fatalf("logged deletes = %d, want 1", tombstones)
	}
}

func TestAuthzWriterPublicationMatchesPersistedRows(t *testing.T) {
	s := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		statetest.AssertUpdateMatchesRows(t, s, <-sub)
	}
	id, err := insertRuleTemplate(s, RuleTemplateRow{Name: "template", Blob: (&apigen.AuthzRuleTemplateSpec{}).Encode()})
	check(err)
	check(updateRuleTemplate(s, id, "renamed", (&apigen.AuthzRuleTemplateSpec{}).Encode(), 1, 10))
	grant, err := insertGrant(s, GrantRow{UserID: 7, TemplateID: id, Blob: (&apigen.AuthzGrantSpec{}).Encode()})
	check(err)
	check(deleteGrant(s, grant, 0))
	check(deleteRuleTemplate(s, id, 0))
	rule, err := insertGlobalRule(s, GlobalRuleRow{Name: "global", Blob: (&apigen.AuthzGlobalRuleSpec{}).Encode()})
	check(err)
	check(deleteGlobalRule(s, rule, 0))
}
