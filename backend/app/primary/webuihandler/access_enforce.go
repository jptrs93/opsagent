package webuihandler

import (
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/authz"
)

var AccessDeniedErr = apigen.NewApiErr("Access denied", "access_denied", http.StatusForbidden)

var DelegationNotPermittedErr = apigen.NewApiErr(
	"This action cannot be performed by an agent session",
	"delegation_not_permitted",
	http.StatusForbidden,
)

func requireHuman(ctx apigen.Context) error {
	if ctx.User == nil {
		return InvalidAuthTokenErr
	}
	if ctx.Delegated {
		return DelegationNotPermittedErr
	}
	return nil
}

const (
	vView     = apigen.AuthzVerb_AUTHZ_VERB_VIEW
	vViewLogs = apigen.AuthzVerb_AUTHZ_VERB_VIEW_LOGS
	vReveal   = apigen.AuthzVerb_AUTHZ_VERB_REVEAL
	vUpdate   = apigen.AuthzVerb_AUTHZ_VERB_UPDATE
	vCreate   = apigen.AuthzVerb_AUTHZ_VERB_CREATE
	vDelete   = apigen.AuthzVerb_AUTHZ_VERB_DELETE
)

const (
	eSpace      = apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SPACE
	eDeployment = apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT
	eSecret     = apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET
	eConfig     = apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_CONFIG
	eAsset      = apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ASSET
	eNode       = apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_NODE
	eCluster    = apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_CLUSTER
	eUser       = apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_USER
	eAccess     = apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ACCESS
)

var eValues = []apigen.AuthzEntityKind{eSecret, eConfig}

func (h *Handler) canAccess(ctx apigen.Context, verb apigen.AuthzVerb, entity apigen.AuthzEntityKind, spaceID, entityID uint64) bool {
	req := authz.RequestedAccess{Verb: verb, SpaceID: spaceID, EntityType: entity, EntityID: entityID}
	if !authz.SystemSpaceAllows(req) {
		return false
	}
	if h.Authz == nil {
		return true
	}
	if ctx.User == nil {
		return false
	}
	req.Delegated = ctx.Delegated
	return h.Authz.HasAccess(ctx.User.ID, req)
}

func (h *Handler) canAccessAny(ctx apigen.Context, verb apigen.AuthzVerb, entities []apigen.AuthzEntityKind, spaceID, entityID uint64) bool {
	for _, entity := range entities {
		if h.canAccess(ctx, verb, entity, spaceID, entityID) {
			return true
		}
	}
	return false
}

func (h *Handler) requireAccess(ctx apigen.Context, verb apigen.AuthzVerb, entity apigen.AuthzEntityKind, spaceID, entityID uint64) error {
	if !h.canAccess(ctx, verb, entity, spaceID, entityID) {
		return AccessDeniedErr
	}
	return nil
}

func (h *Handler) requireEntityAccess(ctx apigen.Context, verb apigen.AuthzVerb, entity apigen.AuthzEntityKind, spaceID, entityID uint64, notFound error) error {
	if !h.canAccess(ctx, apigen.AuthzVerb_AUTHZ_VERB_VIEW, entity, spaceID, entityID) {
		return notFound
	}
	if verb != apigen.AuthzVerb_AUTHZ_VERB_VIEW {
		return h.requireAccess(ctx, verb, entity, spaceID, entityID)
	}
	return nil
}

func (h *Handler) requireAnyEntityAccess(ctx apigen.Context, verb apigen.AuthzVerb, entities []apigen.AuthzEntityKind, spaceID, entityID uint64, notFound error) error {
	if !h.canAccessAny(ctx, vView, entities, spaceID, entityID) {
		return notFound
	}
	if verb != vView && !h.canAccessAny(ctx, verb, entities, spaceID, entityID) {
		return AccessDeniedErr
	}
	return nil
}

func (h *Handler) requireAnyAccess(ctx apigen.Context, verb apigen.AuthzVerb, entities []apigen.AuthzEntityKind, spaceID, entityID uint64) error {
	if !h.canAccessAny(ctx, verb, entities, spaceID, entityID) {
		return AccessDeniedErr
	}
	return nil
}

func valueSpace(spaceID uint64) uint64 {
	return nodes.NormalizedUserSpaceID(spaceID)
}

func (h *Handler) canCreateDeploymentSomewhere(ctx apigen.Context) bool {
	if h.Authz == nil {
		return true
	}
	if ctx.User == nil {
		return false
	}
	for _, space := range nodes.ListSpaces(h.Store.Queries()) {
		if space == nil {
			continue
		}
		if h.canAccess(ctx, vCreate, eDeployment, space.ID, 0) {
			return true
		}
	}
	return false
}

func (h *Handler) spaceVisible(ctx apigen.Context, spaceID uint64) bool {
	if h.Authz == nil {
		return true
	}
	if ctx.User == nil {
		return false
	}
	return h.Authz.SpaceVisible(ctx.User.ID, spaceID, ctx.Delegated)
}

// nodeVisible reports whether the caller may see a node: an explicit node:view
// grant, or — derived — the node hosts any space the caller can see, so a
// space-limited operator can pick placement targets without a cluster-level
// grant. Space 0 is skipped: every node allows it as an invariant, so counting
// it would not narrow anything.
func (h *Handler) nodeVisible(ctx apigen.Context, nodeID uint64, allowedSpaces []uint64) bool {
	if h.canAccess(ctx, vView, eNode, 0, nodeID) {
		return true
	}
	if h.Authz == nil {
		return true
	}
	for _, spaceID := range allowedSpaces {
		if spaceID == internaldeploy.SpaceID {
			continue
		}
		if h.spaceVisible(ctx, spaceID) {
			return true
		}
	}
	return false
}
