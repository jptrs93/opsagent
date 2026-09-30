package pq

import (
	"context"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// UserSession is one event row; the newest row per SessionID is the live
// session. Sessions are never deleted.
type UserSession struct {
	EventMeta
	ID                int64
	EntityID          int64
	SessionID         string
	UserID            int64
	CreatedAt         int64
	ExpiresAt         int64
	TokenHash         []byte
	RevokedAt         int64
	Kind              int64
	RequestingAddress string
	UserAgent         string
}

// Proto is the wire document: everything but the token hash.
func (r UserSession) Proto() *apigen.UserSession {
	return &apigen.UserSession{
		ID: r.SessionID, UserID: int32(r.UserID), CreatedAt: time.Unix(r.CreatedAt, 0), ExpiresAt: unixTime(r.ExpiresAt),
		RevokedAt: unixTime(r.RevokedAt), Kind: apigen.UserSessionKind(r.Kind), RequestingAddress: r.RequestingAddress, UserAgent: r.UserAgent,
	}
}

// Event carries this row's document forward into a new event row.
func (r UserSession) Event(meta EventMeta) UserSessionEventParams {
	return UserSessionEventParams{
		EventMeta: meta, SessionID: r.SessionID, UserID: r.UserID, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, TokenHash: r.TokenHash,
		RevokedAt: r.RevokedAt, Kind: r.Kind, RequestingAddress: r.RequestingAddress, UserAgent: r.UserAgent,
	}
}

// The last column is the session's entity id on the event stream: the id of
// its first row.
const userSessionColumns = `id, global_seq, event_time, author, session_id, event_type, user_id, created_at, expires_at, token_hash, revoked_at, kind, requesting_address, user_agent,
       (SELECT MIN(p.id) FROM user_session_event_log p WHERE p.session_id = user_session_event_log.session_id)`

func scanUserSession(row scanner) (UserSession, error) {
	var i UserSession
	err := row.Scan(&i.ID, &i.GlobalSeq, &i.EventTime, &i.Author, &i.SessionID, &i.EventType, &i.UserID, &i.CreatedAt, &i.ExpiresAt, &i.TokenHash, &i.RevokedAt, &i.Kind, &i.RequestingAddress, &i.UserAgent, &i.EntityID)
	return i, err
}

const liveUserSessions = `FROM user_session_event_log WHERE id IN (SELECT MAX(id) FROM user_session_event_log GROUP BY session_id)`

func (q *Queries) listUserSessions(ctx context.Context, where string, args ...any) ([]UserSession, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+userSessionColumns+` `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []UserSession
	for rows.Next() {
		i, err := scanUserSession(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

func (q *Queries) GetUserSession(ctx context.Context, sessionID string) (UserSession, error) {
	return scanUserSession(q.db.QueryRowContext(ctx, `SELECT `+userSessionColumns+` FROM user_session_event_log WHERE session_id = ? ORDER BY id DESC LIMIT 1`, sessionID))
}

func (q *Queries) ListUserSessionsForUser(ctx context.Context, userID int64) ([]UserSession, error) {
	return q.listUserSessions(ctx, liveUserSessions+` AND user_id = ? ORDER BY created_at DESC, id DESC`, userID)
}

func (q *Queries) ListAllUserSessions(ctx context.Context) ([]UserSession, error) {
	return q.listUserSessions(ctx, liveUserSessions+` ORDER BY created_at, session_id`)
}

type UserSessionEventParams struct {
	EventMeta
	SessionID         string
	UserID            int64
	CreatedAt         int64
	ExpiresAt         int64
	TokenHash         []byte
	RevokedAt         int64
	Kind              int64
	RequestingAddress string
	UserAgent         string
}

func (q *Queries) InsertUserSessionEvent(ctx context.Context, arg UserSessionEventParams) error {
	tokenHash := arg.TokenHash
	if tokenHash == nil {
		tokenHash = []byte{}
	}
	_, err := q.db.ExecContext(ctx, `INSERT INTO user_session_event_log (global_seq, event_time, author, session_id, event_type, user_id, created_at, expires_at, token_hash, revoked_at, kind, requesting_address, user_agent)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		arg.GlobalSeq, arg.EventTime, arg.Author, arg.SessionID, arg.EventType, arg.UserID, arg.CreatedAt, arg.ExpiresAt, tokenHash, arg.RevokedAt, arg.Kind, arg.RequestingAddress, arg.UserAgent)
	return err
}
