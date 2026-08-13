package state

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/authz"
)

func TestAuthzStoreRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := Open(dbPath)

	svc, err := authz.Open(store)
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
	if created.ID <= authz.SpaceAdminTemplateID {
		t.Fatalf("custom template id %d should follow the seeded builtins", created.ID)
	}
	grant, err := svc.CreateGrant(&apigen.AuthzGrantRecord{
		UserID:     7,
		TemplateID: created.ID,
		CreatedBy:  1,
		Grant:      &apigen.AuthzGrant{Args: []*apigen.AuthzArgumentBinding{{ArgumentID: 1, Values: []int64{2}}}},
	})
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if _, err := svc.CreateGrant(&apigen.AuthzGrantRecord{
		UserID:     8,
		TemplateID: authz.ClusterAdminTemplateID,
		Grant:      &apigen.AuthzGrant{},
	}); err != nil {
		t.Fatalf("CreateGrant builtin: %v", err)
	}
	rule, err := svc.CreateGlobalRule("no_reveal_space_2", &apigen.AuthzGlobalRule{
		Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_REVEAL)}},
		Spaces:      &apigen.AuthzSelector{Include: []int64{2}},
		EntityTypes: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzEntity_AUTHZ_ENTITY_SECRET)}},
		EntityRefs:  &apigen.AuthzSelector{Wildcard: true},
	}, 1)
	if err != nil {
		t.Fatalf("CreateGlobalRule: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store = Open(dbPath)
	defer store.Close()
	svc, err = authz.Open(store)
	if err != nil {
		t.Fatalf("authz.Open after reopen: %v", err)
	}
	req := authz.RequestedAccess{
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
	reveal := authz.RequestedAccess{
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
	if got.UserID != 7 || got.TemplateID != created.ID || got.CreatedBy != 1 || got.CreatedAt == 0 || len(got.Grant.Args) != 1 {
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
		t.Fatalf("name should be reusable after delete (partial unique index): %v", err)
	}
}
