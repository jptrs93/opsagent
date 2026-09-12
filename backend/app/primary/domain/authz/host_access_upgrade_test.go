package authz

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

// The builtins shipped before host permissions existed. Keep this independent
// of builtinTemplates so the test exercises upgrading a persisted old policy.
func builtinBeforeHostPermissions(operatorSpaces, agentSpaces *apigen.AuthzSelector) *apigen.AuthzRuleTemplate {
	return &apigen.AuthzRuleTemplate{Rules: []*apigen.AuthzRule{
		{Permissions: all(), Spaces: operatorSpaces, EntityTypes: all(), EntityRefs: all()},
		{
			Permissions: &apigen.AuthzSelector{Wildcard: true, Exclude: []int64{5}},
			Spaces:      agentSpaces,
			EntityTypes: &apigen.AuthzSelector{Wildcard: true, Exclude: []int64{3}},
			EntityRefs:  all(), DelegationAllowed: true,
		},
		{
			Permissions: &apigen.AuthzSelector{Include: []int64{4, 1}},
			Spaces:      agentSpaces,
			EntityTypes: &apigen.AuthzSelector{Include: []int64{3}},
			EntityRefs:  all(), DelegationAllowed: true,
		},
	}}
}

func TestBuiltinHostAccessUpgrade(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := state.Open(dbPath)
	cluster := builtinBeforeHostPermissions(all(), &apigen.AuthzSelector{Wildcard: true, Exclude: []int64{0}})
	space := builtinBeforeHostPermissions(&apigen.AuthzSelector{ArgumentID: 1}, &apigen.AuthzSelector{ArgumentID: 1})
	space.Arguments = []*apigen.AuthzTemplateArgument{{ID: 1, Name: "spaces"}}
	for _, old := range []struct {
		id       int64
		name     string
		template *apigen.AuthzRuleTemplate
	}{
		{ClusterAdminTemplateID, "cluster_admin", cluster}, {SpaceAdminTemplateID, "space_admin", space},
	} {
		if err := upsertBuiltinRuleTemplate(store, old.id, old.name, old.template.Encode()); err != nil {
			t.Fatal(err)
		}
	}
	for _, old := range []GrantRow{
		{UserID: 1, TemplateID: ClusterAdminTemplateID, Blob: (&apigen.AuthzGrant{}).Encode()},
		{UserID: 2, TemplateID: SpaceAdminTemplateID, Blob: (&apigen.AuthzGrant{Args: []*apigen.AuthzArgumentBinding{{ArgumentID: 1, Values: []int64{2}}}}).Encode()},
	} {
		if _, err := insertGrant(store, old); err != nil {
			t.Fatal(err)
		}
	}
	grantsBefore, err := listGrants(store.Queries())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = state.Open(dbPath)
	svc := mustOpen(t, store)
	assertHostAccessDefaults(t, svc)
	grantsAfter, err := listGrants(store.Queries())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(grantsBefore, grantsAfter) {
		t.Fatal("upgrade rewrote existing grants")
	}
	updated, err := store.Queries().ListLatestAuthzRuleTemplateEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 2 {
		t.Fatalf("templates = %d, want 2", len(updated))
	}
	for _, event := range updated {
		if event.Version != 2 || event.EventType != pq.EventUpdate {
			t.Fatalf("template %d: version %d type %v, want one update", event.TemplateID, event.Version, event.EventType)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = state.Open(dbPath)
	defer store.Close()
	svc = mustOpen(t, store)
	assertHostAccessDefaults(t, svc)
	again, err := store.Queries().ListLatestAuthzRuleTemplateEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(updated, again) {
		t.Fatal("second startup wrote additional template events")
	}
}

func assertHostAccessDefaults(t *testing.T, svc *Service) {
	t.Helper()
	for _, user := range []int64{1, 2} {
		for _, delegated := range []bool{false, true} {
			for _, space := range []int64{0, 2, 3} {
				for _, verb := range []apigen.AuthzVerb{apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_MOUNTS, apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_NETWORK} {
					req := RequestedAccess{Verb: verb, SpaceID: space, EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_DEPLOYMENT, EntityID: 7, Delegated: delegated}
					want := user == 1 && !delegated
					if got := svc.HasAccess(user, req); got != want {
						t.Fatalf("user %d access %+v = %v, want %v", user, req, got, want)
					}
				}
			}
			if !svc.HasAccess(user, RequestedAccess{Verb: apigen.AuthzVerb_AUTHZ_VERB_UPDATE, SpaceID: 2,
				EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_DEPLOYMENT, EntityID: 7, Delegated: delegated}) {
				t.Fatalf("user %d delegated=%v lost ordinary deployment updates", user, delegated)
			}
		}
	}
}
