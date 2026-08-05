package webuihandler

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
	"github.com/jptrs93/opsagent/backend/util/jwtu"
)

const (
	// ScopePasskeyCreate is the bootstrap scope, good only for enrolling a passkey.
	ScopePasskeyCreate = "passkey:create"
	// ScopeDefault covers ordinary operator access: deployments, assets, configs,
	// and secret metadata.
	ScopeDefault = "default"
	// ScopeSecretsAccess additionally permits revealing and changing secret
	// values. Held separately so it can be withheld from generated API tokens.
	ScopeSecretsAccess = "secrets_access"
)

// defaultUserScopes is what a passkey login grants. Operators get full access in
// the browser, where the session is bound to a physical authenticator.
var defaultUserScopes = []string{ScopeDefault, ScopeSecretsAccess}

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

// agentSessionTTL is how long an agent session stays valid. Kept shorter than
// the 2-day browser session because these tokens are pasted into shells and end
// up in history files and CI logs.
const agentSessionTTL = 12 * time.Hour

// agentTokenPrefixLen is how much of a token is kept in the clear so an
// operator can tell two sessions apart in the list. Short enough to be useless
// on its own.
const agentTokenPrefixLen = 12

var AgentSessionNotFoundErr = apigen.NewApiErr("Session not found", "agent_session_not_found", http.StatusNotFound)

// agentSessionScopes narrows a session's scopes to what an agent token may
// carry. Secret values are the one thing withheld: these tokens live for 12
// hours in shell history, environment files, and CI logs, which is a much wider
// blast radius than the browser session they were minted from.
func agentSessionScopes(sessionScopes []string) []string {
	out := make([]string, 0, len(sessionScopes))
	for _, scope := range sessionScopes {
		if scope == ScopeSecretsAccess {
			continue
		}
		out = append(out, scope)
	}
	return out
}

func hashAgentToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func agentTokenPrefix(token string) string {
	if len(token) <= agentTokenPrefixLen {
		return token
	}
	return token[:agentTokenPrefixLen]
}

func agentSessionToProto(rec sqlite.AgentSessionRecord) *apigen.AgentSession {
	return &apigen.AgentSession{
		ID:          rec.ID,
		CreatedAt:   rec.CreatedAt,
		ExpiresAt:   rec.ExpiresAt,
		TokenPrefix: rec.TokenPrefix,
		Scopes:      append([]string(nil), rec.Scopes...),
		Revoked:     !rec.RevokedAt.IsZero(),
	}
}

// PostV1AgentSessionsCreate starts an agent session and returns its bearer
// token. The token carries the caller's own scopes minus the withheld ones, so
// it can never grant more than the session that requested it.
//
// Only the token's SHA-256 is stored. The plaintext is returned here and never
// again, so a copy of primary.db — including an off-box backup — carries no
// usable credential.
func (h *Handler) PostV1AgentSessionsCreate(ctx apigen.Context) (*apigen.AgentSessionCreated, error) {
	claims, user, err := h.jwtAuth.VerifyAndResolveUser(ctx.Token)
	if err != nil {
		return nil, InvalidAuthTokenErr
	}
	scopes := agentSessionScopes(jwtu.ScopesFromClaims(claims))
	if len(scopes) == 0 {
		return nil, InvalidAuthTokenErr
	}
	sessionID, err := authu.GenerateRandomToken(16)
	if err != nil {
		return nil, fmt.Errorf("generating agent session id: %w", err)
	}
	now := time.Now()
	expiry := now.Add(agentSessionTTL)
	// Signed here rather than through GenerateTokenWith so the session id can
	// ride along as jti; VerifyAuth uses it to find the row. The sub encoding
	// matches what authu does for a non-string subject (json.Marshal of the
	// int32 user id).
	token, err := h.jwtAuth.Sign(jwt.MapClaims{
		"sub":    strconv.FormatInt(int64(user.ID), 10),
		"scopes": scopes,
		"exp":    expiry.Unix(),
		"iat":    now.Unix(),
		"jti":    sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("generating agent session token: %w", err)
	}
	rec := sqlite.AgentSessionRecord{
		ID:          sessionID,
		UserID:      user.ID,
		CreatedAt:   now,
		ExpiresAt:   expiry,
		TokenHash:   hashAgentToken(token),
		TokenPrefix: agentTokenPrefix(token),
		Scopes:      scopes,
	}
	if err := h.Store.InsertAgentSession(rec); err != nil {
		return nil, fmt.Errorf("storing agent session: %w", err)
	}
	slog.InfoContext(ctx, "started agent session", "session", sessionID, "ttl", agentSessionTTL.String(), "scopes", scopes)
	return &apigen.AgentSessionCreated{
		Token:   token,
		Session: agentSessionToProto(rec),
	}, nil
}

// PostV1AgentSessionsList returns the caller's own sessions, newest first.
// There is no cross-user view: every operator holds the same scopes, so this
// filter is a scoping convenience rather than an isolation boundary.
func (h *Handler) PostV1AgentSessionsList(ctx apigen.Context, _ *apigen.EmptyRequest) (*apigen.AgentSessionList, error) {
	if ctx.User == nil {
		return nil, InvalidAuthTokenErr
	}
	records, err := h.Store.ListAgentSessionsForUser(ctx.User.ID)
	if err != nil {
		return nil, fmt.Errorf("listing agent sessions: %w", err)
	}
	items := make([]*apigen.AgentSession, 0, len(records))
	for _, rec := range records {
		items = append(items, agentSessionToProto(rec))
	}
	return &apigen.AgentSessionList{Items: items}, nil
}

// PostV1AgentSessionsRevoke stops a session immediately. VerifyAuth rejects the
// token on its next request, so this is real revocation rather than a display
// change.
func (h *Handler) PostV1AgentSessionsRevoke(ctx apigen.Context, req *apigen.AgentSessionRevokeRequest) error {
	if ctx.User == nil {
		return InvalidAuthTokenErr
	}
	if strings.TrimSpace(req.ID) == "" {
		return AgentSessionNotFoundErr
	}
	// Scoped by user id, so a guessed id cannot revoke someone else's session.
	rec, err := h.Store.FetchAgentSession(req.ID)
	if errors.Is(err, sqlite.ErrNotFound) || (err == nil && rec.UserID != ctx.User.ID) {
		return AgentSessionNotFoundErr
	}
	if err != nil {
		return fmt.Errorf("fetching agent session: %w", err)
	}
	if err := h.Store.RevokeAgentSession(req.ID, ctx.User.ID, time.Now()); err != nil {
		return fmt.Errorf("revoking agent session: %w", err)
	}
	slog.InfoContext(ctx, "revoked agent session", "session", req.ID)
	return nil
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
	if errors.Is(err, sqlite.ErrNotFound) {
		return InvalidAuthTokenErr
	}
	if err != nil {
		return fmt.Errorf("fetching agent session: %w", err)
	}
	if !rec.RevokedAt.IsZero() {
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
