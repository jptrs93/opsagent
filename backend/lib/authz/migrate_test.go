package authz

import (
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func oldDefaultUserVisibilityRule() *apigen.AuthzGlobalRule {
	return &apigen.AuthzGlobalRule{
		DelegationAllowed: true,
		Permissions:       &apigen.AuthzSelector{Include: []int64{1}},
		Spaces:            &apigen.AuthzSelector{Include: []int64{0}},
		EntityTypes:       &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzEntity_AUTHZ_ENTITY_USER)}},
		EntityRefs:        &apigen.AuthzSelector{Wildcard: true},
	}
}

func visibilityRulePermissions(t *testing.T, store *memStore) []int64 {
	t.Helper()
	rows, _ := store.ListAuthzGlobalRules()
	for _, row := range rows {
		if row.Name != DefaultUserVisibilityRuleName {
			continue
		}
		rule, err := apigen.DecodeAuthzGlobalRule(row.Blob)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		return rule.Permissions.Include
	}
	t.Fatalf("no %s rule", DefaultUserVisibilityRuleName)
	return nil
}

func TestVerbRenumberRewritesOldSeededRule(t *testing.T) {
	store := newMemStore()
	store.MustSetLocalKV(defaultUserVisibilityMarker, []byte("1"))
	if _, err := store.InsertAuthzGlobalRule(GlobalRuleRow{
		Name: DefaultUserVisibilityRuleName,
		Blob: oldDefaultUserVisibilityRule().Encode(),
	}); err != nil {
		t.Fatal(err)
	}

	mustOpen(t, store)

	got := visibilityRulePermissions(t, store)
	want := int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("permissions = %v, want [%d]", got, want)
	}
	// Re-open: marker set, nothing left to rewrite.
	mustOpen(t, store)
	if got := visibilityRulePermissions(t, store); len(got) != 1 || got[0] != want {
		t.Fatalf("after reopen permissions = %v, want [%d]", got, want)
	}
}

func TestVerbRenumberRespectsOptOut(t *testing.T) {
	store := newMemStore()
	store.MustSetLocalKV(defaultUserVisibilityMarker, []byte("1"))

	mustOpen(t, store)

	if rows, _ := store.ListAuthzGlobalRules(); len(rows) != 0 {
		t.Fatalf("rules = %v, want none resurrected", rows)
	}
}

func TestVerbRenumberFreshInstall(t *testing.T) {
	store := newMemStore()

	mustOpen(t, store)

	got := visibilityRulePermissions(t, store)
	want := int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("permissions = %v, want [%d]", got, want)
	}
}
