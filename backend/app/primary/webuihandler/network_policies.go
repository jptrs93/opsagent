package webuihandler

import (
	"errors"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var InvalidNetworkPolicyErr = apigen.NewApiErr("Invalid network policy", "invalid_network_policy", http.StatusBadRequest)
var NetworkPolicyDenyUnsupportedErr = apigen.NewApiErr("Deny policies are not implemented yet", "network_policy_deny_unsupported", http.StatusBadRequest)
var NetworkPolicyRedundantErr = apigen.NewApiErr("Same-space traffic is always allowed; this rule is redundant", "network_policy_redundant", http.StatusBadRequest)
var NetworkPolicyPeerNotFoundErr = apigen.NewApiErr("Network policy peer not found", "network_policy_peer_not_found", http.StatusNotFound)
var NetworkPolicyNotFoundErr = apigen.NewApiErr("Network policy not found", "network_policy_not_found", http.StatusNotFound)
var NetworkPolicyVersionConflictErr = apigen.NewApiErr("Network policy was modified concurrently", "network_policy_version_conflict", http.StatusConflict)

func (h *Handler) PostV1NetworkPoliciesList(ctx apigen.Context) (*apigen.NetworkPolicyList, error) {
	return &apigen.NetworkPolicyList{Items: h.visibleNetworkPolicies(ctx)}, nil
}

func (h *Handler) PostV1NetworkPoliciesCreate(ctx apigen.Context, req *apigen.NetworkPolicyCreateRequest) (*apigen.NetworkPolicy, error) {
	policy := &apigen.NetworkPolicy{Action: req.Action, Source: req.Source, Destination: req.Destination, Ports: req.Ports}
	if err := h.validateNetworkPolicyContent(ctx, policy); err != nil {
		return nil, err
	}
	created, err := h.Store.CreateNetworkPolicy(policy, authorID(ctx))
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (h *Handler) PostV1NetworkPoliciesUpdate(ctx apigen.Context, req *apigen.NetworkPolicyUpdateRequest) (*apigen.NetworkPolicy, error) {
	current := h.Store.GetNetworkPolicy(req.ID)
	if current == nil || !h.networkPolicyVisible(ctx, current) {
		return nil, NetworkPolicyNotFoundErr
	}
	if err := h.requireNetworkPolicyWriteAccess(ctx, current); err != nil {
		return nil, err
	}
	policy := &apigen.NetworkPolicy{Action: req.Action, Source: req.Source, Destination: req.Destination, Ports: req.Ports}
	if err := h.validateNetworkPolicyContent(ctx, policy); err != nil {
		return nil, err
	}
	updated, err := h.Store.UpdateNetworkPolicy(req.ID, req.Version, policy, authorID(ctx))
	if errors.Is(err, state.ErrNotFound) {
		return nil, NetworkPolicyNotFoundErr
	}
	if errors.Is(err, state.ErrVersionConflict) {
		return nil, NetworkPolicyVersionConflictErr
	}
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (h *Handler) PostV1NetworkPoliciesDelete(ctx apigen.Context, req *apigen.NetworkPolicyDeleteRequest) error {
	current := h.Store.GetNetworkPolicy(req.ID)
	if current == nil || !h.networkPolicyVisible(ctx, current) {
		return NetworkPolicyNotFoundErr
	}
	if err := h.requireNetworkPolicyWriteAccess(ctx, current); err != nil {
		return err
	}
	err := h.Store.DeleteNetworkPolicy(req.ID, authorID(ctx))
	if errors.Is(err, state.ErrNotFound) {
		return NetworkPolicyNotFoundErr
	}
	return err
}

func authorID(ctx apigen.Context) int64 {
	if ctx.User == nil {
		return 0
	}
	return int64(ctx.User.ID)
}

func (h *Handler) validateNetworkPolicyContent(ctx apigen.Context, policy *apigen.NetworkPolicy) error {
	if policy.Action == apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_DENY {
		return NetworkPolicyDenyUnsupportedErr
	}
	if policy.Action != apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW {
		return InvalidNetworkPolicyErr
	}
	if err := validateNetworkPolicyPeerRef(policy.Source); err != nil {
		return err
	}
	if err := validateNetworkPolicyPeerRef(policy.Destination); err != nil {
		return err
	}
	for _, port := range policy.Ports {
		if network.ValidateNetPortMatch(port) != nil {
			return InvalidNetworkPolicyErr
		}
	}
	sourceSpace, ok := h.resolveNetworkPolicyPeerSpace(policy.Source)
	if !ok {
		return NetworkPolicyPeerNotFoundErr
	}
	destinationSpace, ok := h.resolveNetworkPolicyPeerSpace(policy.Destination)
	if !ok {
		return NetworkPolicyPeerNotFoundErr
	}
	if sourceSpace == destinationSpace {
		return NetworkPolicyRedundantErr
	}
	if err := h.requireEntityAccess(ctx, vUpdate, eSpace, int64(destinationSpace), int64(destinationSpace), NetworkPolicyPeerNotFoundErr); err != nil {
		return err
	}
	if !h.spaceVisible(ctx, int64(sourceSpace)) {
		return NetworkPolicyPeerNotFoundErr
	}
	return nil
}

func validateNetworkPolicyPeerRef(ref *apigen.NetworkPolicyPeerRef) error {
	if ref == nil {
		return InvalidNetworkPolicyErr
	}
	switch ref.Kind {
	case apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE:
		if ref.ID < 0 || ref.ID > network.MaxSpaceID {
			return InvalidNetworkPolicyErr
		}
	case apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_DEPLOYMENT:
		if ref.ID < 1 || ref.ID > network.MaxDeploymentID {
			return InvalidNetworkPolicyErr
		}
	default:
		return InvalidNetworkPolicyErr
	}
	return nil
}

func (h *Handler) resolveNetworkPolicyPeerSpace(ref *apigen.NetworkPolicyPeerRef) (int32, bool) {
	if ref == nil {
		return 0, false
	}
	switch ref.Kind {
	case apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE:
		for _, space := range h.Store.ListSpaces() {
			if space != nil && space.ID == ref.ID {
				return ref.ID, true
			}
		}
	case apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_DEPLOYMENT:
		if cfg := h.findConfigByID(ref.ID); cfg != nil && !cfg.Deleted() {
			return cfg.Def.SpaceID, true
		}
	}
	return 0, false
}

func (h *Handler) requireNetworkPolicyWriteAccess(ctx apigen.Context, policy *apigen.NetworkPolicy) error {
	destinationSpace, ok := h.resolveNetworkPolicyPeerSpace(policy.Destination)
	if !ok {
		return h.requireAccess(ctx, vUpdate, eCluster, 0, 0)
	}
	return h.requireEntityAccess(ctx, vUpdate, eSpace, int64(destinationSpace), int64(destinationSpace), NetworkPolicyNotFoundErr)
}

func (h *Handler) visibleNetworkPolicies(ctx apigen.Context) []*apigen.NetworkPolicy {
	policies := h.Store.ListNetworkPolicies()
	out := make([]*apigen.NetworkPolicy, 0, len(policies))
	for _, policy := range policies {
		if h.networkPolicyVisible(ctx, policy) {
			out = append(out, policy)
		}
	}
	return out
}

func (h *Handler) networkPolicyVisible(ctx apigen.Context, policy *apigen.NetworkPolicy) bool {
	anyResolved := false
	for _, ref := range []*apigen.NetworkPolicyPeerRef{policy.Destination, policy.Source} {
		spaceID, ok := h.resolveNetworkPolicyPeerSpace(ref)
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
