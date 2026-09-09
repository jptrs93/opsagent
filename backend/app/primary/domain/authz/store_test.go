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
	created, err := svc.CreateRuleTemplate("deployer", &apigen.AuthzRuleTemplate{
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
	grant, err := svc.CreateGrant(&apigen.AuthzGrantRecord{
		UserID:     7,
		TemplateID: created.ID,
		Author:     1,
		Grant:      &apigen.AuthzGrant{Args: []*apigen.AuthzArgumentBinding{{ArgumentID: 1, Values: []int64{2}}}},
	})
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if _, err := svc.CreateGrant(&apigen.AuthzGrantRecord{
		UserID:     8,
		TemplateID: ClusterAdminTemplateID,
		Grant:      &apigen.AuthzGrant{},
	}); err != nil {
		t.Fatalf("CreateGrant builtin: %v", err)
	}
	rule, err := svc.CreateGlobalRule("no_reveal_space_2", &apigen.AuthzGlobalRule{
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
	if err := svc.DeleteGlobalRule(rule.ID); err != nil {
		t.Fatalf("DeleteGlobalRule: %v", err)
	}
	if !svc.HasAccess(8, reveal) {
		t.Fatal("deleting the global rule should restore cluster_admin access")
	}
	got, err := svc.Grant(7, grant.ID)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if got.UserID != 7 || got.TemplateID != created.ID || got.Author != 1 || got.CreatedAt == 0 || len(got.Grant.Args) != 1 {
		t.Fatalf("unexpected grant after reopen: %+v", got)
	}
	if err := svc.DeleteGrant(7, grant.ID); err != nil {
		t.Fatalf("DeleteGrant: %v", err)
	}
	if svc.HasAccess(7, req) {
		t.Fatal("deleted grant should drop access")
	}
	if err := svc.DeleteRuleTemplate(created.ID); err != nil {
		t.Fatalf("DeleteRuleTemplate: %v", err)
	}
	if _, err := svc.CreateRuleTemplate("deployer", &apigen.AuthzRuleTemplate{
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
	if err := deleteGrant(store, grantID); err != nil {
		t.Fatalf("DeleteAuthzGrant with empty blob: %v", err)
	}
	templateID, err := insertRuleTemplate(store, RuleTemplateRow{Name: "empty", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("InsertAuthzRuleTemplate: %v", err)
	}
	if err := deleteRuleTemplate(store, templateID); err != nil {
		t.Fatalf("DeleteAuthzRuleTemplate with empty blob: %v", err)
	}
	ruleID, err := insertGlobalRule(store, GlobalRuleRow{Name: "empty", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("InsertAuthzGlobalRule: %v", err)
	}
	if err := deleteGlobalRule(store, ruleID); err != nil {
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
	if err := deleteGlobalRule(store, seededID); err != nil {
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
	if err := db.QueryRow(`SELECT COUNT(*) FROM global_access_rule_event_log WHERE name = ? AND event_type = 3`,
		DefaultUserVisibilityRuleName).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 1 {
		t.Fatalf("tombstone rows = %d, want 1", tombstones)
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
	id, err := insertRuleTemplate(s, RuleTemplateRow{Name: "template", Blob: (&apigen.AuthzRuleTemplate{}).Encode()})
	check(err)
	check(updateRuleTemplate(s, id, "renamed", (&apigen.AuthzRuleTemplate{}).Encode(), 1, 10))
	grant, err := insertGrant(s, GrantRow{UserID: 7, TemplateID: id, Blob: (&apigen.AuthzGrant{}).Encode()})
	check(err)
	check(deleteGrant(s, grant))
	check(deleteRuleTemplate(s, id))
	rule, err := insertGlobalRule(s, GlobalRuleRow{Name: "global", Blob: (&apigen.AuthzGlobalRule{}).Encode()})
	check(err)
	check(deleteGlobalRule(s, rule))
}
