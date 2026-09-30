package users

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

type UserSession struct {
	ID                string
	UserID            int32
	CreatedAt         time.Time
	ExpiresAt         time.Time
	TokenHash         []byte
	RevokedAt         time.Time
	Kind              apigen.UserSessionKind
	RequestingAddress string
	UserAgent         string
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func timeOrZero(unix int64) time.Time {
	if unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

func userSessionFromRow(row pq.UserSession) UserSession {
	return UserSession{
		ID: row.SessionID, UserID: int32(row.UserID), CreatedAt: time.Unix(row.CreatedAt, 0), ExpiresAt: timeOrZero(row.ExpiresAt),
		TokenHash: row.TokenHash, RevokedAt: timeOrZero(row.RevokedAt), Kind: apigen.UserSessionKind(row.Kind),
		RequestingAddress: row.RequestingAddress, UserAgent: row.UserAgent,
	}
}

func sessionUpdate(ctx context.Context, q *pq.Queries, id string) (*state.WriteUpdate, error) {
	row, err := q.GetUserSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return pq.NewUpdate(pq.UserSessionMutation(row)), nil
}

func InsertUserSession(store *state.Service, rec UserSession) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if err := q.InsertUserSessionEvent(ctx, pq.UserSessionEventParams{
			EventMeta: pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), Author: int64(rec.UserID), EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE},
			SessionID: rec.ID, UserID: int64(rec.UserID), CreatedAt: rec.CreatedAt.Unix(), ExpiresAt: unixOrZero(rec.ExpiresAt), TokenHash: rec.TokenHash,
			Kind: int64(rec.Kind), RequestingAddress: rec.RequestingAddress, UserAgent: rec.UserAgent,
		}); err != nil {
			return nil, err
		}
		return sessionUpdate(ctx, q, rec.ID)
	})
}

func UserSessionByID(q *pq.Queries, id string) (UserSession, error) {
	row, err := q.GetUserSession(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return UserSession{}, ErrNotFound
	}
	if err != nil {
		return UserSession{}, err
	}
	return userSessionFromRow(row), nil
}

func ListUserSessions(q *pq.Queries, userID int32) ([]UserSession, error) {
	rows, err := q.ListUserSessionsForUser(context.Background(), int64(userID))
	if err != nil {
		return nil, err
	}
	out := make([]UserSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, userSessionFromRow(row))
	}
	return out, nil
}

func RevokeUserSession(store *state.Service, id string, userID int32, at time.Time) (bool, error) {
	ctx := context.Background()
	var revoked bool
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		row, err := q.GetUserSession(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if row.UserID != int64(userID) || row.RevokedAt != 0 {
			return nil, nil
		}
		event := row.Event(pq.EventMeta{GlobalSeq: seq, EventTime: at.UnixMilli(), Author: int64(userID), EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE})
		event.RevokedAt = at.Unix()
		if err := q.InsertUserSessionEvent(ctx, event); err != nil {
			return nil, err
		}
		revoked = true
		return sessionUpdate(ctx, q, id)
	})
	return revoked, err
}
