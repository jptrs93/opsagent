package webuihandler

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/authz"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func newAccessTestHandler(t *testing.T) (*Handler, apigen.Context) {
	t.Helper()
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { store.Close() })
	authzService, err := authz.Open(store)
	if err != nil {
		t.Fatalf("authz.Open: %v", err)
	}
	if _, err := authzService.CreateGrant(&apigen.AuthzGrant{
		UserID:     1,
		TemplateID: authz.ClusterAdminTemplateID,
		Spec:       &apigen.AuthzGrantSpec{},
	}, 0); err != nil {
		t.Fatalf("seed admin grant: %v", err)
	}
	h := &Handler{Store: store, Queries: store.Queries(), Authz: authzService}
	ctx := apigen.Context{Ctx: context.Background(), User: &apigen.InternalUser{ID: 1, Name: "operator"}}
	return h, ctx
}

func wildcardSelector() *apigen.AuthzSelector {
	return &apigen.AuthzSelector{Wildcard: true}
}

func TestAccessRuleTemplateCRUD(t *testing.T) {
	h, ctx := newAccessTestHandler(t)

	listed, err := h.PostV1AccessRuleTemplatesList(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed.Items) != 2 {
		t.Fatalf("expected the 2 builtins, got %d", len(listed.Items))
	}

	created, err := h.accessRuleTemplatesCreate(ctx, &apigen.AuthzRuleTemplateCreateRequest{
		Name: "viewer",
		Spec: &apigen.AuthzRuleTemplateSpec{Rules: []*apigen.AuthzRule{{
			Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW)}},
			Spaces:      wildcardSelector(),
			EntityTypes: wildcardSelector(),
			EntityRefs:  wildcardSelector(),
		}}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID <= authz.SpaceAdminTemplateID {
		t.Fatalf("unexpected created template: %+v", created)
	}

	if _, err := h.accessRuleTemplatesCreate(ctx, &apigen.AuthzRuleTemplateCreateRequest{
		Name: "viewer",
		Spec: &apigen.AuthzRuleTemplateSpec{Rules: []*apigen.AuthzRule{{
			Permissions: wildcardSelector(),
			Spaces:      wildcardSelector(),
			EntityTypes: wildcardSelector(),
			EntityRefs:  wildcardSelector(),
		}}},
	}); !errors.Is(err, AccessNameTakenErr) {
		t.Fatalf("duplicate name should map to AccessNameTakenErr, got %v", err)
	}

	if _, err := h.accessRuleTemplatesCreate(ctx, &apigen.AuthzRuleTemplateCreateRequest{
		Name: "empty",
		Spec: &apigen.AuthzRuleTemplateSpec{},
	}); err == nil {
		t.Fatal("template without rules should be rejected")
	} else if apiErr, ok := err.(apigen.ApiErr); !ok || apiErr.Code != 400 {
		t.Fatalf("validation failure should map to a 400 ApiErr, got %v", err)
	}

	updated, err := h.accessRuleTemplatesUpdate(ctx, &apigen.AuthzRuleTemplateUpdateRequest{
		ID:   created.ID,
		Name: "viewer_plus",
		Spec: &apigen.AuthzRuleTemplateSpec{Rules: []*apigen.AuthzRule{{
			Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW), int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW_LOGS)}},
			Spaces:      wildcardSelector(),
			EntityTypes: wildcardSelector(),
			EntityRefs:  wildcardSelector(),
		}}},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "viewer_plus" {
		t.Fatalf("update did not apply: %+v", updated)
	}

	if _, err := h.accessRuleTemplatesUpdate(ctx, &apigen.AuthzRuleTemplateUpdateRequest{
		ID:   authz.ClusterAdminTemplateID,
		Name: "cluster_admin",
		Spec: &apigen.AuthzRuleTemplateSpec{Rules: []*apigen.AuthzRule{{
			Permissions: wildcardSelector(),
			Spaces:      wildcardSelector(),
			EntityTypes: wildcardSelector(),
			EntityRefs:  wildcardSelector(),
		}}},
	}); !errors.Is(err, AccessBuiltinErr) {
		t.Fatalf("builtin update should map to AccessBuiltinErr, got %v", err)
	}

	if err := h.PostV1AccessRuleTemplatesDelete(ctx, &apigen.AuthzRuleTemplateDeleteRequest{ID: authz.ClusterAdminTemplateID}); !errors.Is(err, AccessBuiltinErr) {
		t.Fatalf("builtin delete should map to AccessBuiltinErr, got %v", err)
	}
	if err := h.PostV1AccessRuleTemplatesDelete(ctx, &apigen.AuthzRuleTemplateDeleteRequest{ID: created.ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := h.PostV1AccessRuleTemplatesDelete(ctx, &apigen.AuthzRuleTemplateDeleteRequest{ID: created.ID}); !errors.Is(err, AccessNotFoundErr) {
		t.Fatalf("second delete should map to AccessNotFoundErr, got %v", err)
	}
}

func TestAccessGrantCRUD(t *testing.T) {
	h, ctx := newAccessTestHandler(t)

	grant, err := h.accessGrantsCreate(ctx, &apigen.AuthzGrantCreateRequest{
		UserID:     7,
		TemplateID: authz.SpaceAdminTemplateID,
		Spec:       &apigen.AuthzGrantSpec{Args: []*apigen.AuthzArgumentBinding{{ArgumentID: 1, Values: []int64{2}}}},
	})
	if err != nil {
		t.Fatalf("create template grant: %v", err)
	}
	if grant.ID == 0 || grant.UserID != 7 {
		t.Fatalf("grant = %+v", grant)
	}

	if _, err := h.accessGrantsCreate(ctx, &apigen.AuthzGrantCreateRequest{
		UserID:     7,
		TemplateID: authz.SpaceAdminTemplateID,
	}); err == nil {
		t.Fatal("missing bindings should be rejected")
	} else if apiErr, ok := err.(apigen.ApiErr); !ok || apiErr.Code != 400 {
		t.Fatalf("missing bindings should map to a 400 ApiErr, got %v", err)
	}

	direct, err := h.accessGrantsCreate(ctx, &apigen.AuthzGrantCreateRequest{
		UserID: 7,
		Spec: &apigen.AuthzGrantSpec{Rule: &apigen.AuthzRule{
			Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_VIEW)}},
			Spaces:      &apigen.AuthzSelector{Include: []int64{3}},
			EntityTypes: wildcardSelector(),
			EntityRefs:  wildcardSelector(),
		}},
	})
	if err != nil {
		t.Fatalf("create direct grant: %v", err)
	}

	if listed := h.Authz.Grants(); len(listed) != 3 {
		t.Fatalf("expected the seeded admin grant plus 2 created, got %d", len(listed))
	}

	if err := h.PostV1AccessGrantsDelete(ctx, &apigen.AuthzGrantDeleteRequest{UserID: 8, ID: direct.ID}); !errors.Is(err, AccessNotFoundErr) {
		t.Fatalf("wrong-user delete should map to AccessNotFoundErr, got %v", err)
	}
	if err := h.PostV1AccessGrantsDelete(ctx, &apigen.AuthzGrantDeleteRequest{UserID: 7, ID: direct.ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestAccessGlobalRuleCRUD(t *testing.T) {
	h, ctx := newAccessTestHandler(t)

	rule, err := h.accessGlobalRulesCreate(ctx, &apigen.AuthzGlobalRuleCreateRequest{
		Name: "no_reveal",
		Spec: &apigen.AuthzGlobalRuleSpec{
			Permissions: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzVerb_AUTHZ_VERB_REVEAL)}},
			Spaces:      wildcardSelector(),
			EntityTypes: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzEntity_AUTHZ_ENTITY_SECRET)}},
			EntityRefs:  wildcardSelector(),
			Deny:        true,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := h.accessGlobalRulesCreate(ctx, &apigen.AuthzGlobalRuleCreateRequest{
		Name: "targets_access",
		Spec: &apigen.AuthzGlobalRuleSpec{
			Permissions: wildcardSelector(),
			Spaces:      wildcardSelector(),
			EntityTypes: &apigen.AuthzSelector{Include: []int64{int64(apigen.AuthzEntity_AUTHZ_ENTITY_ACCESS)}},
			EntityRefs:  wildcardSelector(),
			Deny:        true,
		},
	}); err == nil {
		t.Fatal("access-entity global deny rule should be rejected")
	} else if apiErr, ok := err.(apigen.ApiErr); !ok || apiErr.Code != 400 {
		t.Fatalf("validation failure should map to a 400 ApiErr, got %v", err)
	}

	listed, err := h.PostV1AccessGlobalRulesList(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed.Items) != 2 {
		t.Fatalf("expected the seeded default rule plus 1 created, got %d", len(listed.Items))
	}

	if err := h.PostV1AccessGlobalRulesDelete(ctx, &apigen.AuthzGlobalRuleDeleteRequest{ID: rule.ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := h.PostV1AccessGlobalRulesDelete(ctx, &apigen.AuthzGlobalRuleDeleteRequest{ID: rule.ID}); !errors.Is(err, AccessNotFoundErr) {
		t.Fatalf("second delete should map to AccessNotFoundErr, got %v", err)
	}
}

func TestAccessChangeSubscription(t *testing.T) {
	h, ctx := newAccessTestHandler(t)

	sub, unsub := h.Store.SubscribeUpdates()
	defer unsub()

	if _, err := h.accessGrantsCreate(ctx, &apigen.AuthzGrantCreateRequest{
		UserID:     3,
		TemplateID: authz.ClusterAdminTemplateID,
	}); err != nil {
		t.Fatalf("create grant: %v", err)
	}
	select {
	case update := <-sub:
		if len(update.Mutations) != 1 || update.Mutations[0].Type() != apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT || update.Mutations[0].Entity().AuthzGrant.UserID != 3 {
			t.Fatalf("expected grant transaction, got %+v", update)
		}
	default:
		t.Fatal("grant creation should notify subscribers")
	}
}
