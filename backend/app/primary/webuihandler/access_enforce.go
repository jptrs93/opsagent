package webuihandler

import (
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/authz"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
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
	if ctx.User.Delegated {
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

func (h *Handler) canCreateDeploymentSomewhere(ctx apigen.Context) bool {
	if h.Authz == nil {
		return true
	}
	if ctx.User == nil {
		return false
	}
	for _, space := range h.Store.ListSpaces() {
		if space == nil {
			continue
		}
		if h.canAccess(ctx, vCreate, eDeployment, int64(space.ID), 0) {
			return true
		}
	}
	return false
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

// nodeVisible reports whether the caller may see a node: an explicit node:view
// grant, or — derived — the node hosts any space the caller can see, so a
// space-limited operator can pick placement targets without a cluster-level
// grant. Space 0 is skipped: every node allows it as an invariant, so counting
// it would not narrow anything.
func (h *Handler) nodeVisible(ctx apigen.Context, nodeID int64, allowedSpaces []int32) bool {
	if h.canAccess(ctx, vView, eNode, 0, nodeID) {
		return true
	}
	if h.Authz == nil {
		return true
	}
	for _, spaceID := range allowedSpaces {
		if spaceID == state.OpendeploySpaceID {
			continue
		}
		if h.spaceVisible(ctx, int64(spaceID)) {
			return true
		}
	}
	return false
}

// nodeAllowedSpaces snapshots each node's allow list for visibility checks on
// records that carry only a node id.
func (h *Handler) nodeAllowedSpaces() map[int32][]int32 {
	out := map[int32][]int32{}
	for _, node := range h.Store.ListClusterNodes() {
		if node != nil {
			out[node.ID] = node.AllowedSpaces
		}
	}
	return out
}
