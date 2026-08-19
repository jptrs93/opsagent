package webuihandler

import (
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
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/middleware/clientaddr"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

const personalSessionActivityTouchInterval = time.Minute

var PersonalSessionNotFoundErr = apigen.NewApiErr("Session not found", "personal_session_not_found", http.StatusNotFound)

func (h *Handler) signPersonalToken(userID int32, sessionID string, scopes []string, now, expiry time.Time) (string, error) {
	return h.jwtAuth.Sign(jwt.MapClaims{
		"sub":    strconv.FormatInt(int64(userID), 10),
		"scopes": scopes,
		"exp":    expiry.Unix(),
		"iat":    now.Unix(),
		"sid":    sessionID,
	})
}

func (h *Handler) startPersonalSession(ctx apigen.Context, user *apigen.InternalUser) (*apigen.LoginResponse, error) {
	sessionID, err := authu.GenerateRandomToken(32)
	if err != nil {
		return nil, fmt.Errorf("generating personal session id: %w", err)
	}
	now := time.Now()
	expiry := now.Add(defaultSessionTokenTTL)
	token, err := h.signPersonalToken(user.ID, sessionID, defaultUserScopes, now, expiry)
	if err != nil {
		return nil, fmt.Errorf("generating personal session token: %w", err)
	}
	rec := state.PersonalSessionRecord{
		ID:                sessionID,
		UserID:            user.ID,
		CreatedAt:         now,
		ExpiresAt:         expiry,
		TokenHash:         hashAgentToken(token),
		RequestingAddress: clientaddr.From(ctx),
		UserAgent:         clientaddr.UserAgentFrom(ctx),
		LastActiveAt:      now,
	}
	if err := h.Store.InsertPersonalSession(rec); err != nil {
		return nil, fmt.Errorf("storing personal session: %w", err)
	}
	h.Store.TouchUserLastLogin(user.ID)
	slog.InfoContext(ctx, "started personal session", "session", sessionID, "user", user.ID, "address", rec.RequestingAddress)
	return newLoginResponse(user, token, defaultUserScopes, expiry), nil
}

func (h *Handler) verifyPersonalSession(claims map[string]any, token string) error {
	sessionID, _ := claims["sid"].(string)
	if sessionID == "" {
		return nil
	}
	rec, err := h.Store.FetchPersonalSession(sessionID)
	if errors.Is(err, state.ErrNotFound) {
		return InvalidAuthTokenErr
	}
	if err != nil {
		return fmt.Errorf("fetching personal session: %w", err)
	}
	if !rec.RevokedAt.IsZero() {
		return InvalidAuthTokenErr
	}
	if subtle.ConstantTimeCompare(rec.TokenHash, hashAgentToken(token)) != 1 {
		return InvalidAuthTokenErr
	}
	if now := time.Now(); now.Sub(rec.LastActiveAt) >= personalSessionActivityTouchInterval {
		if err := h.Store.TouchPersonalSessionActivity(rec.ID, now); err != nil {
			slog.Warn("failed to touch personal session activity", "session", rec.ID, "err", err)
		}
	}
	return nil
}

func (h *Handler) currentPersonalSessionID(ctx apigen.Context) string {
	claims, _, err := h.jwtAuth.VerifyAndResolveUser(ctx.Token)
	if err != nil {
		return ""
	}
	sid, _ := claims["sid"].(string)
	return sid
}

func personalSessionToProto(rec state.PersonalSessionRecord, currentID string) *apigen.PersonalSession {
	return &apigen.PersonalSession{
		ID:                rec.ID,
		CreatedAt:         rec.CreatedAt,
		ExpiresAt:         rec.ExpiresAt,
		RevokedAt:         rec.RevokedAt,
		RequestingAddress: rec.RequestingAddress,
		UserAgent:         rec.UserAgent,
		LastActiveAt:      rec.LastActiveAt,
		Current:           currentID != "" && rec.ID == currentID,
	}
}

func (h *Handler) PostV1PersonalSessionsList(ctx apigen.Context) (*apigen.PersonalSessionList, error) {
	if ctx.User == nil {
		return nil, InvalidAuthTokenErr
	}
	records, err := h.Store.ListPersonalSessionsForUser(ctx.User.ID)
	if err != nil {
		return nil, fmt.Errorf("listing personal sessions: %w", err)
	}
	currentID := h.currentPersonalSessionID(ctx)
	items := make([]*apigen.PersonalSession, 0, len(records))
	for _, rec := range records {
		items = append(items, personalSessionToProto(rec, currentID))
	}
	return &apigen.PersonalSessionList{Items: items}, nil
}

func (h *Handler) PostV1PersonalSessionsRevoke(ctx apigen.Context, req *apigen.PersonalSessionRevokeRequest) error {
	if ctx.User == nil {
		return InvalidAuthTokenErr
	}
	if strings.TrimSpace(req.ID) == "" {
		return PersonalSessionNotFoundErr
	}
	revoked, err := h.Store.RevokePersonalSession(req.ID, ctx.User.ID, time.Now())
	if err != nil {
		return fmt.Errorf("revoking personal session: %w", err)
	}
	if !revoked {
		rec, fetchErr := h.Store.FetchPersonalSession(req.ID)
		if fetchErr != nil || rec.UserID != ctx.User.ID {
			return PersonalSessionNotFoundErr
		}
		return nil
	}
	slog.InfoContext(ctx, "revoked personal session", "session", req.ID, "user", ctx.User.ID)
	return nil
}
