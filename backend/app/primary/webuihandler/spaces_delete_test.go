package webuihandler

import (
	"errors"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/assets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/authz"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
)

// A space delete is refused while anything still lives in the space or names
// it by id, and the refusal says what stands in the way.
func TestSpaceDeleteIsRefusedWhileInUse(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	h.Assets = testAssetStore(t, h)
	node := ensureTestNode(h.Store, "primary", "primary-id")
	admin := enforceCtx(1, false)

	cases := []struct {
		name string
		want string
		fill func(t *testing.T, space *apigen.Space)
	}{
		{"deployment", "1 deployment", func(t *testing.T, space *apigen.Space) {
			if _, err := h.deploymentsCreate(admin, &apigen.DeploymentCreateRequest{
				SpaceID: space.ID, Name: "web",
				Scheduling: apigen.DedicatedScheduling(false, node.ID),
				Spec:       remoteDeploymentSpec("nginx", hostNetworking()),
			}); err != nil {
				t.Fatalf("create deployment: %v", err)
			}
		}},
		{"secret", "1 secret", func(t *testing.T, space *apigen.Space) {
			if _, err := h.secretsCreate(admin, &apigen.SecretCreateRequest{Key: "token", SpaceID: space.ID, Value: []byte("x")}); err != nil {
				t.Fatalf("create secret: %v", err)
			}
		}},
		{"config", "2 configs", func(t *testing.T, space *apigen.Space) {
			for _, key := range []string{"a.conf", "b.conf"} {
				if _, err := values.CreateConfig(h.Store, key, space.ID, 0, 0, "v"); err != nil {
					t.Fatalf("create config: %v", err)
				}
			}
		}},
		{"value directory", "1 value directory", func(t *testing.T, space *apigen.Space) {
			if _, err := values.CreateDirectory(h.Store, space.ID, 0, "dir", 0); err != nil {
				t.Fatalf("create value directory: %v", err)
			}
		}},
		{"asset", "1 asset", func(t *testing.T, space *apigen.Space) {
			if _, err := createTestAsset(h, admin, "bundle.tar", space.ID, 0, []byte("payload")); err != nil {
				t.Fatalf("create asset: %v", err)
			}
		}},
		{"asset directory", "1 asset directory", func(t *testing.T, space *apigen.Space) {
			if _, err := assets.CreateDirectory(h.Store, space.ID, 0, "dir", 0); err != nil {
				t.Fatalf("create asset directory: %v", err)
			}
		}},
		{"network policy", "1 network policy", func(t *testing.T, space *apigen.Space) {
			if _, err := h.networkPoliciesCreate(admin, allowCreateRequest(spacePeer(nodes.DefaultSpaceID), spacePeer(space.ID))); err != nil {
				t.Fatalf("create network policy: %v", err)
			}
		}},
		{"template grant binding", "1 access grant", func(t *testing.T, space *apigen.Space) {
			if _, err := h.Authz.CreateGrant(templateGrant(2, authz.SpaceAdminTemplateID, spaceBinding(1, space.ID)), 0); err != nil {
				t.Fatalf("create grant: %v", err)
			}
		}},
		{"rule grant excluding the space", "1 access grant", func(t *testing.T, space *apigen.Space) {
			sel := anySelector()
			sel.Spaces = allSpacesExcluding(space.ID)
			if _, err := h.Authz.CreateGrant(ruleGrant(2, allowRule(sel)), 0); err != nil {
				t.Fatalf("create grant: %v", err)
			}
		}},
		{"rule grant by entity ref", "1 access grant", func(t *testing.T, space *apigen.Space) {
			sel := anySelector()
			sel.EntityRefs = exactRefs(spaceRef(space.ID))
			if _, err := h.Authz.CreateGrant(ruleGrant(2, allowRule(sel)), 0); err != nil {
				t.Fatalf("create grant: %v", err)
			}
		}},
		{"grant template", "1 access grant template", func(t *testing.T, space *apigen.Space) {
			rule := allowRule(anySelector())
			rule.Selector.Spaces = exactSpaces(space.ID)
			if _, err := h.Authz.CreateGrantTemplate("scoped", literalTemplateSpec(rule), 0); err != nil {
				t.Fatalf("create template: %v", err)
			}
		}},
		{"global rule", "1 global access rule", func(t *testing.T, space *apigen.Space) {
			sel := anySelector()
			sel.Spaces = exactSpaces(space.ID)
			if _, err := h.Authz.CreateGlobalRule("scoped", allowRule(sel), 0); err != nil {
				t.Fatalf("create global rule: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			space, err := nodes.CreateSpace(h.Store, "scratch-"+tc.name, 0)
			if err != nil {
				t.Fatalf("CreateSpace: %v", err)
			}
			tc.fill(t, space)
			err = h.PostV1SpacesDelete(admin, &apigen.SpaceDeleteRequest{ID: space.ID})
			var apiErr apigen.ApiErr
			if !errors.As(err, &apiErr) || apiErr.InternalErr != "space_in_use" {
				t.Fatalf("err = %v, want space_in_use", err)
			}
			if !strings.Contains(apiErr.DisplayErr, tc.want) {
				t.Fatalf("message %q does not name %q", apiErr.DisplayErr, tc.want)
			}
			if _, err := h.Queries.GetSpace(admin.Ctx, space.ID); err != nil {
				t.Fatalf("a refused delete still removed the space: %v", err)
			}
		})
	}
}

func TestSpaceDeleteRemovesAnEmptySpaceFromNodeAllowLists(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	node := ensureTestNode(h.Store, "primary", "primary-id")
	admin := enforceCtx(1, false)
	space, err := nodes.CreateSpace(h.Store, "scratch", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if !nodeAllowsSpaceForTest(h, node.ID, space.ID) {
		t.Fatal("a new space is not open on the node")
	}

	// A grant that names only another space does not stand in the way.
	if _, err := h.Authz.CreateGrant(templateGrant(2, authz.SpaceAdminTemplateID, spaceBinding(1, staging.ID)), 0); err != nil {
		t.Fatalf("create grant: %v", err)
	}
	if err := h.PostV1SpacesDelete(admin, &apigen.SpaceDeleteRequest{ID: space.ID}); err != nil {
		t.Fatalf("delete empty space: %v", err)
	}
	if nodeAllowsSpaceForTest(h, node.ID, space.ID) {
		t.Fatal("the deleted space is still on the node's allow list")
	}
	if err := h.PostV1SpacesDelete(admin, &apigen.SpaceDeleteRequest{ID: space.ID}); !errors.Is(err, SpaceNotFoundErr) {
		t.Fatalf("second delete err = %v, want SpaceNotFoundErr", err)
	}
}

func spaceRef(id uint64) apigen.AuthzEntityRef {
	return apigen.AuthzEntityRef{Target: apigen.AuthzEntityRefTarget{Value: apigen.AuthzEntityRefTargetValueOneof{Space: &id}}}
}

func literalTemplateSpec(rule *apigen.AuthzRule) *apigen.AuthzGrantTemplateSpec {
	return &apigen.AuthzGrantTemplateSpec{Rules: []apigen.AuthzTemplateRule{{
		Effect: rule.Effect,
		Selector: apigen.AuthzTemplateSelector{
			Permissions: apigen.AuthzTemplatePermissionSelector{Value: apigen.AuthzTemplatePermissionSelectorValueOneof{Selector: &rule.Selector.Permissions}},
			Spaces:      apigen.AuthzTemplateSpaceSelector{Value: apigen.AuthzTemplateSpaceSelectorValueOneof{Selector: &rule.Selector.Spaces}},
			EntityTypes: apigen.AuthzTemplateEntityTypeSelector{Value: apigen.AuthzTemplateEntityTypeSelectorValueOneof{Selector: &rule.Selector.EntityTypes}},
			EntityRefs:  apigen.AuthzTemplateEntityRefSelector{Value: apigen.AuthzTemplateEntityRefSelectorValueOneof{Selector: &rule.Selector.EntityRefs}},
		},
	}}}
}
