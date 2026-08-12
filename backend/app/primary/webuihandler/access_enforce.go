package webuihandler

import (
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/authz"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var AccessDeniedErr = apigen.NewApiErr("Access denied", "access_denied", http.StatusForbidden)

const (
	vView     = apigen.AuthzVerb_AUTHZ_VERB_VIEW
	vViewLogs = apigen.AuthzVerb_AUTHZ_VERB_VIEW_LOGS
	vReveal   = apigen.AuthzVerb_AUTHZ_VERB_REVEAL
	vEdit     = apigen.AuthzVerb_AUTHZ_VERB_EDIT
	vCreate   = apigen.AuthzVerb_AUTHZ_VERB_CREATE
	vDelete   = apigen.AuthzVerb_AUTHZ_VERB_DELETE
)

const (
	eSpace      = apigen.AuthzEntity_AUTHZ_ENTITY_SPACE
	eDeployment = apigen.AuthzEntity_AUTHZ_ENTITY_DEPLOYMENT
	eSecret     = apigen.AuthzEntity_AUTHZ_ENTITY_SECRET
	eConfig     = apigen.AuthzEntity_AUTHZ_ENTITY_CONFIG
	eAsset      = apigen.AuthzEntity_AUTHZ_ENTITY_ASSET
	eNode       = apigen.AuthzEntity_AUTHZ_ENTITY_NODE
	eCluster    = apigen.AuthzEntity_AUTHZ_ENTITY_CLUSTER
	eUser       = apigen.AuthzEntity_AUTHZ_ENTITY_USER
	eAccess     = apigen.AuthzEntity_AUTHZ_ENTITY_ACCESS
)

var eValues = []apigen.AuthzEntity{eSecret, eConfig}

func (h *Handler) canAccess(ctx apigen.Context, verb apigen.AuthzVerb, entity apigen.AuthzEntity, spaceID, entityID int64) bool {
	if h.Authz == nil {
		return true
	}
	if ctx.User == nil {
		return false
	}
	return h.Authz.HasAccess(int64(ctx.User.ID), authz.RequestedAccess{
		Verb:       verb,
		SpaceID:    spaceID,
		EntityType: entity,
		EntityID:   entityID,
		Delegated:  ctx.User.Delegated,
	})
}

func (h *Handler) canAccessAny(ctx apigen.Context, verb apigen.AuthzVerb, entities []apigen.AuthzEntity, spaceID, entityID int64) bool {
	for _, entity := range entities {
		if h.canAccess(ctx, verb, entity, spaceID, entityID) {
			return true
		}
	}
	return false
}

func (h *Handler) requireAccess(ctx apigen.Context, verb apigen.AuthzVerb, entity apigen.AuthzEntity, spaceID, entityID int64) error {
	if !h.canAccess(ctx, verb, entity, spaceID, entityID) {
		return AccessDeniedErr
	}
	return nil
}

func (h *Handler) requireEntityAccess(ctx apigen.Context, verb apigen.AuthzVerb, entity apigen.AuthzEntity, spaceID, entityID int64, notFound error) error {
	if !h.canAccess(ctx, apigen.AuthzVerb_AUTHZ_VERB_VIEW, entity, spaceID, entityID) {
		return notFound
	}
	if verb != apigen.AuthzVerb_AUTHZ_VERB_VIEW {
		return h.requireAccess(ctx, verb, entity, spaceID, entityID)
	}
	return nil
}

func (h *Handler) requireAnyEntityAccess(ctx apigen.Context, verb apigen.AuthzVerb, entities []apigen.AuthzEntity, spaceID, entityID int64, notFound error) error {
	if !h.canAccessAny(ctx, vView, entities, spaceID, entityID) {
		return notFound
	}
	if verb != vView && !h.canAccessAny(ctx, verb, entities, spaceID, entityID) {
		return AccessDeniedErr
	}
	return nil
}

func (h *Handler) requireAnyAccess(ctx apigen.Context, verb apigen.AuthzVerb, entities []apigen.AuthzEntity, spaceID, entityID int64) error {
	if !h.canAccessAny(ctx, verb, entities, spaceID, entityID) {
		return AccessDeniedErr
	}
	return nil
}

func valueSpace(spaceID int32) int64 {
	return int64(state.NormalizedUserSpaceID(spaceID))
}

func (h *Handler) spaceVisible(ctx apigen.Context, spaceID int64) bool {
	if h.Authz == nil {
		return true
	}
	if ctx.User == nil {
		return false
	}
	return h.Authz.SpaceVisible(int64(ctx.User.ID), spaceID, ctx.User.Delegated)
}

