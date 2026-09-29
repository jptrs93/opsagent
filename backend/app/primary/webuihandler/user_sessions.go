package webuihandler

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/users"
	"github.com/jptrs93/opsagent/backend/lib/middleware/clientaddr"
)

var UserSessionNotFoundErr = apigen.NewApiErr("Session not found", "user_session_not_found", http.StatusNotFound)

func (h *Handler) startUserSession(ctx apigen.Context, user *apigen.InternalUser, kind apigen.UserSessionKind, ttl time.Duration) (*apigen.LoginResponse, error) {
	sessionID, err := authu.GenerateRandomToken(32)
	if err != nil {
		return nil, fmt.Errorf("generating user session id: %w", err)
	}
	token, err := mintToken(userTokenKind, sessionID)
	if err != nil {
		return nil, fmt.Errorf("generating user session token: %w", err)
	}
	now := time.Now()
	rec := users.UserSession{
		ID:                sessionID,
		UserID:            user.ID,
		CreatedAt:         now,
		ExpiresAt:         now.Add(ttl),
		TokenHash:         hashToken(token),
		Kind:              kind,
		RequestingAddress: clientaddr.From(ctx),
		UserAgent:         clientaddr.UserAgentFrom(ctx),
	}
	if err := users.InsertUserSession(h.Store, rec); err != nil {
		return nil, fmt.Errorf("storing user session: %w", err)
	}
	slog.InfoContext(ctx, fmt.Sprintf("started user session address=%s kind=%s", rec.RequestingAddress, kind), "session", sessionID, "user", user.ID)
	return newLoginResponse(user, token, kind, rec.ExpiresAt, sessionID), nil
}

func (h *Handler) startDefaultUserSession(ctx apigen.Context, user *apigen.InternalUser) (*apigen.LoginResponse, error) {
	return h.startUserSession(ctx, user, apigen.UserSessionKind_USER_SESSION_KIND_FULL, defaultSessionTokenTTL)
}

// resolveUserSession turns a u_ token into its user and session, rejecting a
// missing, revoked, expired, or mismatched one.
func (h *Handler) resolveUserSession(sessionID, token string, now time.Time) (*apigen.InternalUser, users.UserSession, error) {
	rec, err := users.UserSessionByID(h.Store.Queries(), sessionID)
	if errors.Is(err, users.ErrNotFound) {
		return nil, rec, InvalidAuthTokenErr
	}
	if err != nil {
		return nil, rec, fmt.Errorf("fetching user session: %w", err)
	}
	if !rec.RevokedAt.IsZero() || !now.Before(rec.ExpiresAt) {
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

func userSessionToProto(rec users.UserSession) *apigen.UserSession {
	return &apigen.UserSession{
		ID:                rec.ID,
		UserID:            rec.UserID,
		CreatedAt:         rec.CreatedAt,
		ExpiresAt:         rec.ExpiresAt,
		RevokedAt:         rec.RevokedAt,
		RequestingAddress: rec.RequestingAddress,
		UserAgent:         rec.UserAgent,
		Kind:              rec.Kind,
	}
}

func (h *Handler) PostV1UserSessionsList(ctx apigen.Context) (*apigen.UserSessionList, error) {
	if err := requireHuman(ctx); err != nil {
		return nil, err
	}
	records, err := users.ListUserSessions(h.Store.Queries(), ctx.User.ID)
	if err != nil {
		return nil, fmt.Errorf("listing user sessions: %w", err)
	}
	items := make([]*apigen.UserSession, 0, len(records))
	for _, rec := range records {
		items = append(items, userSessionToProto(rec))
	}
	return &apigen.UserSessionList{Items: items}, nil
}

func (h *Handler) PostV1UserSessionsRevoke(ctx apigen.Context, req *apigen.UserSessionRevokeRequest) error {
	if err := requireHuman(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(req.ID) == "" {
		return UserSessionNotFoundErr
	}
	revoked, err := users.RevokeUserSession(h.Store, req.ID, ctx.User.ID, time.Now())
	if err != nil {
		return fmt.Errorf("revoking user session: %w", err)
	}
	if !revoked {
		rec, fetchErr := users.UserSessionByID(h.Store.Queries(), req.ID)
		if fetchErr != nil || rec.UserID != ctx.User.ID {
			return UserSessionNotFoundErr
		}
		return nil
	}
	slog.InfoContext(ctx, "revoked user session", "session", req.ID, "user", ctx.User.ID)
	return nil
}
