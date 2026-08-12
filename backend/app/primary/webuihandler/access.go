package webuihandler

import (
	"errors"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/authz"
)

var AccessNotFoundErr = apigen.NewApiErr("Not found", "access_not_found", http.StatusNotFound)
var AccessBuiltinErr = apigen.NewApiErr("Builtin rule templates are read-only", "access_builtin", http.StatusConflict)
var AccessNameTakenErr = apigen.NewApiErr("Name already in use", "access_name_taken", http.StatusConflict)
var AccessTemplateInUseErr = apigen.NewApiErr("Rule template is referenced by grants", "access_template_in_use", http.StatusConflict)

func mapAuthzErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, authz.ErrBuiltin):
		return AccessBuiltinErr
	case errors.Is(err, authz.ErrNameTaken):
		return AccessNameTakenErr
	case errors.Is(err, authz.ErrTemplateInUse):
		return AccessTemplateInUseErr
	case errors.Is(err, authz.ErrInvalid):
		return apigen.NewApiErr(err.Error(), "access_invalid", http.StatusBadRequest)
	case errors.Is(err, authz.ErrNotFound):
		return AccessNotFoundErr
	default:
		return err
	}
}

func (h *Handler) PostV1AccessRuleTemplatesList(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.AuthzRuleTemplateList, error) {
	return &apigen.AuthzRuleTemplateList{Items: h.Authz.RuleTemplates()}, nil
}

func (h *Handler) PostV1AccessRuleTemplatesCreate(ctx apigen.Context, req *apigen.AuthzRuleTemplateCreateRequest) (*apigen.AuthzRuleTemplateRecord, error) {
	rec, err := h.Authz.CreateRuleTemplate(req.Name, req.Template, int64(requestUserID(ctx)))
	if err != nil {
		return nil, mapAuthzErr(err)
	}
	return rec, nil
}

func (h *Handler) PostV1AccessRuleTemplatesUpdate(ctx apigen.Context, req *apigen.AuthzRuleTemplateUpdateRequest) (*apigen.AuthzRuleTemplateRecord, error) {
	rec, err := h.Authz.UpdateRuleTemplate(req.ID, req.Name, req.Template)
	if err != nil {
		return nil, mapAuthzErr(err)
	}
	return rec, nil
}

func (h *Handler) PostV1AccessRuleTemplatesDelete(ctx apigen.Context, req *apigen.AuthzRuleTemplateDeleteRequest) error {
	return mapAuthzErr(h.Authz.DeleteRuleTemplate(req.ID))
}

func (h *Handler) PostV1AccessGrantsList(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.AuthzGrantList, error) {
	return &apigen.AuthzGrantList{Items: h.Authz.Grants()}, nil
}

func (h *Handler) PostV1AccessGrantsCreate(ctx apigen.Context, req *apigen.AuthzGrantCreateRequest) (*apigen.AuthzGrantRecord, error) {
	rec, err := h.Authz.CreateGrant(&apigen.AuthzGrantRecord{
		UserID:     req.UserID,
		TemplateID: req.TemplateID,
		CreatedBy:  int64(requestUserID(ctx)),
		Grant:      req.Grant,
	})
	if err != nil {
		return nil, mapAuthzErr(err)
	}
	return rec, nil
}

func (h *Handler) PostV1AccessGrantsDelete(ctx apigen.Context, req *apigen.AuthzGrantDeleteRequest) error {
	return mapAuthzErr(h.Authz.DeleteGrant(req.UserID, req.ID))
}

func (h *Handler) PostV1AccessGlobalRulesList(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.AuthzGlobalRuleList, error) {
	return &apigen.AuthzGlobalRuleList{Items: h.Authz.GlobalRules()}, nil
}

func (h *Handler) PostV1AccessGlobalRulesCreate(ctx apigen.Context, req *apigen.AuthzGlobalRuleCreateRequest) (*apigen.AuthzGlobalRuleRecord, error) {
	rec, err := h.Authz.CreateGlobalRule(req.Name, req.Rule, int64(requestUserID(ctx)))
	if err != nil {
		return nil, mapAuthzErr(err)
	}
	return rec, nil
}

func (h *Handler) PostV1AccessGlobalRulesDelete(ctx apigen.Context, req *apigen.AuthzGlobalRuleDeleteRequest) error {
	return mapAuthzErr(h.Authz.DeleteGlobalRule(req.ID))
}
