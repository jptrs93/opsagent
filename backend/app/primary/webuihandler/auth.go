package webuihandler

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/agentsessions"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/users"
)

// rootSessionTTL bounds a master-password exchange: long enough to enrol or
// recover a user, short enough that a leaked root token is useless by the
// time anyone reads the log it landed in.
const rootSessionTTL = 10 * time.Minute

var InvalidAuthTokenErr = apigen.NewApiErr("Unauthorized", "auth_invalid_token", http.StatusUnauthorized)
var InvalidMasterPasswordErr = apigen.NewApiErr("", "invalid_master_password", http.StatusUnauthorized)
var MasterPasswordNotConfiguredErr = apigen.NewApiErr("", "master_password_not_configured", http.StatusServiceUnavailable)
var MasterPasswordRequiredErr = apigen.NewApiErr("Master password is required", "master_password_required", http.StatusBadRequest)
var UsernameRequiredErr = apigen.NewApiErr("Username is required", "username_required", http.StatusBadRequest)

// newLoginResponse describes a session to its holder; user is nil for a
// root session.
func newLoginResponse(user *apigen.User, token string, kind apigen.UserSessionKind, expiry time.Time, sessionID string) *apigen.LoginResponse {
	res := &apigen.LoginResponse{Token: token, Kind: kind, Expiry: expiry, SessionID: sessionID}
	if user != nil {
		res.UserID = user.ID
		res.Name = user.DisplayName()
	}
	return res
}

// PostV1AuthMaster exchanges the master password for a root session: no
// user, a short life, and only the enrol-and-recover routes open to it.
func (h *Handler) PostV1AuthMaster(ctx apigen.Context, req *apigen.MasterPasswordRequest) (*apigen.LoginResponse, error) {
	if err := h.verifyMasterPassword(req.Password); err != nil {
		return nil, err
	}
	return h.startUserSession(ctx, nil, apigen.UserSessionKind_USER_SESSION_KIND_ROOT, rootSessionTTL)
}

func (h *Handler) verifyMasterPassword(password string) error {
	masterPasswordHash, err := h.SystemConfig.GetMasterPasswordHash()
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
	if err := h.SystemConfig.SetMasterPasswordHash(hash, ctx.AttributionUserID()); err != nil {
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
	if ctx.User == nil && ctx.SessionKind != apigen.UserSessionKind_USER_SESSION_KIND_ROOT {
		return nil, InvalidAuthTokenErr
	}
	return newLoginResponse(ctx.User, ctx.Token, ctx.SessionKind, ctx.SessionExpiresAt, ctx.SessionID), nil
}

// resolveAgentSession turns an a_ token into its user and session. Only an
// approved-and-collected session carries a working token: pending, rejected,
// revoked, and expired rows all fail here, and the hash check ties the token
// to the exact row so a reused id cannot ride another session.
func (h *Handler) resolveAgentSession(sessionID, token string, now time.Time) (*apigen.User, agentsessions.Record, error) {
	rec, err := h.agentSessions().FetchAgentSession(sessionID)
	if errors.Is(err, agentsessions.ErrNotFound) {
		return nil, rec, InvalidAuthTokenErr
	}
	if err != nil {
		return nil, rec, fmt.Errorf("fetching agent session: %w", err)
	}
	if rec.Status != apigen.AgentSessionStatus_AGENT_SESSION_STATUS_APPROVED || !rec.Collected() || !now.Before(rec.ExpiresAt) {
		return nil, rec, InvalidAuthTokenErr
	}
	if subtle.ConstantTimeCompare(rec.TokenHash, hashToken(token)) != 1 {
		return nil, rec, InvalidAuthTokenErr
	}
	user, err := users.ByID(h.Store.Queries(), rec.UserID)
	if errors.Is(err, users.ErrNotFound) {
		return nil, rec, InvalidAuthTokenErr
	}
	if err != nil {
		return nil, rec, fmt.Errorf("fetching session user: %w", err)
	}
	return user, rec, nil
}

// VerifyAuth is the package-level function expected by the generated mux. A
// bearer token is looked up by its session id, checked against the stored
// hash, and rejected if the session is revoked or expired; there is no
// signature to verify and nothing to trust in the token itself. A link token
// is never a bearer and fails before any lookup. The route policy then only
// asks whether a root session may use the route; what a full session may do
// is the authz layer's decision.
func (h *Handler) VerifyAuth(ctx context.Context, _ http.ResponseWriter, r *http.Request, policy apigen.AccessPolicy) (apigen.Context, error) {
	res := apigen.Context{Ctx: ctx}
	if policy.PolicyType == apigen.AccessPolicyType_NO_AUTH {
		return res, nil
	}
	tokenString, ok := strings.CutPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	tokenString = strings.TrimSpace(tokenString)
	if !ok || tokenString == "" {
		return res, InvalidAuthTokenErr
	}
	kind, sessionID, ok := splitToken(tokenString)
	if !ok {
		return res, InvalidAuthTokenErr
	}
	now := time.Now()
	switch kind {
	case userTokenKind:
		user, rec, err := h.resolveUserSession(sessionID, tokenString, now)
		if err != nil {
			return res, err
		}
		res.User, res.SessionKind, res.SessionExpiresAt = user, rec.Kind, rec.ExpiresAt
	case agentTokenKind:
		user, rec, err := h.resolveAgentSession(sessionID, tokenString, now)
		if err != nil {
			return res, err
		}
		// Agent-session tokens act with delegated authority: authz rules without
		// DelegationAllowed will not match them.
		res.User, res.Delegated, res.SessionKind, res.SessionExpiresAt = user, true, apigen.UserSessionKind_USER_SESSION_KIND_FULL, rec.ExpiresAt
	default:
		return res, InvalidAuthTokenErr
	}
	res.SessionID = sessionID
	res.Token = tokenString
	if res.User != nil {
		res.Ctx = logu.AddKV(res.Ctx, "user", strconv.FormatUint(res.User.ID, 10))
	} else {
		res.Ctx = logu.AddKV(res.Ctx, "user", "root")
	}
	if err := policy.CanAccess(res.SessionKind); err != nil {
		return res, err
	}
	return res, nil
}
