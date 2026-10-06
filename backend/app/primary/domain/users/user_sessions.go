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
	UserID            uint64
	CreatedAt         time.Time
	ExpiresAt         time.Time
	TokenHash         []byte
	RevokedAt         time.Time
	Kind              apigen.UserSessionKind
	RequestingAddress string
	UserAgent         string
}

func timeOrZero(unix int64) time.Time {
	if unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

func userSessionFromRow(row pq.UserSession) UserSession {
	return UserSession{
		ID: row.SessionID, UserID: row.UserID, CreatedAt: time.UnixMilli(row.CreatedAt), ExpiresAt: timeOrZero(row.ExpiresAt),
		TokenHash: row.TokenHash, RevokedAt: timeOrZero(row.RevokedAt), Kind: apigen.UserSessionKind(row.Kind),
		RequestingAddress: row.RequestingAddress, UserAgent: row.UserAgent,
	}
}

func InsertUserSession(store *state.Service, rec UserSession) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_USER_SESSION)
		if err != nil {
			return nil, err
		}
		doc := &apigen.UserSession{SessionID: rec.ID, UserID: rec.UserID, ExpiresAt: rec.ExpiresAt, TokenHash: apigen.Some(rec.TokenHash),
			Kind: rec.Kind, RequestingAddress: rec.RequestingAddress, UserAgent: rec.UserAgent}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), Author: int64(rec.UserID), EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}
		return pq.NewUpdate(pq.UserSessionMutation(meta, id, doc)), nil
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

func ListUserSessions(q *pq.Queries, userID uint64) ([]UserSession, error) {
	rows, err := q.ListUserSessionsForUser(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]UserSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, userSessionFromRow(row))
	}
	return out, nil
}

func RevokeUserSession(store *state.Service, id string, userID uint64, at time.Time) (bool, error) {
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
		if row.UserID != userID || row.RevokedAt != 0 {
			return nil, nil
		}
		row.RevokedAt = at.Unix()
		revoked = true
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: at.UnixMilli(), Author: int64(userID), EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}
		return pq.NewUpdate(pq.UserSessionMutation(meta, row.ID, row.Entity())), nil
	})
	return revoked, err
}
