package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/secrets"
)

var SecretNameRequiredErr = apigen.NewApiErr("Secret name is required", "secret_name_required", http.StatusBadRequest)
var SecretKeyRequiredErr = apigen.NewApiErr("Secret key is required", "secret_key_required", http.StatusBadRequest)
var SecretsLockedErr = apigen.NewApiErr("Secrets store is locked; unlock with the recovery code", "secrets_locked", http.StatusServiceUnavailable)
var InvalidRecoveryCodeErr = apigen.NewApiErr("Invalid recovery code", "secret_invalid_recovery_code", http.StatusBadRequest)
var NoRecoveryCodeErr = apigen.NewApiErr("No recovery code configured", "secret_no_recovery_code", http.StatusBadRequest)
var SecretNotFoundErr = apigen.NewApiErr("Secret not found", "secret_not_found", http.StatusNotFound)
var SecretReservedNameErr = apigen.NewApiErr("Secret name is reserved for OpenDeploy internal use", "secret_reserved_name", http.StatusBadRequest)

func secretMetaToProto(m secrets.Meta) *apigen.SecretMeta {
	return &apigen.SecretMeta{
		ID:        m.ID,
		Name:      m.Name,
		SpaceID:   m.SpaceID,
		Group:     m.Group,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
		UpdatedBy: m.UpdatedBy,
	}
}

func (h *Handler) secretsStatus() apigen.SecretsStatusResponse {
	unlocked, recoveryConfigured := h.Secrets.Status()
	return apigen.SecretsStatusResponse{
		Unlocked:           unlocked,
		RecoveryConfigured: recoveryConfigured,
	}
}

func (h *Handler) listSecretMetas() []*apigen.SecretMeta {
	metas := h.Secrets.List()
	items := make([]*apigen.SecretMeta, 0, len(metas))
	for _, m := range metas {
		items = append(items, secretMetaToProto(m))
	}
	return items
}

func (h *Handler) PostV1SecretsList(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.SecretList, error) {
	return &apigen.SecretList{Items: h.listSecretMetas()}, nil
}

func (h *Handler) PostV1SecretsSet(ctx apigen.Context, req *apigen.SecretSetRequest) (*apigen.SecretMeta, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, SecretNameRequiredErr
	}
	var updatedBy int32
	if ctx.User != nil {
		updatedBy = ctx.User.ID
	}
	meta, err := h.Secrets.Set(req.Name, req.Group, req.Value, updatedBy, req.SpaceID)
	if err != nil {
		if errors.Is(err, secrets.ErrLocked) {
			return nil, SecretsLockedErr
		}
		if errors.Is(err, secrets.ErrReservedName) {
			return nil, SecretReservedNameErr
		}
		return nil, err
	}
	proto := secretMetaToProto(meta)
	h.Store.NotifySecretReferenceUpdate(apigen.SecretReference{ID: proto.ID, Name: proto.Name, SpaceID: proto.SpaceID})
	h.Store.NotifySecretMetaUpdate(*proto)
	return proto, nil
}

func (h *Handler) PostV1SecretsReveal(ctx apigen.Context, req *apigen.SecretRevealRequest) (*apigen.SecretRevealResponse, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, SecretNameRequiredErr
	}
	value, err := h.Secrets.Reveal(req.Name)
	if err != nil {
		switch {
		case errors.Is(err, secrets.ErrLocked):
			return nil, SecretsLockedErr
		case errors.Is(err, secrets.ErrNotFound):
			return nil, SecretNotFoundErr
		default:
			return nil, err
		}
	}
	return &apigen.SecretRevealResponse{Value: value}, nil
}

func (h *Handler) PostV1SecretValueReveal(ctx apigen.Context, req *apigen.SecretValue) (*apigen.SecretRevealResponse, error) {
	if strings.TrimSpace(req.Key) == "" {
		return nil, SecretKeyRequiredErr
	}
	value, err := h.Secrets.Reveal(req.Key)
	if err != nil {
		switch {
		case errors.Is(err, secrets.ErrLocked):
			return nil, SecretsLockedErr
		case errors.Is(err, secrets.ErrNotFound):
			return nil, SecretNotFoundErr
		default:
			return nil, err
		}
	}
	return &apigen.SecretRevealResponse{Value: value}, nil
}

func (h *Handler) PostV1SecretsDelete(ctx apigen.Context, req *apigen.SecretDeleteRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return SecretNameRequiredErr
	}
	name := strings.TrimSpace(req.Name)
	var deleted *apigen.SecretReference
	var deletedMeta *apigen.SecretMeta
	for _, meta := range h.listSecretMetas() {
		if meta.Name == name {
			deleted = &apigen.SecretReference{ID: meta.ID, Name: meta.Name, SpaceID: meta.SpaceID, Deleted: true}
			deletedMeta = meta
			deletedMeta.Deleted = true
			break
		}
	}
	if err := h.Secrets.Delete(req.Name); err != nil {
		if errors.Is(err, secrets.ErrReservedName) {
			return SecretReservedNameErr
		}
		return err
	}
	if deleted != nil {
		h.Store.NotifySecretReferenceUpdate(*deleted)
	}
	if deletedMeta != nil {
		h.Store.NotifySecretMetaUpdate(*deletedMeta)
	}
	return nil
}

func (h *Handler) PostV1SecretsStatus(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.SecretsStatusResponse, error) {
	status := h.secretsStatus()
	return &status, nil
}

func (h *Handler) PostV1SecretsGenerateRecoveryCode(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.SecretRecoveryCodeResponse, error) {
	code, err := h.Secrets.GenerateRecoveryCode()
	if err != nil {
		if errors.Is(err, secrets.ErrLocked) {
			return nil, SecretsLockedErr
		}
		return nil, err
	}
	status := h.secretsStatus()
	h.Store.NotifySecretsStatusUpdate(status)
	return &apigen.SecretRecoveryCodeResponse{Code: code}, nil
}

func (h *Handler) PostV1SecretsUnlock(ctx apigen.Context, req *apigen.SecretUnlockRequest) (*apigen.SecretsStatusResponse, error) {
	if err := h.Secrets.Unlock(req.Code); err != nil {
		switch {
		case errors.Is(err, secrets.ErrNoRecoveryCode):
			return nil, NoRecoveryCodeErr
		case errors.Is(err, secrets.ErrInvalidRecoveryCode):
			return nil, InvalidRecoveryCodeErr
		default:
			return nil, err
		}
	}
	status := h.secretsStatus()
	h.Store.NotifySecretsStatusUpdate(status)
	return &status, nil
}
