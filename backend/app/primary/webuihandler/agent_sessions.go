package webuihandler

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/agentsessions"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/users"
	"github.com/jptrs93/opsagent/backend/lib/middleware/clientaddr"
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

// PostV1AgentSessionsCreate starts an agent session and returns its bearer
// token immediately, for non-interactive callers with no agent waiting on an
// approval. What the token may do is bounded by the authz layer, which treats
// every a_ token as delegated.
//
// Only the token's SHA-256 is stored. The plaintext is returned here and never
// again, so a copy of primary.db — including an off-box backup — carries no
// usable credential.
func (h *Handler) PostV1AgentSessionsCreate(ctx apigen.Context) (*apigen.AgentSessionCreated, error) {
	if err := requireHuman(ctx); err != nil {
		return nil, err
	}
	sessionID, err := authu.GenerateRandomToken(32)
	if err != nil {
		return nil, fmt.Errorf("generating agent session id: %w", err)
	}
	now := time.Now()
	expiry := now.Add(agentSessionTTL)
	token, err := mintToken(agentTokenKind, sessionID)
	if err != nil {
		return nil, fmt.Errorf("generating agent session token: %w", err)
	}
	user := ctx.User
	rec := agentsessions.Record{
		ID:                sessionID,
		UserID:            user.ID,
		CreatedAt:         now,
		ExpiresAt:         expiry,
		TokenHash:         hashToken(token),
		TokenPrefix:       tokenDisplayPrefix(token),
		Status:            apigen.AgentSessionStatus_AGENT_SESSION_STATUS_APPROVED,
		RequestingAddress: clientaddr.From(ctx),
		ApprovedAt:        now,
	}
	if err := h.agentSessions().InsertAgentSession(rec, ctx.AttributionUserID()); err != nil {
		return nil, fmt.Errorf("storing agent session: %w", err)
	}
	slog.InfoContext(ctx, fmt.Sprintf("started agent session ttl=%s", agentSessionTTL), "session", sessionID)
	return &apigen.AgentSessionCreated{
		Token:   token,
		Session: *agentsessions.ToProto(rec),
	}, nil
}

// PostV1AgentSessionsRequestStart opens a session request for an operator to
// approve. It is unauthenticated by necessity — the caller has no credential
// yet — and so grants nothing on its own: the row it creates carries no token
// until a real user approves it and the agent collects.
//
// Only one request may be open per user at a time, so an operator is never
// asked to choose between two identical-looking requests. Stale ones are closed
// on the way through rather than by a sweeper.
func (h *Handler) PostV1AgentSessionsRequestStart(ctx apigen.Context, req *apigen.AgentSessionRequestStartRequest) (*apigen.AgentSessionRequest, error) {
	user, err := users.Matching(h.Store.Queries(), func(u *apigen.User) bool { return u.ID == req.UserID })
	if errors.Is(err, users.ErrNotFound) {
		return nil, AgentSessionUserNotFoundErr
	}
	if err != nil {
		return nil, fmt.Errorf("resolving user for agent session request: %w", err)
	}
	now := time.Now()
	pending, err := h.agentSessions().ListPendingAgentSessionsForUser(user.ID)
	if err != nil {
		return nil, fmt.Errorf("listing pending agent sessions: %w", err)
	}
	for _, rec := range pending {
		if now.Sub(rec.CreatedAt) < agentSessionPendingTTL {
			return nil, AgentSessionRequestPendingErr
		}
		if err := h.agentSessions().SetAgentSessionStatus(rec.ID, apigen.AgentSessionStatus_AGENT_SESSION_STATUS_REJECTED, time.Time{}, 0); err != nil {
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
	rec := agentsessions.Record{
		ID:                sessionID,
		UserID:            user.ID,
		CreatedAt:         now,
		Status:            apigen.AgentSessionStatus_AGENT_SESSION_STATUS_PENDING,
		RequestingAddress: clientaddr.From(ctx),
		ApprovalCode:      code,
	}
	if err := h.agentSessions().InsertAgentSession(rec, 0); err != nil {
		return nil, fmt.Errorf("storing agent session request: %w", err)
	}
	slog.InfoContext(ctx, fmt.Sprintf("agent session requested address=%s", rec.RequestingAddress), "session", sessionID, "user", user.ID)
	return &apigen.AgentSessionRequest{
		SessionID:        sessionID,
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
	if strings.TrimSpace(req.SessionID) == "" {
		return nil, AgentSessionNotFoundErr
	}
	rec, err := h.agentSessions().FetchAgentSession(req.SessionID)
	if errors.Is(err, agentsessions.ErrNotFound) {
		return nil, AgentSessionNotFoundErr
	}
	if err != nil {
		return nil, fmt.Errorf("fetching agent session: %w", err)
	}
	now := time.Now()
	switch rec.Status {
	case apigen.AgentSessionStatus_AGENT_SESSION_STATUS_PENDING:
		if now.Sub(rec.CreatedAt) >= agentSessionPendingTTL {
			return h.closeAgentSession(ctx, rec, now, "agent session request expired unapproved")
		}
		return &apigen.AgentSessionPickup{Status: rec.Status}, nil
	case apigen.AgentSessionStatus_AGENT_SESSION_STATUS_APPROVED:
		if rec.Collected() {
			// The token was handed over on an earlier call and is not
			// recoverable. Reporting the status alone is the whole point.
			return &apigen.AgentSessionPickup{Status: rec.Status, ExpiresAt: apigen.TimeOf(rec.ExpiresAt)}, nil
		}
		if now.Sub(rec.ApprovedAt) >= agentSessionPickupTTL {
			return h.closeAgentSession(ctx, rec, now, "approved agent session expired uncollected")
		}
		return h.mintApprovedAgentSession(ctx, rec, now)
	default:
		return &apigen.AgentSessionPickup{Status: rec.Status}, nil
	}
}

func (h *Handler) closeAgentSession(ctx apigen.Context, rec agentsessions.Record, now time.Time, msg string) (*apigen.AgentSessionPickup, error) {
	if err := h.agentSessions().SetAgentSessionStatus(rec.ID, apigen.AgentSessionStatus_AGENT_SESSION_STATUS_REJECTED, rec.ApprovedAt, 0); err != nil {
		return nil, fmt.Errorf("closing agent session: %w", err)
	}
	slog.InfoContext(ctx, msg, "session", rec.ID)
	return &apigen.AgentSessionPickup{Status: apigen.AgentSessionStatus_AGENT_SESSION_STATUS_REJECTED}, nil
}

func (h *Handler) mintApprovedAgentSession(ctx apigen.Context, rec agentsessions.Record, now time.Time) (*apigen.AgentSessionPickup, error) {
	expiry := now.Add(agentSessionTTL)
	token, err := mintToken(agentTokenKind, rec.ID)
	if err != nil {
		return nil, fmt.Errorf("generating agent session token: %w", err)
	}
	claimed, err := h.agentSessions().ClaimAgentSessionToken(rec.ID, hashToken(token), tokenDisplayPrefix(token), expiry)
	if err != nil {
		return nil, fmt.Errorf("claiming agent session token: %w", err)
	}
	if !claimed {
		// Another pickup won the race and its token is the one that works.
		// Discarding this one is what keeps a session to a single credential.
		slog.WarnContext(ctx, "discarded agent session token lost to a concurrent pickup", "session", rec.ID)
		return &apigen.AgentSessionPickup{Status: apigen.AgentSessionStatus_AGENT_SESSION_STATUS_APPROVED}, nil
	}
	slog.InfoContext(ctx, fmt.Sprintf("agent session collected ttl=%s", agentSessionTTL), "session", rec.ID)
	return &apigen.AgentSessionPickup{
		Status:    apigen.AgentSessionStatus_AGENT_SESSION_STATUS_APPROVED,
		Token:     token,
		ExpiresAt: apigen.TimeOf(expiry),
	}, nil
}

// PostV1AgentSessionsApprove turns one of the caller's own pending requests
// into an approved session.
func (h *Handler) PostV1AgentSessionsApprove(ctx apigen.Context, req *apigen.AgentSessionApproveRequest) (*apigen.AgentSession, error) {
	if err := requireHuman(ctx); err != nil {
		return nil, err
	}
	rec, err := h.fetchOwnAgentSession(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if rec.Status != apigen.AgentSessionStatus_AGENT_SESSION_STATUS_PENDING {
		return nil, AgentSessionNotPendingErr
	}
	now := time.Now()
	if now.Sub(rec.CreatedAt) >= agentSessionPendingTTL {
		return nil, AgentSessionNotPendingErr
	}
	approved, err := h.agentSessions().ApproveAgentSession(rec.ID, ctx.User.ID, now)
	if err != nil {
		return nil, fmt.Errorf("approving agent session: %w", err)
	}
	if !approved {
		return nil, AgentSessionNotPendingErr
	}
	updated, err := h.agentSessions().FetchAgentSession(rec.ID)
	if err != nil {
		return nil, fmt.Errorf("fetching approved agent session: %w", err)
	}
	slog.InfoContext(ctx, fmt.Sprintf("approved agent session address=%s", rec.RequestingAddress), "session", rec.ID)
	return agentsessions.ToProto(updated), nil
}

// PostV1AgentSessionsList returns the caller's own sessions, newest first.
// There is no cross-user view: sessions are only ever managed by their owner.
func (h *Handler) PostV1AgentSessionsList(ctx apigen.Context) (*apigen.AgentSessionList, error) {
	if err := requireHuman(ctx); err != nil {
		return nil, err
	}
	records, err := h.agentSessions().ListAgentSessionsForUser(ctx.User.ID)
	if err != nil {
		return nil, fmt.Errorf("listing agent sessions: %w", err)
	}
	items := make([]apigen.AgentSession, 0, len(records))
	for _, rec := range records {
		items = append(items, *agentsessions.ToProto(rec))
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
	if ctx.Delegated && strings.TrimSpace(req.SessionID) != ctx.SessionID {
		return AgentSessionNotFoundErr
	}
	rec, err := h.fetchOwnAgentSession(ctx, req.SessionID)
	if err != nil {
		return err
	}
	status := apigen.AgentSessionStatus_AGENT_SESSION_STATUS_REVOKED
	if rec.Status == apigen.AgentSessionStatus_AGENT_SESSION_STATUS_PENDING {
		status = apigen.AgentSessionStatus_AGENT_SESSION_STATUS_REJECTED
	}
	if err := h.agentSessions().RevokeAgentSession(req.SessionID, ctx.User.ID, status, ctx.AttributionUserID()); err != nil {
		return fmt.Errorf("revoking agent session: %w", err)
	}
	slog.InfoContext(ctx, fmt.Sprintf("stopped agent session status=%v", status), "session", req.SessionID)
	return nil
}

// fetchOwnAgentSession resolves a session the caller owns. Someone else's id
// and a made-up one are both reported as not found, so a guessed id reveals
// nothing about whether it exists.
func (h *Handler) fetchOwnAgentSession(ctx apigen.Context, id string) (agentsessions.Record, error) {
	if strings.TrimSpace(id) == "" {
		return agentsessions.Record{}, AgentSessionNotFoundErr
	}
	rec, err := h.agentSessions().FetchAgentSession(id)
	if errors.Is(err, agentsessions.ErrNotFound) || (err == nil && rec.UserID != ctx.User.ID) {
		return agentsessions.Record{}, AgentSessionNotFoundErr
	}
	if err != nil {
		return agentsessions.Record{}, fmt.Errorf("fetching agent session: %w", err)
	}
	return rec, nil
}
