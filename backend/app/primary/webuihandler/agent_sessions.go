package webuihandler

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/middleware/clientaddr"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
	"github.com/jptrs93/opsagent/backend/util/jwtu"
)

// agentSessionTTL is how long an agent session stays valid once its token is
// collected. Kept shorter than the 2-day browser session because these tokens
// are pasted into shells and end up in history files and CI logs.
const agentSessionTTL = 6 * time.Hour

// agentSessionPendingTTL is how long a request waits for approval, and
// agentSessionPickupTTL how long an approved session waits to be collected.
// Both exist to keep the one-open-request-per-user rule from becoming a denial
// of service: request-start is unauthenticated, so without an expiry a single
// hostile request would occupy an operator's only slot indefinitely.
//
// Variables rather than constants so tests can shorten them; nothing else
// writes to them.
var (
	agentSessionPendingTTL = 10 * time.Minute
	agentSessionPickupTTL  = 15 * time.Minute
)

// agentTokenPrefixLen is how much of a token is kept in the clear so an
// operator can tell two sessions apart in the list. Short enough to be useless
// on its own.
const agentTokenPrefixLen = 12

// approvalCodeAlphabet is Crockford base32: no I, L, O, or U, so a code cannot
// be misread between the agent's output and the operator's screen. 32 divides
// 256, which is what keeps the modulo below unbiased.
const approvalCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	AgentSessionNotFoundErr       = apigen.NewApiErr("Session not found", "agent_session_not_found", http.StatusNotFound)
	AgentSessionUserNotFoundErr   = apigen.NewApiErr("No such user", "agent_session_user_not_found", http.StatusNotFound)
	AgentSessionRequestPendingErr = apigen.NewApiErr("A session request is already awaiting approval", "agent_session_request_pending", http.StatusConflict)
	AgentSessionNotPendingErr     = apigen.NewApiErr("Session is not awaiting approval", "agent_session_not_pending", http.StatusConflict)
)

// agentSessionScopes narrows a session's scopes to what an agent token may
// carry. Secret values are the one thing withheld: these tokens live for hours
// in shell history, environment files, and CI logs, which is a much wider blast
// radius than the browser session they were minted from.
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

func generateApprovalCode() (string, error) {
	buf := make([]byte, 7)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating approval code: %w", err)
	}
	out := make([]byte, 0, len(buf)+1)
	for i, b := range buf {
		if i == 3 {
			out = append(out, '-')
		}
		out = append(out, approvalCodeAlphabet[int(b)%len(approvalCodeAlphabet)])
	}
	return string(out), nil
}

func agentSessionToProto(rec sqlite.AgentSessionRecord) *apigen.AgentSession {
	return &apigen.AgentSession{
		ID:                rec.ID,
		CreatedAt:         rec.CreatedAt,
		ExpiresAt:         rec.ExpiresAt,
		TokenPrefix:       rec.TokenPrefix,
		Scopes:            append([]string(nil), rec.Scopes...),
		Status:            rec.Status,
		RequestingAddress: rec.RequestingAddress,
		ApprovalCode:      rec.ApprovalCode,
		ApprovedAt:        rec.ApprovedAt,
	}
}

// signAgentToken mints the bearer token for a session. Signed here rather than
// through GenerateTokenWith so the session id can ride along as jti; VerifyAuth
// uses it to find the row. The sub encoding matches what authu does for a
// non-string subject (json.Marshal of the int32 user id).
func (h *Handler) signAgentToken(userID int32, sessionID string, scopes []string, now, expiry time.Time) (string, error) {
	return h.jwtAuth.Sign(jwt.MapClaims{
		"sub":    strconv.FormatInt(int64(userID), 10),
		"scopes": scopes,
		"exp":    expiry.Unix(),
		"iat":    now.Unix(),
		"jti":    sessionID,
	})
}

// PostV1AgentSessionsCreate starts an agent session and returns its bearer
// token immediately, for non-interactive callers with no agent waiting on an
// approval. The token carries the caller's own scopes minus the withheld ones,
// so it can never grant more than the session that requested it.
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
	sessionID, err := authu.GenerateRandomToken(32)
	if err != nil {
		return nil, fmt.Errorf("generating agent session id: %w", err)
	}
	now := time.Now()
	expiry := now.Add(agentSessionTTL)
	token, err := h.signAgentToken(user.ID, sessionID, scopes, now, expiry)
	if err != nil {
		return nil, fmt.Errorf("generating agent session token: %w", err)
	}
	rec := sqlite.AgentSessionRecord{
		ID:                sessionID,
		UserID:            user.ID,
		CreatedAt:         now,
		ExpiresAt:         expiry,
		TokenHash:         hashAgentToken(token),
		TokenPrefix:       agentTokenPrefix(token),
		Scopes:            scopes,
		Status:            apigen.AgentSessionStatus_AGENT_SESSION_APPROVED,
		RequestingAddress: clientaddr.From(ctx),
		ApprovedAt:        now,
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

// PostV1AgentSessionsRequestStart opens a session request for an operator to
// approve. It is unauthenticated by necessity — the caller has no credential
// yet — and so grants nothing on its own: the row it creates carries no token
// and no scopes until a real user approves it.
//
// Only one request may be open per user at a time, so an operator is never
// asked to choose between two identical-looking requests. Stale ones are closed
// on the way through rather than by a sweeper.
func (h *Handler) PostV1AgentSessionsRequestStart(ctx apigen.Context, req *apigen.AgentSessionRequestStartRequest) (*apigen.AgentSessionRequest, error) {
	user, err := h.Store.FetchUserMatching(func(u *apigen.InternalUser) bool { return u.ID == req.UserID })
	if errors.Is(err, sqlite.ErrNotFound) {
		return nil, AgentSessionUserNotFoundErr
	}
	if err != nil {
		return nil, fmt.Errorf("resolving user for agent session request: %w", err)
	}
	now := time.Now()
	pending, err := h.Store.ListPendingAgentSessionsForUser(user.ID)
	if err != nil {
		return nil, fmt.Errorf("listing pending agent sessions: %w", err)
	}
	for _, rec := range pending {
		if now.Sub(rec.CreatedAt) < agentSessionPendingTTL {
			return nil, AgentSessionRequestPendingErr
		}
		if err := h.Store.SetAgentSessionStatus(rec.ID, apigen.AgentSessionStatus_AGENT_SESSION_REJECTED, time.Time{}, now); err != nil {
			return nil, fmt.Errorf("closing stale agent session request: %w", err)
		}
		slog.InfoContext(ctx, "closed stale agent session request", "session", rec.ID)
	}
	// The id is the pickup secret: whoever holds it collects the token once the
	// request is approved, so it is full-length random and never displayed.
	sessionID, err := authu.GenerateRandomToken(32)
	if err != nil {
		return nil, fmt.Errorf("generating agent session id: %w", err)
	}
	code, err := generateApprovalCode()
	if err != nil {
		return nil, err
	}
	rec := sqlite.AgentSessionRecord{
		ID:                sessionID,
		UserID:            user.ID,
		CreatedAt:         now,
		Status:            apigen.AgentSessionStatus_AGENT_SESSION_PENDING,
		RequestingAddress: clientaddr.From(ctx),
		ApprovalCode:      code,
	}
	if err := h.Store.InsertAgentSession(rec); err != nil {
		return nil, fmt.Errorf("storing agent session request: %w", err)
	}
	slog.InfoContext(ctx, "agent session requested", "session", sessionID, "user", user.ID, "address", rec.RequestingAddress)
	return &apigen.AgentSessionRequest{
		ID:               sessionID,
		ApprovalCode:     code,
		Status:           rec.Status,
		RequestExpiresAt: now.Add(agentSessionPendingTTL),
	}, nil
}

// PostV1AgentSessionsGetSession polls a request and, on the first call after
// approval, mints and returns the token. Minting here rather than at approval
// is deliberate: the plaintext token never has to sit in the database waiting
// to be collected, so a backup snapshot taken at any moment carries no usable
// credential. It also starts the 6-hour clock when the agent actually picks
// the token up.
func (h *Handler) PostV1AgentSessionsGetSession(ctx apigen.Context, req *apigen.AgentSessionGetRequest) (*apigen.AgentSessionPickup, error) {
	if strings.TrimSpace(req.ID) == "" {
		return nil, AgentSessionNotFoundErr
	}
	rec, err := h.Store.FetchAgentSession(req.ID)
	if errors.Is(err, sqlite.ErrNotFound) {
		return nil, AgentSessionNotFoundErr
	}
	if err != nil {
		return nil, fmt.Errorf("fetching agent session: %w", err)
	}
	now := time.Now()
	switch rec.Status {
	case apigen.AgentSessionStatus_AGENT_SESSION_PENDING:
		if now.Sub(rec.CreatedAt) >= agentSessionPendingTTL {
			return h.closeAgentSession(ctx, rec, now, "agent session request expired unapproved")
		}
		return &apigen.AgentSessionPickup{Status: rec.Status}, nil
	case apigen.AgentSessionStatus_AGENT_SESSION_APPROVED:
		if rec.Collected() {
			// The token was handed over on an earlier call and is not
			// recoverable. Reporting the status alone is the whole point.
			return &apigen.AgentSessionPickup{Status: rec.Status, ExpiresAt: rec.ExpiresAt}, nil
		}
		if now.Sub(rec.ApprovedAt) >= agentSessionPickupTTL {
			return h.closeAgentSession(ctx, rec, now, "approved agent session expired uncollected")
		}
		return h.mintApprovedAgentSession(ctx, rec, now)
	default:
		return &apigen.AgentSessionPickup{Status: rec.Status}, nil
	}
}

func (h *Handler) closeAgentSession(ctx apigen.Context, rec sqlite.AgentSessionRecord, now time.Time, msg string) (*apigen.AgentSessionPickup, error) {
	if err := h.Store.SetAgentSessionStatus(rec.ID, apigen.AgentSessionStatus_AGENT_SESSION_REJECTED, rec.ApprovedAt, now); err != nil {
		return nil, fmt.Errorf("closing agent session: %w", err)
	}
	slog.InfoContext(ctx, msg, "session", rec.ID)
	return &apigen.AgentSessionPickup{Status: apigen.AgentSessionStatus_AGENT_SESSION_REJECTED}, nil
}

func (h *Handler) mintApprovedAgentSession(ctx apigen.Context, rec sqlite.AgentSessionRecord, now time.Time) (*apigen.AgentSessionPickup, error) {
	expiry := now.Add(agentSessionTTL)
	token, err := h.signAgentToken(rec.UserID, rec.ID, rec.Scopes, now, expiry)
	if err != nil {
		return nil, fmt.Errorf("generating agent session token: %w", err)
	}
	claimed, err := h.Store.ClaimAgentSessionToken(rec.ID, hashAgentToken(token), agentTokenPrefix(token), expiry, rec.Scopes)
	if err != nil {
		return nil, fmt.Errorf("claiming agent session token: %w", err)
	}
	if !claimed {
		// Another pickup won the race and its token is the one that works.
		// Discarding this one is what keeps a session to a single credential.
		slog.WarnContext(ctx, "discarded agent session token lost to a concurrent pickup", "session", rec.ID)
		return &apigen.AgentSessionPickup{Status: apigen.AgentSessionStatus_AGENT_SESSION_APPROVED}, nil
	}
	slog.InfoContext(ctx, "agent session collected", "session", rec.ID, "ttl", agentSessionTTL.String(), "scopes", rec.Scopes)
	return &apigen.AgentSessionPickup{
		Status:    apigen.AgentSessionStatus_AGENT_SESSION_APPROVED,
		Token:     token,
		ExpiresAt: expiry,
	}, nil
}

// PostV1AgentSessionsApprove turns one of the caller's own pending requests
// into an approved session. The scopes recorded here are the approver's own,
// narrowed by agentSessionScopes, so approving can never grant more than the
// approver holds.
func (h *Handler) PostV1AgentSessionsApprove(ctx apigen.Context, req *apigen.AgentSessionApproveRequest) (*apigen.AgentSession, error) {
	if ctx.User == nil {
		return nil, InvalidAuthTokenErr
	}
	claims, _, err := h.jwtAuth.VerifyAndResolveUser(ctx.Token)
	if err != nil {
		return nil, InvalidAuthTokenErr
	}
	scopes := agentSessionScopes(jwtu.ScopesFromClaims(claims))
	if len(scopes) == 0 {
		return nil, InvalidAuthTokenErr
	}
	rec, err := h.fetchOwnAgentSession(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if rec.Status != apigen.AgentSessionStatus_AGENT_SESSION_PENDING {
		return nil, AgentSessionNotPendingErr
	}
	now := time.Now()
	if now.Sub(rec.CreatedAt) >= agentSessionPendingTTL {
		return nil, AgentSessionNotPendingErr
	}
	// Scopes are frozen here, so the token minted at pickup carries what the
	// approver held at the moment they approved rather than whatever their
	// session looks like by the time the agent collects.
	approved, err := h.Store.ApproveAgentSession(rec.ID, ctx.User.ID, scopes, now)
	if err != nil {
		return nil, fmt.Errorf("approving agent session: %w", err)
	}
	if !approved {
		return nil, AgentSessionNotPendingErr
	}
	updated, err := h.Store.FetchAgentSession(rec.ID)
	if err != nil {
		return nil, fmt.Errorf("fetching approved agent session: %w", err)
	}
	slog.InfoContext(ctx, "approved agent session", "session", rec.ID, "address", rec.RequestingAddress, "scopes", scopes)
	return agentSessionToProto(updated), nil
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

// PostV1AgentSessionsRevoke stops a session immediately: a pending request is
// rejected, anything else is revoked. VerifyAuth turns the token away on its
// next request, so this is real revocation rather than a display change.
func (h *Handler) PostV1AgentSessionsRevoke(ctx apigen.Context, req *apigen.AgentSessionRevokeRequest) error {
	if ctx.User == nil {
		return InvalidAuthTokenErr
	}
	rec, err := h.fetchOwnAgentSession(ctx, req.ID)
	if err != nil {
		return err
	}
	status := apigen.AgentSessionStatus_AGENT_SESSION_REVOKED
	if rec.Status == apigen.AgentSessionStatus_AGENT_SESSION_PENDING {
		status = apigen.AgentSessionStatus_AGENT_SESSION_REJECTED
	}
	if err := h.Store.RevokeAgentSession(req.ID, ctx.User.ID, status, time.Now()); err != nil {
		return fmt.Errorf("revoking agent session: %w", err)
	}
	slog.InfoContext(ctx, "stopped agent session", "session", req.ID, "status", status)
	return nil
}

// fetchOwnAgentSession resolves a session the caller owns. Someone else's id
// and a made-up one are both reported as not found, so a guessed id reveals
// nothing about whether it exists.
func (h *Handler) fetchOwnAgentSession(ctx apigen.Context, id string) (sqlite.AgentSessionRecord, error) {
	if strings.TrimSpace(id) == "" {
		return sqlite.AgentSessionRecord{}, AgentSessionNotFoundErr
	}
	rec, err := h.Store.FetchAgentSession(id)
	if errors.Is(err, sqlite.ErrNotFound) || (err == nil && rec.UserID != ctx.User.ID) {
		return sqlite.AgentSessionRecord{}, AgentSessionNotFoundErr
	}
	if err != nil {
		return sqlite.AgentSessionRecord{}, fmt.Errorf("fetching agent session: %w", err)
	}
	return rec, nil
}
