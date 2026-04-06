package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

var InvalidAuthTokenErr = apigen.NewApiErr("Unauthorized", "auth_invalid_token", http.StatusUnauthorized)
var InvalidMasterPasswordErr = apigen.NewApiErr("", "invalid_master_password", http.StatusUnauthorized)
var MasterPasswordNotConfiguredErr = apigen.NewApiErr("", "master_password_not_configured", http.StatusServiceUnavailable)
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

func scopesFromClaims(claims map[string]any) []string {
	if direct, ok := claims["scopes"].([]string); ok {
		return append([]string(nil), direct...)
	}
	scopesRaw, _ := claims["scopes"].([]any)
	scopes := make([]string, 0, len(scopesRaw))
	for _, s := range scopesRaw {
		if str, ok := s.(string); ok {
			scopes = append(scopes, str)
		}
	}
	return scopes
}

func expiryFromClaims(claims map[string]any) (time.Time, error) {
	exp, ok := claims["exp"].(float64)
	if !ok {
		return time.Time{}, fmt.Errorf("missing exp claim")
	}
	return time.Unix(int64(exp), 0), nil
}

func (h *Handler) PostV1AuthMaster(ctx apigen.Context, req *apigen.MasterPasswordRequest) (*apigen.LoginResponse, error) {
	if ainit.Config.MasterPasswordHash == "" {
		return nil, MasterPasswordNotConfiguredErr
	}
	ok, err := authu.VerifyPassword(req.Password, ainit.Config.MasterPasswordHash)
	if err != nil {
		return nil, fmt.Errorf("verifying master password: %w", err)
	}
	if !ok {
		return nil, InvalidMasterPasswordErr
	}
	if strings.TrimSpace(req.Username) == "" {
		return nil, UsernameRequiredErr
	}

	// Find or create user by name. The Fetch is an auth-helper read where
	// ErrNotFound is a legitimate "first login" signal, so we keep the
	// non-Must variant; the Write follows the global storage policy.
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

func (h *Handler) GetV1AuthCurrentSession(ctx apigen.Context) (*apigen.LoginResponse, error) {
	claims, user, err := h.jwtAuth.VerifyAndResolveUser(ctx.Token)
	if err != nil {
		return nil, InvalidAuthTokenErr
	}
	expiry, err := expiryFromClaims(claims)
	if err != nil {
		return nil, err
	}
	return newLoginResponse(user, ctx.Token, scopesFromClaims(claims), expiry), nil
}

// VerifyAuth is the package-level function expected by the generated mux.
func (h *Handler) VerifyAuth(ctx context.Context, r *http.Request, policy apigen.AccessPolicy) (apigen.Context, error) {
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
	scopes := scopesFromClaims(claims)
	res.User = user
	res.Token = tokenString
	if err := policy.CanAccess(scopes); err != nil {
		return res, err
	}
	return res, nil
}
