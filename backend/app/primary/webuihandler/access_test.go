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
	if _, err := authzService.CreateGrant(clusterAdminGrant(1), 0); err != nil {
		t.Fatalf("seed admin grant: %v", err)
	}
	h := &Handler{Store: store, Queries: store.Queries(), Authz: authzService}
	ctx := apigen.Context{Ctx: context.Background(), User: &apigen.User{ID: 1, Name: "operator"}}
	return h, ctx
}

func anyTemplateSelector() apigen.AuthzTemplateSelector {
	return templateSelectorOf(anySelector())
}

func templateSelectorOf(sel apigen.AuthzSelector) apigen.AuthzTemplateSelector {
	return apigen.AuthzTemplateSelector{
		Permissions: apigen.AuthzTemplatePermissionSelector{Value: apigen.AuthzTemplatePermissionSelectorValueOneof{Selector: &sel.Permissions}},
		Spaces:      apigen.AuthzTemplateSpaceSelector{Value: apigen.AuthzTemplateSpaceSelectorValueOneof{Selector: &sel.Spaces}},
		EntityTypes: apigen.AuthzTemplateEntityTypeSelector{Value: apigen.AuthzTemplateEntityTypeSelectorValueOneof{Selector: &sel.EntityTypes}},
		EntityRefs:  apigen.AuthzTemplateEntityRefSelector{Value: apigen.AuthzTemplateEntityRefSelectorValueOneof{Selector: &sel.EntityRefs}},
	}
}

func templateRules(rules ...apigen.AuthzTemplateRule) apigen.AuthzGrantTemplateSpec {
	return apigen.AuthzGrantTemplateSpec{Rules: rules}
}

func allowTemplateRule(sel apigen.AuthzSelector) apigen.AuthzTemplateRule {
	return apigen.AuthzTemplateRule{Effect: allowEffect(true), Selector: templateSelectorOf(sel)}
}

func TestAccessGrantTemplateCRUD(t *testing.T) {
	h, ctx := newAccessTestHandler(t)

	listed, err := h.PostV1AccessGrantTemplatesList(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed.Items) != 2 {
		t.Fatalf("expected the 2 builtins, got %d", len(listed.Items))
	}

	viewer := anySelector()
	viewer.Permissions = exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_VIEW)
	created, err := h.accessGrantTemplatesCreate(ctx, &apigen.AuthzGrantTemplateCreateRequest{
		Name: "viewer",
		Spec: templateRules(allowTemplateRule(viewer)),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID <= authz.SpaceAdminTemplateID {
		t.Fatalf("unexpected created template: %+v", created)
	}

	if _, err := h.accessGrantTemplatesCreate(ctx, &apigen.AuthzGrantTemplateCreateRequest{
		Name: "viewer",
		Spec: templateRules(allowTemplateRule(anySelector())),
	}); !errors.Is(err, AccessNameTakenErr) {
		t.Fatalf("duplicate name should map to AccessNameTakenErr, got %v", err)
	}

	if _, err := h.accessGrantTemplatesCreate(ctx, &apigen.AuthzGrantTemplateCreateRequest{
		Name: "empty",
		Spec: apigen.AuthzGrantTemplateSpec{},
	}); err == nil {
		t.Fatal("template without rules should be rejected")
	} else if apiErr, ok := err.(apigen.ApiErr); !ok || apiErr.Code != 400 {
		t.Fatalf("validation failure should map to a 400 ApiErr, got %v", err)
	}

	viewerPlus := anySelector()
	viewerPlus.Permissions = exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_VIEW, apigen.AuthzVerb_AUTHZ_VERB_VIEW_LOGS)
	updated, err := h.accessGrantTemplatesUpdate(ctx, &apigen.AuthzGrantTemplateUpdateRequest{
		ID:   created.ID,
		Name: "viewer_plus",
		Spec: templateRules(allowTemplateRule(viewerPlus)),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "viewer_plus" {
		t.Fatalf("update did not apply: %+v", updated)
	}

	if _, err := h.accessGrantTemplatesUpdate(ctx, &apigen.AuthzGrantTemplateUpdateRequest{
		ID:   authz.ClusterAdminTemplateID,
		Name: "cluster_admin",
		Spec: templateRules(allowTemplateRule(anySelector())),
	}); !errors.Is(err, AccessBuiltinErr) {
		t.Fatalf("builtin update should map to AccessBuiltinErr, got %v", err)
	}

	if err := h.PostV1AccessGrantTemplatesDelete(ctx, &apigen.AuthzGrantTemplateDeleteRequest{ID: authz.ClusterAdminTemplateID}); !errors.Is(err, AccessBuiltinErr) {
		t.Fatalf("builtin delete should map to AccessBuiltinErr, got %v", err)
	}
	if err := h.PostV1AccessGrantTemplatesDelete(ctx, &apigen.AuthzGrantTemplateDeleteRequest{ID: created.ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := h.PostV1AccessGrantTemplatesDelete(ctx, &apigen.AuthzGrantTemplateDeleteRequest{ID: created.ID}); !errors.Is(err, AccessNotFoundErr) {
		t.Fatalf("second delete should map to AccessNotFoundErr, got %v", err)
	}
}

func TestAccessGrantCRUD(t *testing.T) {
	h, ctx := newAccessTestHandler(t)

	grant, err := h.accessGrantsCreate(ctx, &apigen.AuthzGrantCreateRequest{
		UserID: 7,
		Grant:  templateSource(authz.SpaceAdminTemplateID, spaceBinding(1, 2)),
	})
	if err != nil {
		t.Fatalf("create template grant: %v", err)
	}
	if grant.ID == 0 || grant.UserID != 7 {
		t.Fatalf("grant = %+v", grant)
	}

	if _, err := h.accessGrantsCreate(ctx, &apigen.AuthzGrantCreateRequest{
		UserID: 7,
		Grant:  templateSource(authz.SpaceAdminTemplateID),
	}); err == nil {
		t.Fatal("missing bindings should be rejected")
	} else if apiErr, ok := err.(apigen.ApiErr); !ok || apiErr.Code != 400 {
		t.Fatalf("missing bindings should map to a 400 ApiErr, got %v", err)
	}

	directSel := anySelector()
	directSel.Permissions = exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_VIEW)
	directSel.Spaces = exactSpaces(3)
	direct, err := h.accessGrantsCreate(ctx, &apigen.AuthzGrantCreateRequest{
		UserID: 7,
		Grant:  apigen.AuthzGrantSource{Value: apigen.AuthzGrantSourceValueOneof{Rule: allowRule(directSel)}},
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

	noReveal := anySelector()
	noReveal.Permissions = exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_REVEAL)
	noReveal.EntityTypes = exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET)
	rule, err := h.accessGlobalRulesCreate(ctx, &apigen.AuthzGlobalRuleCreateRequest{
		Name: "no_reveal",
		Rule: *denyRule(noReveal),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	targetsAccess := anySelector()
	targetsAccess.EntityTypes = exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ACCESS)
	if _, err := h.accessGlobalRulesCreate(ctx, &apigen.AuthzGlobalRuleCreateRequest{
		Name: "targets_access",
		Rule: *denyRule(targetsAccess),
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
		UserID: 3,
		Grant:  templateSource(authz.ClusterAdminTemplateID),
	}); err != nil {
		t.Fatalf("create grant: %v", err)
	}
	select {
	case update := <-sub:
		if len(update.Mutations) != 1 || update.Mutations[0].Type() != apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT || update.Mutations[0].Entity().Value.AuthzGrant.UserID != 3 {
			t.Fatalf("expected grant transaction, got %+v", update)
		}
	default:
		t.Fatal("grant creation should notify subscribers")
	}
}
