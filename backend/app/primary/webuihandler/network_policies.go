package webuihandler

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/networkpolicies"
)

func (h *Handler) PostV1NetworkPoliciesCreate(ctx apigen.Context, req *apigen.NetworkPolicyCreateRequest) (*apigen.CoreWriteUpdate, error) {
	policy := &apigen.NetworkPolicy{Action: req.Action, Source: req.Source, Destination: req.Destination, Ports: req.Ports}
	if err := h.validateNetworkPolicyContent(ctx, policy); err != nil {
		return nil, err
	}
	event, err := networkpolicies.Create(h.Store, int32(authorID(ctx)), policy)
	if err != nil {
		return nil, err
	}
	return h.written(ctx, event.Mutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE)), nil
}

func (h *Handler) PostV1NetworkPoliciesUpdate(ctx apigen.Context, req *apigen.NetworkPolicyUpdateRequest) (*apigen.CoreWriteUpdate, error) {
	current := networkpolicies.ByID(h.Queries, req.ID)
	if current == nil || !h.networkPolicyVisible(ctx, &current.Value) {
		return nil, networkpolicies.NotFoundErr
	}
	if err := h.requireNetworkPolicyWriteAccess(ctx, &current.Value); err != nil {
		return nil, err
	}
	policy := &apigen.NetworkPolicy{Action: req.Action, Source: req.Source, Destination: req.Destination, Ports: req.Ports}
	if err := h.validateNetworkPolicyContent(ctx, policy); err != nil {
		return nil, err
	}
	event, err := networkpolicies.Update(h.Store, req.ID, req.ExpectedSeq, int32(authorID(ctx)), policy)
	if err != nil {
		return nil, err
	}
	return h.written(ctx, event.Mutation(apigen.AuthzVerb_AUTHZ_VERB_UPDATE)), nil
}

func (h *Handler) PostV1NetworkPoliciesDelete(ctx apigen.Context, req *apigen.NetworkPolicyDeleteRequest) error {
	current := networkpolicies.ByID(h.Queries, req.ID)
	if current == nil || !h.networkPolicyVisible(ctx, &current.Value) {
		return networkpolicies.NotFoundErr
	}
	if err := h.requireNetworkPolicyWriteAccess(ctx, &current.Value); err != nil {
		return err
	}
	return networkpolicies.Delete(h.Store, req.ID, int32(authorID(ctx)))
}

func authorID(ctx apigen.Context) int64 {
	if ctx.User == nil {
		return 0
	}
	return int64(ctx.User.ID)
}

func (h *Handler) validateNetworkPolicyContent(ctx apigen.Context, policy *apigen.NetworkPolicy) error {
	if err := networkpolicies.ValidateShape(policy); err != nil {
		return err
	}
	sourceSpace, ok := networkpolicies.PeerSpace(h.Queries, policy.Source)
	if !ok {
		return networkpolicies.PeerNotFoundErr
	}
	destinationSpace, ok := networkpolicies.PeerSpace(h.Queries, policy.Destination)
	if !ok {
		return networkpolicies.PeerNotFoundErr
	}
	if sourceSpace == destinationSpace {
		return networkpolicies.RedundantErr
	}
	if err := h.requireEntityAccess(ctx, vUpdate, eSpace, int64(destinationSpace), int64(destinationSpace), networkpolicies.PeerNotFoundErr); err != nil {
		return err
	}
	if !h.spaceVisible(ctx, int64(sourceSpace)) {
		return networkpolicies.PeerNotFoundErr
	}
	return nil
}

func (h *Handler) requireNetworkPolicyWriteAccess(ctx apigen.Context, policy *apigen.NetworkPolicy) error {
	destinationSpace, ok := networkpolicies.PeerSpace(h.Queries, policy.Destination)
	if !ok {
		return h.requireAccess(ctx, vUpdate, eCluster, 0, 0)
	}
	return h.requireEntityAccess(ctx, vUpdate, eSpace, int64(destinationSpace), int64(destinationSpace), networkpolicies.NotFoundErr)
}

func (h *Handler) networkPolicyVisible(ctx apigen.Context, policy *apigen.NetworkPolicy) bool {
	anyResolved := false
	for _, ref := range []*apigen.NetworkPolicyPeerRef{policy.Destination, policy.Source} {
		spaceID, ok := networkpolicies.PeerSpace(h.Queries, ref)
		if !ok {
			continue
		}
		anyResolved = true
		if h.spaceVisible(ctx, int64(spaceID)) {
			return true
		}
	}
	return !anyResolved
}
