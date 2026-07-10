package webuihandler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
	"github.com/jptrs93/opsagent/backend/util/jwtu"
)

var InvalidAuthTokenErr = apigen.NewApiErr("Unauthorized", "auth_invalid_token", http.StatusUnauthorized)
var InvalidMasterPasswordErr = apigen.NewApiErr("", "invalid_master_password", http.StatusUnauthorized)
var MasterPasswordNotConfiguredErr = apigen.NewApiErr("", "master_password_not_configured", http.StatusServiceUnavailable)
var MasterPasswordRequiredErr = apigen.NewApiErr("Master password is required", "master_password_required", http.StatusBadRequest)
var UsernameRequiredErr = apigen.NewApiErr("Username is required", "username_required", http.StatusBadRequest)

func newLoginResponse(user *apigen.InternalUser, token string, scopes []string, expiry time.Time) *apigen.LoginResponse {
	return &apigen.LoginResponse{
		Token:  token,
		UserID: user.ID,
		Scopes: append([]string(nil), scopes...),
		Name:   user.Name,
		Expiry: expiry,
	}
}

func (h *Handler) PostV1AuthMaster(ctx apigen.Context, req *apigen.MasterPasswordRequest) (*apigen.LoginResponse, error) {
	if err := h.verifyMasterPassword(req.Password); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Username) == "" {
		return nil, UsernameRequiredErr
	}
	user, err := h.Store.FetchUserMatching(func(u *apigen.InternalUser) bool {
		return u.Name == req.Username
	})
	if errors.Is(err, sqlite.ErrNotFound) {
		id := int32(h.Store.UserCount()) + 1
		webAuthNID, err := authu.GenerateWebAuthnID(32)
		if err != nil {
			return nil, err
		}
		user = &apigen.InternalUser{
			ID:         id,
			WebAuthNID: webAuthNID,
			Name:       req.Username,
		}
		h.Store.WriteUser(user)
	} else if err != nil {
		return nil, err
	}
	token, err := h.jwtAuth.GenerateTokenWith(user.ID, []string{"passkey:create"}, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	return newLoginResponse(user, token, []string{"passkey:create"}, time.Now().Add(10*time.Minute)), nil
}

func (h *Handler) verifyMasterPassword(password string) error {
	masterPasswordHash, err := h.ConfigService.GetMasterPasswordHash()
	if err != nil {
		return err
	}
	if masterPasswordHash == "" {
		return MasterPasswordNotConfiguredErr
	}
	ok, err := authu.VerifyPassword(password, masterPasswordHash)
	if err != nil {
		return fmt.Errorf("verifying master password: %w", err)
	}
	if !ok {
		return InvalidMasterPasswordErr
	}
	return nil
}

func (h *Handler) PostV1AuthMasterPasswordSave(ctx apigen.Context, req *apigen.MasterPasswordSaveRequest) error {
	if req.Password == "" {
		return MasterPasswordRequiredErr
	}
	password := req.Password
	hash, err := authu.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hashing master password: %w", err)
	}
	if err := h.ConfigService.SetMasterPasswordHash(hash); err != nil {
		return err
	}
	return nil
}

func (h *Handler) PostV1AuthMasterPasswordVerify(ctx apigen.Context, req *apigen.MasterPasswordVerifyRequest) error {
	return h.verifyMasterPassword(req.Password)
}

func (h *Handler) GetV1AuthCurrentSession(ctx apigen.Context) (*apigen.LoginResponse, error) {
	claims, user, err := h.jwtAuth.VerifyAndResolveUser(ctx.Token)
	if err != nil {
		return nil, InvalidAuthTokenErr
	}
	expiry, err := jwtu.ExpiryFromClaims(claims)
	if err != nil {
		return nil, err
	}
	return newLoginResponse(user, ctx.Token, jwtu.ScopesFromClaims(claims), expiry), nil
}

// VerifyAuth is the package-level function expected by the generated mux.
func (h *Handler) VerifyAuth(ctx context.Context, _ http.ResponseWriter, r *http.Request, policy apigen.AccessPolicy) (apigen.Context, error) {
	res := apigen.Context{Ctx: ctx}
	if policy.PolicyType == apigen.AccessPolicyType_NO_AUTH {
		return res, nil
	}
	tokenString, ok := strings.CutPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	if !ok || strings.TrimSpace(tokenString) == "" {
		return res, InvalidAuthTokenErr
	}
	claims, user, err := h.jwtAuth.VerifyAndResolveUser(tokenString)
	if err != nil {
		return res, apigen.NewApiErr("invalid token", fmt.Sprintf("err=%v", err), http.StatusUnauthorized)
	}
	sub, _ := claims["sub"].(string)
	res.Ctx = logu.ExtendLogContext(res.Ctx, "user", sub)
	scopes := jwtu.ScopesFromClaims(claims)
	res.User = user
	res.Token = tokenString
	if err := policy.CanAccess(scopes); err != nil {
		return res, err
	}
	return res, nil
}
