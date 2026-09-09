package webuihandler

import (
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
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
	case errors.Is(err, secrets.ErrNotFound), errors.Is(err, values.ErrNotFound):
		return SecretNotFoundErr
	case errors.Is(err, values.ErrAlreadyExists):
		return SecretAlreadyExistsErr
	case errors.Is(err, values.ErrNameInvalid):
		return SecretNameInvalidErr
	case errors.Is(err, values.ErrDirectoryNotFound):
		return ValueDirectoryNotFoundErr
	case errors.Is(err, values.ErrSpaceMoveUnsupported):
		return ValueSpaceMoveUnsupportedErr
	}
	return err
}

func (h *Handler) secretsStatus() apigen.SecretsStatusResponse {
	if h.Secrets == nil {
		return apigen.SecretsStatusResponse{}
	}
	unlocked, recoveryConfigured := h.Secrets.Status()
	return apigen.SecretsStatusResponse{
		Unlocked:           unlocked,
		RecoveryConfigured: recoveryConfigured,
	}
}

func (h *Handler) PostV1SecretsList(ctx apigen.Context) (*apigen.SecretEventList, error) {
	return &apigen.SecretEventList{Items: h.filterSecrets(ctx, secrets.List(h.Store.Queries()))}, nil
}

// secretForVersionID resolves the identity that owns a version row. Reveal
// addresses version rows, but access is granted on the identity.
func (h *Handler) secretForVersionID(versionID int32) *apigen.SecretEvent {
	for _, sec := range secrets.List(h.Store.Queries()) {
		for _, id := range secrets.VersionIDs(h.Store.Queries(), sec.SecretID) {
			if id == versionID {
				return sec
			}
		}
	}
	return nil
}

func (h *Handler) PostV1SecretsCreate(ctx apigen.Context, req *apigen.SecretCreateRequest) (*apigen.SecretEvent, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, SecretNameRequiredErr
	}
	if err := h.requireAccess(ctx, vCreate, eSecret, valueSpace(req.SpaceID), 0); err != nil {
		return nil, err
	}
	// A caller supplying the value knows it, so this path also demands reveal;
	// create alone only covers Generate, where the value never leaves the server.
	if err := h.requireAccess(ctx, vReveal, eSecret, valueSpace(req.SpaceID), 0); err != nil {
		return nil, err
	}
	meta, err := h.Secrets.Create(req.Name, req.Value, requestUserID(ctx), req.SpaceID, req.ValueDirectoryID)
	if err != nil {
		return nil, mapSecretErr(err)
	}

	proto, ok := secrets.GetEvent(h.Store.Queries(), meta.SecretID, int64(meta.ID))
	if !ok {
		return nil, SecretNotFoundErr
	}
	return proto, nil
}

func (h *Handler) PostV1SecretsSet(ctx apigen.Context, req *apigen.SecretSetRequest) (*apigen.SecretEvent, error) {
	if req.SecretID == 0 {
		return nil, SecretIDRequiredErr
	}
	if existing, ok := secrets.Get(h.Store.Queries(), req.SecretID); !ok {
		return nil, SecretNotFoundErr
	} else if err := h.requireEntityAccess(ctx, vUpdate, eSecret, int64(existing.SpaceID()), int64(existing.SecretID), SecretNotFoundErr); err != nil {
		return nil, err
	}
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
		nil,
	)
	if err != nil {
		mapped := mapSecretErr(err)
		if mapped != err {
			return nil, mapped
		}
		return nil, versionedValueSetError(err)
	}
	proto, ok := secrets.GetEvent(h.Store.Queries(), meta.SecretID, int64(meta.ID))
	if !ok {
		return nil, SecretNotFoundErr
	}
	return proto, nil
}

// PostV1SecretsGenerate creates a secret the caller never sees: the value is
// produced inside this process, sealed, and only its metadata is returned. It
// is what makes secret:create a safe verb to delegate — an agent holding it can
// mint a credential and reference it from deployment env without ever being
// able to read one back.
func (h *Handler) PostV1SecretsGenerate(ctx apigen.Context, req *apigen.SecretGenerateRequest) (*apigen.SecretEvent, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, SecretNameRequiredErr
	}
	if err := h.requireAccess(ctx, vCreate, eSecret, valueSpace(req.SpaceID), 0); err != nil {
		return nil, err
	}
	// Create-only. Set appends an immutable version rather than replacing one,
	// so without this a caller could bury an operator's credential under a
	// value that neither of them can read back. Rotation stays a browser action.
	// The storage create enforces the namespace law; this early check just maps
	// the common case to a clearer error before generating a value.
	if _, exists := secrets.IDByName(h.Store.Queries(), req.SpaceID, name); exists {
		return nil, SecretAlreadyExistsErr
	}
	value, err := generateSecretValue(req)
	if err != nil {
		return nil, err
	}
	defer secrets.Zero(value)

	meta, err := h.Secrets.Create(name, value, requestUserID(ctx), req.SpaceID, 0)
	if err != nil {
		return nil, mapSecretErr(err)
	}

	proto, ok := secrets.GetEvent(h.Store.Queries(), meta.SecretID, int64(meta.ID))
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

func (h *Handler) PostV1SecretsRename(ctx apigen.Context, req *apigen.SecretRenameRequest) (*apigen.SecretEvent, error) {
	if req.SecretID == 0 {
		return nil, SecretIDRequiredErr
	}
	if strings.TrimSpace(req.NewName) == "" {
		return nil, SecretNameRequiredErr
	}
	if existing, ok := secrets.Get(h.Store.Queries(), req.SecretID); !ok {
		return nil, SecretNotFoundErr
	} else if err := h.requireEntityAccess(ctx, vUpdate, eSecret, int64(existing.SpaceID()), int64(existing.SecretID), SecretNotFoundErr); err != nil {
		return nil, err
	}
	if err := h.Secrets.Rename(req.SecretID, req.NewName); err != nil {
		return nil, mapSecretErr(err)
	}
	proto, ok := secrets.Get(h.Store.Queries(), req.SecretID)
	if !ok {
		return nil, SecretNotFoundErr
	}

	return proto, nil
}

// PostV1SecretsMove relocates a secret within its space's folder tree, or —
// when space_id names another space — moves it there. Version rows and every
// pinned reference are untouched either way. A cross-space move is allowed
// only while nothing outside the destination space references the secret:
// deployments must be able to keep their pins within their own or the global
// space, so a move *to* the global space is always reference-safe, and a
// settings reference pins the value to the global space. Reserved opendeploy
// secrets stay put: install/restore flows find them by name in the space
// root, so moving one would strand it.
func (h *Handler) PostV1SecretsMove(ctx apigen.Context, req *apigen.SecretMoveRequest) (*apigen.SecretEvent, error) {
	if req.SecretID == 0 {
		return nil, SecretIDRequiredErr
	}
	sec, ok := secrets.Get(h.Store.Queries(), req.SecretID)
	if !ok {
		return nil, SecretNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vUpdate, eSecret, int64(sec.SpaceID()), int64(sec.SecretID), SecretNotFoundErr); err != nil {
		return nil, err
	}
	destSpace := nodes.NormalizedUserSpaceID(req.SpaceID)
	spaceChanging := req.SpaceID != 0 && destSpace != sec.SpaceID()
	// Moving into another space also needs the right to create a secret there.
	if spaceChanging {
		if err := h.requireAccess(ctx, vCreate, eSecret, valueSpace(req.SpaceID), 0); err != nil {
			return nil, err
		}
	}
	if isReservedSecretMetaName(sec.Value.Fs.Name) {
		return nil, SecretReservedNameErr
	}
	if spaceChanging {
		validate := func(q *pq.Queries) error {
			if destSpace == nodes.DefaultSpaceID {
				return nil
			}
			ids := deployments.Int32Set(secrets.VersionIDs(h.Store.Queries(), req.SecretID))
			if h.settingsUseSecretID(ids) {
				return deployments.MoveReferencesOutsideSpaceErr
			}
			live, err := nodes.ReadLiveState(ctx, q)
			if err != nil {
				return err
			}
			if deployments.ReferencesOutsideSpace(live, ids, runtimeinputs.SecretRefs, destSpace) {
				return deployments.MoveReferencesOutsideSpaceErr
			}
			return nil
		}
		if err := h.Secrets.MoveSpace(req.SecretID, req.SpaceID, req.ValueDirectoryID, ctx.AttributionUserID(), validate); err != nil {
			return nil, mapSecretErr(err)
		}
		// Clients that saw the old space but cannot see the new one would
		// otherwise keep a stale row forever — updates a user cannot view are
		// dropped, and nothing else says "gone". The tombstone speaks to them;
		// the update below re-adds the row for everyone who sees the destination.

	} else if err := secrets.MoveDirectory(h.Store, req.SecretID, req.ValueDirectoryID); err != nil {
		return nil, mapSecretErr(err)
	}
	proto, ok := secrets.Get(h.Store.Queries(), req.SecretID)
	if !ok {
		return nil, SecretNotFoundErr
	}

	return proto, nil
}

func (h *Handler) PostV1SecretsReveal(ctx apigen.Context, req *apigen.SecretRevealRequest) (*apigen.SecretRevealResponse, error) {
	if req.ID == 0 {
		return nil, SecretIDRequiredErr
	}
	sec := h.secretForVersionID(req.ID)
	if sec == nil {
		return nil, SecretNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vReveal, eSecret, int64(sec.SpaceID()), int64(sec.SecretID), SecretNotFoundErr); err != nil {
		return nil, err
	}
	value, err := h.Secrets.RevealByID(req.ID)
	if err != nil {
		return nil, mapSecretErr(err)
	}
	return &apigen.SecretRevealResponse{Value: value}, nil
}

func (h *Handler) PostV1SecretsDelete(ctx apigen.Context, req *apigen.SecretDeleteRequest) error {
	if req.SecretID == 0 {
		return SecretIDRequiredErr
	}
	sec, ok := secrets.Get(h.Store.Queries(), req.SecretID)
	if !ok {
		return SecretNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vDelete, eSecret, int64(sec.SpaceID()), int64(sec.SecretID), SecretNotFoundErr); err != nil {
		return err
	}
	if isReservedSecretMetaName(sec.Value.Fs.Name) {
		return SecretReservedNameErr
	}
	validate := func(q *pq.Queries) error {
		ids := deployments.Int32Set(secrets.VersionIDs(h.Store.Queries(), req.SecretID))
		live, err := nodes.ReadLiveState(ctx, q)
		if err != nil {
			return err
		}
		details := append(h.settingsSecretRefDetails(ids), deployments.RefDetails(ctx, q, live, ids, runtimeinputs.SecretRefs)...)
		if len(details) > 0 {
			return deployments.ReferenceInUseDetailErr("Secret", details)
		}
		return nil
	}
	if err := h.Secrets.Delete(req.SecretID, validate); err != nil {
		return mapSecretErr(err)
	}

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

func (h *Handler) PostV1SecretsStatus(ctx apigen.Context) (*apigen.SecretsStatusResponse, error) {
	status := h.secretsStatus()
	return &status, nil
}

func (h *Handler) PostV1SecretsRotateRecoveryCode(ctx apigen.Context) (*apigen.SecretRecoveryCodeResponse, error) {
	// Recovery-code rotation and unlock act on the whole secrets store, so they
	// are cluster-level operations rather than any one secret's.
	if err := h.requireAccess(ctx, vUpdate, eCluster, 0, 0); err != nil {
		return nil, err
	}
	code, err := h.Secrets.GenerateRecoveryCode()
	if err != nil {
		return nil, mapSecretErr(err)
	}
	status := h.secretsStatus()
	h.secretsUpdates.Notify(status)
	return &apigen.SecretRecoveryCodeResponse{Code: code}, nil
}

func (h *Handler) PostV1SecretsUnlock(ctx apigen.Context, req *apigen.SecretUnlockRequest) (*apigen.SecretsStatusResponse, error) {
	if err := h.requireAccess(ctx, vUpdate, eCluster, 0, 0); err != nil {
		return nil, err
	}
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
	h.secretsUpdates.Notify(status)
	return &status, nil
}
