package authz

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func minimalTemplateSpec() *apigen.AuthzGrantTemplateSpec {
	return &apigen.AuthzGrantTemplateSpec{Rules: []apigen.AuthzTemplateRule{templateRule(allow(false), anyTemplateSelector())}}
}

func TestAuthzStoreRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := state.Open(dbPath)

	svc, err := Open(store)
	if err != nil {
		t.Fatalf("authz.Open: %v", err)
	}
	viewSel := spacesArgTemplateSelector(1)
	viewSel.Permissions = templatePermissions(exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_VIEW))
	created, err := svc.CreateGrantTemplate("deployer", spacesTemplate(1, templateRule(allow(false), viewSel)), 1)
	if err != nil {
		t.Fatalf("CreateGrantTemplate: %v", err)
	}
	if created.ID <= SpaceAdminTemplateID {
		t.Fatalf("custom template id %d should follow the seeded builtins", created.ID)
	}
	grant, err := svc.CreateGrant(templateGrant(7, created.ID, spaceBinding(1, 2)), 1)
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if _, err := svc.CreateGrant(templateGrant(8, ClusterAdminTemplateID), 1); err != nil {
		t.Fatalf("CreateGrant builtin: %v", err)
	}
	rule, err := svc.CreateGlobalRule("no_reveal_space_2", denyRule(false, apigen.AuthzSelector{
		Permissions: exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_REVEAL),
		Spaces:      exactSpaces(2),
		EntityTypes: exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET),
		EntityRefs:  allEntityRefs(),
	}), 1)
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
		EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT,
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
		EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET,
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
	if tg := got.Grant.Value.Template; got.UserID != 7 || tg == nil || tg.TemplateID != created.ID || len(tg.Args) != 1 {
		t.Fatalf("unexpected grant after reopen: %+v", got)
	}
	if row, err := store.Queries().GetAuthzGrant(t.Context(), grant.ID); err != nil || row.Author != 1 || row.TemplateID != created.ID {
		t.Fatalf("grant row = %+v, %v, want the creating operator and the template id", row, err)
	}
	if err := svc.DeleteGrant(7, grant.ID, 0); err != nil {
		t.Fatalf("DeleteGrant: %v", err)
	}
	if svc.HasAccess(7, req) {
		t.Fatal("deleted grant should drop access")
	}
	if err := svc.DeleteGrantTemplate(created.ID, 0); err != nil {
		t.Fatalf("DeleteGrantTemplate: %v", err)
	}
	if _, err := svc.CreateGrantTemplate("deployer", minimalTemplateSpec(), 1); err != nil {
		t.Fatalf("name should be reusable after delete: %v", err)
	}
}

func TestDeleteAuthzRowsThroughStore(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()

	templateID, err := insertGrantTemplate(store, GrantTemplateRow{Name: "plain", CreatedAt: 1000, Blob: minimalTemplateSpec().Encode()})
	if err != nil {
		t.Fatalf("insertGrantTemplate: %v", err)
	}
	grantID, err := insertGrant(store, GrantRow{UserID: 7, CreatedAt: 1000, Blob: sourceBlob(templateSource(templateID))})
	if err != nil {
		t.Fatalf("insertGrant: %v", err)
	}
	if err := deleteGrant(store, grantID, 0); err != nil {
		t.Fatalf("deleteGrant: %v", err)
	}
	if err := deleteGrantTemplate(store, templateID, 0); err != nil {
		t.Fatalf("deleteGrantTemplate: %v", err)
	}
	ruleID, err := insertGlobalRule(store, GlobalRuleRow{Name: "plain", CreatedAt: 1000, Blob: allowRule(false, anySelector()).Encode()})
	if err != nil {
		t.Fatalf("insertGlobalRule: %v", err)
	}
	if err := deleteGlobalRule(store, ruleID, 0); err != nil {
		t.Fatalf("deleteGlobalRule: %v", err)
	}
}

func TestDeletedSeededGlobalRuleStaysDeleted(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := state.Open(dbPath)

	if _, err := Open(store); err != nil {
		t.Fatalf("authz.Open: %v", err)
	}
	rules, err := store.Queries().ListAuthzGlobalRules(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var seededID uint64
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
	rules, err = store.Queries().ListAuthzGlobalRules(t.Context())
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
	id, err := insertGrantTemplate(s, GrantTemplateRow{Name: "template", Blob: minimalTemplateSpec().Encode()})
	check(err)
	check(updateGrantTemplate(s, id, "renamed", minimalTemplateSpec().Encode(), 1, 10))
	grant, err := insertGrant(s, GrantRow{UserID: 7, Blob: sourceBlob(templateSource(id))})
	check(err)
	check(deleteGrant(s, grant, 0))
	check(deleteGrantTemplate(s, id, 0))
	rule, err := insertGlobalRule(s, GlobalRuleRow{Name: "global", Blob: allowRule(false, anySelector()).Encode()})
	check(err)
	check(deleteGlobalRule(s, rule, 0))
}
