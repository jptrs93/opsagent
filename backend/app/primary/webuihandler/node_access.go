package webuihandler

import (
	"context"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodeaccesspolicies"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/nodeaccess"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

type nodeAccessView struct {
	policies []apigen.NodeAccessPolicy
	spaces   []uint64
}

func (h *Handler) readNodeAccess() *nodeAccessView {
	ctx := context.Background()
	view := &nodeAccessView{policies: erru.Must(h.Queries.NodeAccessPolicyValues(ctx))}
	for _, space := range erru.Must(h.Queries.ListSpaces(ctx)) {
		view.spaces = append(view.spaces, space.ID)
	}
	return view
}

func (h *Handler) runSpaces(ctx apigen.Context, access *nodeAccessView, nodeID uint64) []uint64 {
	out := []uint64{}
	for _, spaceID := range nodeaccess.RunSpaces(access.policies, nodeID, access.spaces) {
		if spaceID != internaldeploy.SpaceID && h.spaceVisible(ctx, spaceID) {
			out = append(out, spaceID)
		}
	}
	return out
}

func (h *Handler) nodeVisibleIn(ctx apigen.Context, access *nodeAccessView, nodeID uint64) bool {
	if h.canAccess(ctx, vView, eNode, 0, nodeID) || h.Authz == nil {
		return true
	}
	return len(h.runSpaces(ctx, access, nodeID)) > 0
}

func (h *Handler) nodeVisible(ctx apigen.Context, nodeID uint64) bool {
	if h.canAccess(ctx, vView, eNode, 0, nodeID) || h.Authz == nil {
		return true
	}
	return h.nodeVisibleIn(ctx, h.readNodeAccess(), nodeID)
}

func (h *Handler) nodeAccessList(ctx apigen.Context) apigen.NodeAccessList {
	out := apigen.NodeAccessList{Items: []apigen.NodeAccess{}}
	if isRoot(ctx) {
		return out
	}
	access := h.readNodeAccess()
	for _, row := range erru.Must(h.Queries.ListNodeRows(context.Background(), pq.AllNodeStatuses)) {
		id := row.Event.NodeID
		if !h.nodeVisibleIn(ctx, access, id) {
			continue
		}
		out.Items = append(out.Items, apigen.NodeAccess{NodeID: id, RunDeploymentSpaces: h.runSpaces(ctx, access, id)})
	}
	return out
}

func (h *Handler) PostV1NodeAccessPoliciesCreate(ctx apigen.Context, req *apigen.NodeAccessPolicyCreateRequest) (*apigen.CoreWriteUpdate, error) {
	if err := h.requireAccess(ctx, vCreate, eAccess, 0, 0); err != nil {
		return nil, err
	}
	policy := &apigen.NodeAccessPolicy{Name: req.Name, Disabled: req.Disabled, Nodes: req.Nodes, Rule: req.Rule}
	if err := nodeaccesspolicies.Normalize(policy); err != nil {
		return nil, err
	}
	event, err := nodeaccesspolicies.Create(h.Store, authorID(ctx), policy)
	if err != nil {
		return nil, err
	}
	return h.written(ctx, event.Mutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE)), nil
}

func (h *Handler) PostV1NodeAccessPoliciesUpdate(ctx apigen.Context, req *apigen.NodeAccessPolicyUpdateRequest) (*apigen.CoreWriteUpdate, error) {
	if err := h.requireEntityAccess(ctx, vUpdate, eAccess, 0, req.ID, nodeaccesspolicies.NotFoundErr); err != nil {
		return nil, err
	}
	policy := &apigen.NodeAccessPolicy{Name: req.Name, Disabled: req.Disabled, Nodes: req.Nodes, Rule: req.Rule}
	if err := nodeaccesspolicies.Normalize(policy); err != nil {
		return nil, err
	}
	event, err := nodeaccesspolicies.Update(h.Store, req.ID, req.ExpectedSeq, authorID(ctx), policy)
	if err != nil {
		return nil, err
	}
	return h.written(ctx, event.Mutation(apigen.AuthzVerb_AUTHZ_VERB_UPDATE)), nil
}

func (h *Handler) PostV1NodeAccessPoliciesDelete(ctx apigen.Context, req *apigen.NodeAccessPolicyDeleteRequest) error {
	if err := h.requireEntityAccess(ctx, vDelete, eAccess, 0, req.ID, nodeaccesspolicies.NotFoundErr); err != nil {
		return err
	}
	return nodeaccesspolicies.Delete(h.Store, req.ID, authorID(ctx))
}
