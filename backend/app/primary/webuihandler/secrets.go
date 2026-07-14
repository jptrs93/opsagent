package webuihandler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
)

var SecretNameRequiredErr = apigen.NewApiErr("Secret name is required", "secret_name_required", http.StatusBadRequest)
var SecretsLockedErr = apigen.NewApiErr("Secrets store is locked; unlock with the recovery code", "secrets_locked", http.StatusServiceUnavailable)
var InvalidRecoveryCodeErr = apigen.NewApiErr("Invalid recovery code", "secret_invalid_recovery_code", http.StatusBadRequest)
var NoRecoveryCodeErr = apigen.NewApiErr("No recovery code configured", "secret_no_recovery_code", http.StatusBadRequest)
var SecretNotFoundErr = apigen.NewApiErr("Secret not found", "secret_not_found", http.StatusNotFound)
var SecretReservedNameErr = apigen.NewApiErr("Secret name is reserved for OpenDeploy internal use", "secret_reserved_name", http.StatusBadRequest)
var SecretAlreadyExistsErr = apigen.NewApiErr("Secret name already exists", "secret_name_exists", http.StatusBadRequest)

func secretMetaToProto(m secrets.Meta) *apigen.SecretMeta {
	return &apigen.SecretMeta{
		ID:        m.ID,
		Name:      m.Name,
		SpaceID:   m.SpaceID,
		CreatedAt: m.CreatedAt,
		UpdatedBy: m.UpdatedBy,
		Version:   m.Version,
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

func (h *Handler) listAllSecretMetas() []*apigen.SecretMeta {
	metas := h.Secrets.ListAll()
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
	meta, err := h.Secrets.Set(req.Name, req.Value, updatedBy, req.SpaceID)
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
	h.Store.NotifySecretReferenceUpdate(apigen.SecretReference{ID: proto.ID, Name: proto.Name, SpaceID: proto.SpaceID, Version: proto.Version})
	h.Store.NotifySecretMetaUpdate(*proto)
	return proto, nil
}

func (h *Handler) PostV1SecretsRename(ctx apigen.Context, req *apigen.SecretRenameRequest) (*apigen.SecretMeta, error) {
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.NewName) == "" {
		return nil, SecretNameRequiredErr
	}
	meta, err := h.Secrets.Rename(req.Name, req.NewName)
	if err != nil {
		switch {
		case errors.Is(err, secrets.ErrReservedName):
			return nil, SecretReservedNameErr
		case errors.Is(err, secrets.ErrNotFound):
			return nil, SecretNotFoundErr
		case errors.Is(err, secrets.ErrAlreadyExists):
			return nil, SecretAlreadyExistsErr
		default:
			return nil, err
		}
	}
	proto := secretMetaToProto(meta)
	for _, renamed := range h.Secrets.MetasByName(proto.Name) {
		h.Store.NotifySecretReferenceUpdate(apigen.SecretReference{ID: renamed.ID, Name: renamed.Name, SpaceID: renamed.SpaceID, Version: renamed.Version})
	}
	h.Store.NotifySecretMetaUpdate(*proto)
	return proto, nil
}

func (h *Handler) PostV1SecretsReveal(ctx apigen.Context, req *apigen.SecretRevealRequest) (*apigen.SecretRevealResponse, error) {
	if req.ID == 0 && strings.TrimSpace(req.Name) == "" {
		return nil, SecretNameRequiredErr
	}
	var value []byte
	var err error
	if req.ID != 0 {
		value, err = h.Secrets.RevealByID(req.ID)
	} else {
		value, err = h.Secrets.Reveal(req.Name)
	}
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
	unlockReferences := h.ConfigService.LockReferences()
	defer unlockReferences()
	if strings.TrimSpace(req.Name) == "" {
		return SecretNameRequiredErr
	}
	name := strings.TrimSpace(req.Name)
	ids := int32Set(h.Secrets.IDsByName(name))
	if h.settingsUseSecretID(ids) || h.deploymentUsesSecretID(ids) {
		return ReferenceInUseErr
	}
	deletedMetas := h.Secrets.MetasByName(name)
	if len(deletedMetas) == 0 {
		return SecretNotFoundErr
	}
	if err := h.Secrets.Delete(req.Name); err != nil {
		if errors.Is(err, secrets.ErrReservedName) {
			return SecretReservedNameErr
		}
		return err
	}
	for _, meta := range deletedMetas {
		h.Store.NotifySecretReferenceUpdate(apigen.SecretReference{ID: meta.ID, Deleted: true})
		proto := secretMetaToProto(meta)
		proto.Deleted = true
		h.Store.NotifySecretMetaUpdate(*proto)
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
