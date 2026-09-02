package webuihandler

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/authz"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/util/jwtu"
)

const (
	// ScopePasskeyCreate is the bootstrap scope, good only for enrolling a passkey.
	ScopePasskeyCreate = "passkey:create"
	// ScopeDefault covers all operator access. What a session may actually do is
	// decided by the authz layer per user, entity, and space; scopes only
	// separate a bootstrap token from a real one.
	ScopeDefault = "default"
)

// defaultUserScopes is what a passkey login grants.
var defaultUserScopes = []string{ScopeDefault}

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
	if errors.Is(err, state.ErrNotFound) {
		id := int32(h.Store.UserCount()) + 1
		webAuthNID, generateErr := authu.GenerateWebAuthnID(32)
		if generateErr != nil {
			return nil, generateErr
		}
		user = &apigen.InternalUser{
			ID:         id,
			WebAuthNID: webAuthNID,
			Name:       req.Username,
		}
		h.Store.WriteUser(user)
		if _, grantErr := h.Authz.CreateGrant(&apigen.AuthzGrantRecord{
			UserID:     int64(user.ID),
			TemplateID: authz.ClusterAdminTemplateID,
			Grant:      &apigen.AuthzGrant{},
		}); grantErr != nil {
			return nil, grantErr
		}
	} else if err != nil {
		return nil, err
	}
	token, err := h.jwtAuth.GenerateTokenWith(user.ID, []string{ScopePasskeyCreate}, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	return newLoginResponse(user, token, []string{ScopePasskeyCreate}, time.Now().Add(10*time.Minute)), nil
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
	if err := requireHuman(ctx); err != nil {
		return err
	}
	if err := h.requireAccess(ctx, vUpdate, eCluster, 0, 0); err != nil {
		return err
	}
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
	if err := requireHuman(ctx); err != nil {
		return err
	}
	if err := h.requireAccess(ctx, vView, eCluster, 0, 0); err != nil {
		return err
	}
	return h.verifyMasterPassword(req.Password)
}

// GetV1AuthCurrentSession returns the caller's own session, including the
// bearer token they presented — the web UI relies on that to restore a stored
// session. It hands back only what the caller already holds, but any client
// that must not expose its credential (agents in particular) should not echo
// the response.
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

// verifyAgentSession enforces revocation for tokens that carry a jti. Browser
// session and bootstrap tokens carry none and keep the stateless fast path, so
// this costs one indexed read only on agent-token requests.
func (h *Handler) verifyAgentSession(claims map[string]any, token string) error {
	sessionID, _ := claims["jti"].(string)
	if sessionID == "" {
		return nil
	}
	rec, err := h.Store.FetchAgentSession(sessionID)
	if errors.Is(err, state.ErrNotFound) {
		return InvalidAuthTokenErr
	}
	if err != nil {
		return fmt.Errorf("fetching agent session: %w", err)
	}
	// Only an approved-and-collected session carries a working token. Pending,
	// rejected, and revoked rows all fail here.
	if rec.Status != apigen.AgentSessionStatus_AGENT_SESSION_APPROVED {
		return InvalidAuthTokenErr
	}
	// The signature already proves authenticity; this additionally ties the
	// token to the exact row, so a reused jti cannot ride another session.
	if subtle.ConstantTimeCompare(rec.TokenHash, hashAgentToken(token)) != 1 {
		return InvalidAuthTokenErr
	}
	return nil
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
	if err := h.verifyAgentSession(claims, tokenString); err != nil {
		return res, err
	}
	if err := h.verifyPersonalSession(ctx, claims, tokenString); err != nil {
		return res, err
	}
	// Agent-session tokens act with delegated authority: authz rules without
	// DelegationAllowed will not match them. Set on a copy — FetchUserByID
	// decodes fresh, but that is its contract, not this call site's to assume.
	if jti, _ := claims["jti"].(string); jti != "" {
		delegated := *user
		delegated.Delegated = true
		user = &delegated
	}
	sub, _ := claims["sub"].(string)
	res.Ctx = logu.AddKV(res.Ctx, "user", sub)
	scopes := jwtu.ScopesFromClaims(claims)
	res.User = user
	res.Token = tokenString
	if err := policy.CanAccess(scopes); err != nil {
		return res, err
	}
	return res, nil
}
