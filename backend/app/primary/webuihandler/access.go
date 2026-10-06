package webuihandler

import (
	"errors"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/authz"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

var AccessNotFoundErr = apigen.NewApiErr("Not found", "access_not_found", http.StatusNotFound)
var AccessBuiltinErr = apigen.NewApiErr("Builtin grant templates are read-only", "access_builtin", http.StatusConflict)
var AccessNameTakenErr = apigen.NewApiErr("Name already in use", "access_name_taken", http.StatusConflict)
var AccessTemplateInUseErr = apigen.NewApiErr("Grant template is referenced by grants", "access_template_in_use", http.StatusConflict)
var AccessLastAdminErr = apigen.NewApiErr("Cannot delete the last access-managing grant", "access_last_admin", http.StatusConflict)

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
	case errors.Is(err, authz.ErrLastAdmin):
		return AccessLastAdminErr
	case errors.Is(err, authz.ErrInvalid):
		return apigen.NewApiErr(err.Error(), "access_invalid", http.StatusBadRequest)
	case errors.Is(err, authz.ErrNotFound):
		return AccessNotFoundErr
	default:
		return err
	}
}

func (h *Handler) PostV1AccessGrantTemplatesList(ctx apigen.Context) (*apigen.AuthzGrantTemplateList, error) {
	if err := h.requireAccess(ctx, vView, eAccess, 0, 0); err != nil {
		return nil, err
	}
	items := h.Authz.GrantTemplates()
	out := make([]apigen.AuthzGrantTemplate, 0, len(items))
	for _, t := range items {
		out = append(out, *t)
	}
	return &apigen.AuthzGrantTemplateList{Items: out}, nil
}

func (h *Handler) PostV1AccessGrantTemplatesCreate(ctx apigen.Context, req *apigen.AuthzGrantTemplateCreateRequest) (*apigen.CoreWriteUpdate, error) {
	if err := h.requireAccess(ctx, vCreate, eAccess, 0, 0); err != nil {
		return nil, err
	}
	rec, err := h.Authz.CreateGrantTemplate(req.Name, &req.Spec, requestUserID(ctx))
	if err != nil {
		return nil, mapAuthzErr(err)
	}
	return h.writtenGrantTemplate(ctx, rec.ID, apigen.AuthzVerb_AUTHZ_VERB_CREATE)
}

func (h *Handler) PostV1AccessGrantTemplatesUpdate(ctx apigen.Context, req *apigen.AuthzGrantTemplateUpdateRequest) (*apigen.CoreWriteUpdate, error) {
	if err := h.requireAccess(ctx, vUpdate, eAccess, 0, req.ID); err != nil {
		return nil, err
	}
	rec, err := h.Authz.UpdateGrantTemplate(req.ID, req.Name, &req.Spec, requestUserID(ctx))
	if err != nil {
		return nil, mapAuthzErr(err)
	}
	return h.writtenGrantTemplate(ctx, rec.ID, apigen.AuthzVerb_AUTHZ_VERB_UPDATE)
}

func (h *Handler) PostV1AccessGrantTemplatesDelete(ctx apigen.Context, req *apigen.AuthzGrantTemplateDeleteRequest) error {
	if err := h.requireAccess(ctx, vDelete, eAccess, 0, req.ID); err != nil {
		return err
	}
	return mapAuthzErr(h.Authz.DeleteGrantTemplate(req.ID, requestUserID(ctx)))
}

func (h *Handler) PostV1AccessGrantsCreate(ctx apigen.Context, req *apigen.AuthzGrantCreateRequest) (*apigen.CoreWriteUpdate, error) {
	if err := h.requireAccess(ctx, vCreate, eAccess, 0, 0); err != nil {
		return nil, err
	}
	rec, err := h.Authz.CreateGrant(&apigen.AuthzGrant{UserID: req.UserID, Grant: req.Grant}, requestUserID(ctx))
	if err != nil {
		return nil, mapAuthzErr(err)
	}
	return h.writtenGrant(ctx, rec.ID)
}

func (h *Handler) PostV1AccessGrantsDelete(ctx apigen.Context, req *apigen.AuthzGrantDeleteRequest) error {
	if err := h.requireAccess(ctx, vDelete, eAccess, 0, req.ID); err != nil {
		return err
	}
	return mapAuthzErr(h.Authz.DeleteGrant(req.UserID, req.ID, requestUserID(ctx)))
}

func (h *Handler) PostV1AccessGlobalRulesList(ctx apigen.Context) (*apigen.AuthzGlobalRuleList, error) {
	if err := h.requireAccess(ctx, vView, eAccess, 0, 0); err != nil {
		return nil, err
	}
	rules := h.Authz.GlobalRules()
	out := make([]apigen.AuthzGlobalRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, *r)
	}
	return &apigen.AuthzGlobalRuleList{Items: out}, nil
}

func (h *Handler) PostV1AccessGlobalRulesCreate(ctx apigen.Context, req *apigen.AuthzGlobalRuleCreateRequest) (*apigen.CoreWriteUpdate, error) {
	if err := h.requireAccess(ctx, vCreate, eAccess, 0, 0); err != nil {
		return nil, err
	}
	rec, err := h.Authz.CreateGlobalRule(req.Name, &req.Rule, requestUserID(ctx))
	if err != nil {
		return nil, mapAuthzErr(err)
	}
	return h.writtenGlobalRule(ctx, rec.ID)
}

func (h *Handler) PostV1AccessGlobalRulesDelete(ctx apigen.Context, req *apigen.AuthzGlobalRuleDeleteRequest) error {
	if err := h.requireAccess(ctx, vDelete, eAccess, 0, req.ID); err != nil {
		return err
	}
	return mapAuthzErr(h.Authz.DeleteGlobalRule(req.ID, requestUserID(ctx)))
}

func (h *Handler) writtenGrantTemplate(ctx apigen.Context, id uint64, verb apigen.AuthzVerb) (*apigen.CoreWriteUpdate, error) {
	row, err := h.Queries.GetAuthzGrantTemplate(ctx, id)
	if err != nil {
		return nil, err
	}
	entity, err := pq.AuthzGrantTemplateEntity(row)
	if err != nil {
		return nil, err
	}
	meta := pq.EventMeta{GlobalSeq: row.Seq, EventTime: row.EventTime, Author: row.Author, EventType: verb}
	return h.written(ctx, pq.AuthzGrantTemplateMutation(meta, entity)), nil
}

func (h *Handler) writtenGrant(ctx apigen.Context, id uint64) (*apigen.CoreWriteUpdate, error) {
	row, err := h.Queries.GetAuthzGrant(ctx, id)
	if err != nil {
		return nil, err
	}
	entity, err := pq.AuthzGrantEntity(row)
	if err != nil {
		return nil, err
	}
	meta := pq.EventMeta{GlobalSeq: row.Seq, EventTime: row.EventTime, Author: row.Author, EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}
	return h.written(ctx, pq.AuthzGrantMutation(meta, row.ID, entity)), nil
}

func (h *Handler) writtenGlobalRule(ctx apigen.Context, id uint64) (*apigen.CoreWriteUpdate, error) {
	row, err := h.Queries.GetAuthzGlobalRule(ctx, id)
	if err != nil {
		return nil, err
	}
	entity, err := pq.AuthzGlobalRuleEntity(row)
	if err != nil {
		return nil, err
	}
	meta := pq.EventMeta{GlobalSeq: row.Seq, EventTime: row.EventTime, Author: row.Author, EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}
	return h.written(ctx, pq.AuthzGlobalRuleMutation(meta, entity)), nil
}
