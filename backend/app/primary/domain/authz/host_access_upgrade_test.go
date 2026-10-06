package authz

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

// The builtins shipped before host permissions existed. Keep this independent
// of builtinTemplates so the test exercises upgrading a persisted old policy.
func builtinBeforeHostPermissions(operatorSpaces, agentSpaces apigen.AuthzTemplateSpaceSelector) *apigen.AuthzGrantTemplateSpec {
	return &apigen.AuthzGrantTemplateSpec{Rules: []apigen.AuthzTemplateRule{
		templateRule(allow(false), apigen.AuthzTemplateSelector{
			Permissions: templatePermissions(allVerbsExcluding()),
			Spaces:      operatorSpaces,
			EntityTypes: templateEntityTypes(allEntityTypesExcluding()),
			EntityRefs:  templateEntityRefs(allEntityRefs()),
		}),
		templateRule(allow(true), apigen.AuthzTemplateSelector{
			Permissions: templatePermissions(allVerbsExcluding(apigen.AuthzVerb_AUTHZ_VERB_VIEW_LOGS)),
			Spaces:      agentSpaces,
			EntityTypes: templateEntityTypes(allEntityTypesExcluding(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET)),
			EntityRefs:  templateEntityRefs(allEntityRefs()),
		}),
		templateRule(allow(true), apigen.AuthzTemplateSelector{
			Permissions: templatePermissions(exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_VIEW, apigen.AuthzVerb_AUTHZ_VERB_CREATE)),
			Spaces:      agentSpaces,
			EntityTypes: templateEntityTypes(exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET)),
			EntityRefs:  templateEntityRefs(allEntityRefs()),
		}),
	}}
}

func TestBuiltinHostAccessUpgrade(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := state.Open(dbPath)
	cluster := builtinBeforeHostPermissions(templateSpaces(allSpacesExcluding()), templateSpaces(allSpacesExcluding(0)))
	space := builtinBeforeHostPermissions(templateSpacesArgument(1), templateSpacesArgument(1))
	space.Arguments = []apigen.AuthzTemplateArgument{spaceArg(1, "spaces")}
	for _, old := range []struct {
		id       uint64
		name     string
		template *apigen.AuthzGrantTemplateSpec
	}{
		{ClusterAdminTemplateID, "cluster_admin", cluster}, {SpaceAdminTemplateID, "space_admin", space},
	} {
		if err := upsertBuiltinGrantTemplate(store, old.id, old.name, old.template.Encode()); err != nil {
			t.Fatal(err)
		}
	}
	for _, old := range []GrantRow{
		{UserID: 1, Blob: sourceBlob(templateSource(ClusterAdminTemplateID))},
		{UserID: 2, Blob: sourceBlob(templateSource(SpaceAdminTemplateID, spaceBinding(1, 2)))},
	} {
		if _, err := insertGrant(store, old); err != nil {
			t.Fatal(err)
		}
	}
	grantsBefore, err := store.Queries().ListAuthzGrants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	templatesBefore, err := store.Queries().ListAuthzGrantTemplates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seqBefore, err := store.Queries().GetGlobalSeq(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = state.Open(dbPath)
	svc := mustOpen(t, store)
	assertHostAccessDefaults(t, svc)
	grantsAfter, err := store.Queries().ListAuthzGrants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(grantsBefore, grantsAfter) {
		t.Fatal("upgrade rewrote existing grants")
	}
	updated, err := store.Queries().ListAuthzGrantTemplates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 2 || len(templatesBefore) != 2 {
		t.Fatalf("templates = %d, want 2", len(updated))
	}
	for i, row := range updated {
		before := templatesBefore[i]
		if row.ID != before.ID || row.Seq <= seqBefore || row.CreatedTime != before.CreatedTime || !row.Builtin || bytes.Equal(row.DataBlob, before.DataBlob) {
			t.Fatalf("template %d: %+v after %+v, want one update keeping the creation time", row.ID, row, before)
		}
		if m, err := store.Queries().LatestMutation(context.Background(), apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT_TEMPLATE, row.ID); err != nil || m.Kind() != apigen.AuthzVerb_AUTHZ_VERB_UPDATE {
			t.Fatalf("template %d: newest logged mutation %v %v, want an update", row.ID, m, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = state.Open(dbPath)
	defer store.Close()
	svc = mustOpen(t, store)
	assertHostAccessDefaults(t, svc)
	again, err := store.Queries().ListAuthzGrantTemplates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(updated, again) {
		t.Fatal("second startup wrote the templates again")
	}
}

func assertHostAccessDefaults(t *testing.T, svc *Service) {
	t.Helper()
	for _, user := range []uint64{1, 2} {
		for _, delegated := range []bool{false, true} {
			for _, space := range []uint64{0, 2, 3} {
				for _, verb := range []apigen.AuthzVerb{apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_MOUNTS, apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_NETWORK} {
					req := RequestedAccess{Verb: verb, SpaceID: space, EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT, EntityID: 7, Delegated: delegated}
					want := user == 1 && !delegated
					if got := svc.HasAccess(user, req); got != want {
						t.Fatalf("user %d access %+v = %v, want %v", user, req, got, want)
					}
				}
			}
			if !svc.HasAccess(user, RequestedAccess{Verb: apigen.AuthzVerb_AUTHZ_VERB_UPDATE, SpaceID: 2,
				EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT, EntityID: 7, Delegated: delegated}) {
				t.Fatalf("user %d delegated=%v lost ordinary deployment updates", user, delegated)
			}
		}
	}
}
