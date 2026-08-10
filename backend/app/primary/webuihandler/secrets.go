package webuihandler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var SecretNameRequiredErr = apigen.NewApiErr("Secret name is required", "secret_name_required", http.StatusBadRequest)
var SecretIDRequiredErr = apigen.NewApiErr("Secret id is required", "secret_id_required", http.StatusBadRequest)
var SecretNameInvalidErr = apigen.NewApiErr("Secret name is not a valid file name", "secret_name_invalid", http.StatusBadRequest)
var SecretsLockedErr = apigen.NewApiErr("Secrets store is locked; unlock with the recovery code", "secrets_locked", http.StatusServiceUnavailable)
var InvalidRecoveryCodeErr = apigen.NewApiErr("Invalid recovery code", "secret_invalid_recovery_code", http.StatusBadRequest)
var NoRecoveryCodeErr = apigen.NewApiErr("No recovery code configured", "secret_no_recovery_code", http.StatusBadRequest)
var SecretNotFoundErr = apigen.NewApiErr("Secret not found", "secret_not_found", http.StatusNotFound)
var SecretReservedNameErr = apigen.NewApiErr("Secret name is reserved for OpenDeploy internal use", "secret_reserved_name", http.StatusBadRequest)
var SecretAlreadyExistsErr = apigen.NewApiErr("Secret name already exists", "secret_name_exists", http.StatusBadRequest)
var SecretGeneratorRequiredErr = apigen.NewApiErr("A generator specification is required", "secret_generator_required", http.StatusBadRequest)

// Rejected rather than clamped: a caller cannot read a generated value back, so
// silently widening a length it asked for would go unnoticed.
var SecretPasswordLengthErr = apigen.NewApiErr(
	fmt.Sprintf("Password length must be between %d and %d", secrets.MinPasswordLength, secrets.MaxPasswordLength),
	"secret_password_length", http.StatusBadRequest)

func mapSecretErr(err error) error {
	switch {
	case errors.Is(err, secrets.ErrLocked):
		return SecretsLockedErr
	case errors.Is(err, secrets.ErrReservedName):
		return SecretReservedNameErr
	case errors.Is(err, secrets.ErrNotFound), errors.Is(err, state.ErrValueNotFound):
		return SecretNotFoundErr
	case errors.Is(err, state.ErrValueAlreadyExists):
		return SecretAlreadyExistsErr
	case errors.Is(err, state.ErrValueNameInvalid):
		return SecretNameInvalidErr
	}
	return err
}

func (h *Handler) secretsStatus() apigen.SecretsStatusResponse {
	unlocked, recoveryConfigured := h.Secrets.Status()
	return apigen.SecretsStatusResponse{
		Unlocked:           unlocked,
		RecoveryConfigured: recoveryConfigured,
	}
}

// notifySecretMeta pushes the secret's current meta into the state stream.
func (h *Handler) notifySecretMeta(secretID int32) {
	if meta, ok := h.Store.GetSecretMeta(secretID); ok {
		h.Store.NotifySecretMetaUpdate(*meta)
	}
}

func (h *Handler) PostV1SecretsList(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.SecretList, error) {
	return &apigen.SecretList{Items: h.Store.ListSecretMetas()}, nil
}

func (h *Handler) PostV1SecretsCreate(ctx apigen.Context, req *apigen.SecretCreateRequest) (*apigen.SecretMeta, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, SecretNameRequiredErr
	}
	meta, err := h.Secrets.Create(req.Name, req.Value, requestUserID(ctx), req.SpaceID)
	if err != nil {
		return nil, mapSecretErr(err)
	}
	h.notifySecretMeta(meta.SecretID)
	proto, ok := h.Store.GetSecretMeta(meta.SecretID)
	if !ok {
		return nil, SecretNotFoundErr
	}
	return proto, nil
}

func (h *Handler) PostV1SecretsSet(ctx apigen.Context, req *apigen.SecretSetRequest) (*apigen.SecretMeta, error) {
	if req.SecretID == 0 {
		return nil, SecretIDRequiredErr
	}
	unlockReferences := h.ConfigService.LockReferences()
	defer unlockReferences()
	expected, err := requestedDeploymentVersions(req.UpdateReferencingDeployments, req.ReferencingDeployments)
	if err != nil {
		return nil, err
	}
	meta, err := h.Secrets.SetWithDeploymentUpdates(
		req.SecretID,
		req.Value,
		requestUserID(ctx),
		req.UpdateReferencingDeployments,
		expected,
		func(committed secrets.Meta) {
			h.notifySecretMeta(committed.SecretID)
		},
	)
	if err != nil {
		mapped := mapSecretErr(err)
		if mapped != err {
			return nil, mapped
		}
		return nil, versionedValueSetError(err)
	}
	proto, ok := h.Store.GetSecretMeta(meta.SecretID)
	if !ok {
		return nil, SecretNotFoundErr
	}
	return proto, nil
}

// PostV1SecretsGenerate creates a secret the caller never sees. It is the only
// route that writes a secret value without "secrets_access": the value is
// produced inside this process, sealed, and only its metadata is returned, so
// an agent token can mint a credential and reference it from deployment env
// without ever being able to read it.
func (h *Handler) PostV1SecretsGenerate(ctx apigen.Context, req *apigen.SecretGenerateRequest) (*apigen.SecretMeta, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, SecretNameRequiredErr
	}
	// Create-only. Set appends an immutable version rather than replacing one,
	// so without this a caller could bury an operator's credential under a
	// value that neither of them can read back. Rotation stays a browser action.
	// The storage create enforces the namespace law; this early check just maps
	// the common case to a clearer error before generating a value.
	if _, exists := h.Store.GetSecretInRootByName(req.SpaceID, name); exists {
		return nil, SecretAlreadyExistsErr
	}
	value, err := generateSecretValue(req)
	if err != nil {
		return nil, err
	}
	defer secrets.Zero(value)

	meta, err := h.Secrets.Create(name, value, requestUserID(ctx), req.SpaceID)
	if err != nil {
		return nil, mapSecretErr(err)
	}
	h.notifySecretMeta(meta.SecretID)
	proto, ok := h.Store.GetSecretMeta(meta.SecretID)
	if !ok {
		return nil, SecretNotFoundErr
	}
	return proto, nil
}

// generateSecretValue dispatches on which specification the request carries.
// Exactly one may be set; further generators become further cases here, which
// is why the specification is a nested message rather than fields hung off the
// request itself.
func generateSecretValue(req *apigen.SecretGenerateRequest) ([]byte, error) {
	switch {
	case req.Password != nil:
		value, err := secrets.GeneratePassword(int(req.Password.Length), req.Password.IncludeSymbols)
		if err != nil {
			if errors.Is(err, secrets.ErrPasswordLength) {
				return nil, SecretPasswordLengthErr
			}
			return nil, err
		}
		return value, nil
	default:
		return nil, SecretGeneratorRequiredErr
	}
}

func (h *Handler) PostV1SecretsRename(ctx apigen.Context, req *apigen.SecretRenameRequest) (*apigen.SecretMeta, error) {
	if req.SecretID == 0 {
		return nil, SecretIDRequiredErr
	}
	if strings.TrimSpace(req.NewName) == "" {
		return nil, SecretNameRequiredErr
	}
	if err := h.Secrets.Rename(req.SecretID, req.NewName); err != nil {
		return nil, mapSecretErr(err)
	}
	proto, ok := h.Store.GetSecretMeta(req.SecretID)
	if !ok {
		return nil, SecretNotFoundErr
	}
	h.Store.NotifySecretMetaUpdate(*proto)
	return proto, nil
}

func (h *Handler) PostV1SecretsReveal(ctx apigen.Context, req *apigen.SecretRevealRequest) (*apigen.SecretRevealResponse, error) {
	if req.ID == 0 {
		return nil, SecretIDRequiredErr
	}
	value, err := h.Secrets.RevealByID(req.ID)
	if err != nil {
		return nil, mapSecretErr(err)
	}
	return &apigen.SecretRevealResponse{Value: value}, nil
}

func (h *Handler) PostV1SecretsDelete(ctx apigen.Context, req *apigen.SecretDeleteRequest) error {
	unlockReferences := h.ConfigService.LockReferences()
	defer unlockReferences()
	if req.SecretID == 0 {
		return SecretIDRequiredErr
	}
	meta, ok := h.Store.GetSecretMeta(req.SecretID)
	if !ok {
		return SecretNotFoundErr
	}
	if isReservedSecretMetaName(meta.Name) {
		return SecretReservedNameErr
	}
	ids := int32Set(h.Store.SecretVersionIDs(req.SecretID))
	if h.settingsUseSecretID(ids) || h.deploymentUsesSecretID(ids) {
		return ReferenceInUseErr
	}
	if err := h.Secrets.Delete(req.SecretID); err != nil {
		return mapSecretErr(err)
	}
	meta.Deleted = true
	h.Store.NotifySecretMetaUpdate(*meta)
	return nil
}

// isReservedSecretMetaName mirrors the Manager's reserved-namespace guard for
// the delete path, which no longer flows through a name-based Manager call.
func isReservedSecretMetaName(name string) bool {
	name = strings.TrimSpace(name)
	if name == secrets.TLSCertPEMSecretName {
		return false
	}
	return strings.HasPrefix(name, "opendeploy.") && !strings.HasPrefix(name, "opendeploy.config.")
}

func (h *Handler) PostV1SecretsStatus(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.SecretsStatusResponse, error) {
	status := h.secretsStatus()
	return &status, nil
}

func (h *Handler) PostV1SecretsRotateRecoveryCode(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.SecretRecoveryCodeResponse, error) {
	code, err := h.Secrets.GenerateRecoveryCode()
	if err != nil {
		return nil, mapSecretErr(err)
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
