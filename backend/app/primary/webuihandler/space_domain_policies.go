package webuihandler

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/spacedomainpolicies"
)

func (h *Handler) PostV1SpaceDomainPoliciesCreate(ctx apigen.Context, req *apigen.SpaceDomainPolicyCreateRequest) (*apigen.CoreWriteUpdate, error) {
	if err := h.requireAccess(ctx, vCreate, eDomain, 0, 0); err != nil {
		return nil, err
	}
	policy := &apigen.SpaceDomainPolicy{Name: req.Name, Disabled: req.Disabled, Rule: req.Rule}
	if err := spacedomainpolicies.Normalize(policy); err != nil {
		return nil, err
	}
	event, err := spacedomainpolicies.Create(h.Store, authorID(ctx), policy)
	if err != nil {
		return nil, err
	}
	return h.written(ctx, event.Mutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE)), nil
}

func (h *Handler) PostV1SpaceDomainPoliciesUpdate(ctx apigen.Context, req *apigen.SpaceDomainPolicyUpdateRequest) (*apigen.CoreWriteUpdate, error) {
	if err := h.requireEntityAccess(ctx, vUpdate, eDomain, 0, req.ID, spacedomainpolicies.NotFoundErr); err != nil {
		return nil, err
	}
	policy := &apigen.SpaceDomainPolicy{Name: req.Name, Disabled: req.Disabled, Rule: req.Rule}
	if err := spacedomainpolicies.Normalize(policy); err != nil {
		return nil, err
	}
	event, err := spacedomainpolicies.Update(h.Store, req.ID, req.ExpectedSeq, authorID(ctx), policy)
	if err != nil {
		return nil, err
	}
	return h.written(ctx, event.Mutation(apigen.AuthzVerb_AUTHZ_VERB_UPDATE)), nil
}

func (h *Handler) PostV1SpaceDomainPoliciesDelete(ctx apigen.Context, req *apigen.SpaceDomainPolicyDeleteRequest) error {
	if err := h.requireEntityAccess(ctx, vDelete, eDomain, 0, req.ID, spacedomainpolicies.NotFoundErr); err != nil {
		return err
	}
	return spacedomainpolicies.Delete(h.Store, req.ID, authorID(ctx))
}
