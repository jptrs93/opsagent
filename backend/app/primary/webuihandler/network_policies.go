package webuihandler

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/networkpolicies"
)

func (h *Handler) PostV1NetworkPoliciesList(ctx apigen.Context) (*apigen.NetworkPolicyEventList, error) {
	return &apigen.NetworkPolicyEventList{Items: h.visibleNetworkPolicies(ctx)}, nil
}

func (h *Handler) PostV1NetworkPoliciesCreate(ctx apigen.Context, req *apigen.NetworkPolicyCreateRequest) (*apigen.NetworkPolicyEvent, error) {
	policy := &apigen.NetworkPolicy{Action: req.Action, Source: req.Source, Destination: req.Destination, Ports: req.Ports}
	if err := h.validateNetworkPolicyContent(ctx, policy); err != nil {
		return nil, err
	}
	return networkpolicies.Create(h.Store, int32(authorID(ctx)), policy)
}

func (h *Handler) PostV1NetworkPoliciesUpdate(ctx apigen.Context, req *apigen.NetworkPolicyUpdateRequest) (*apigen.NetworkPolicyEvent, error) {
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
	return networkpolicies.Update(h.Store, req.ID, req.Version, int32(authorID(ctx)), policy)
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

func (h *Handler) visibleNetworkPolicies(ctx apigen.Context) []*apigen.NetworkPolicyEvent {
	policies := networkpolicies.List(h.Queries)
	out := make([]*apigen.NetworkPolicyEvent, 0, len(policies))
	for _, policy := range policies {
		if h.networkPolicyVisible(ctx, &policy.Value) {
			out = append(out, policy)
		}
	}
	return out
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
