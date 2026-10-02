package pq

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// UserSession is one live session: the facts of the UserSession payload in
// storage form (unix seconds, the kind as its enum value), its creation
// time in epoch ms, and the envelope of the last write. ID is the stream entity id, SessionID the id inside
// the token.
type UserSession struct {
	ID                int64
	SessionID         string
	UserID            int64
	CreatedAt         int64
	ExpiresAt         int64
	TokenHash         []byte
	RevokedAt         int64
	Kind              int64
	RequestingAddress string
	UserAgent         string
	Seq               int64
	EventTime         int64
	Author            int64
}

// Proto is the wire document: everything but the token hash.
func (r UserSession) Proto() *apigen.UserSession {
	return &apigen.UserSession{
		ID: r.SessionID, UserID: int32(r.UserID), ExpiresAt: unixTime(r.ExpiresAt),
		RevokedAt: unixTime(r.RevokedAt), Kind: apigen.UserSessionKind(r.Kind), RequestingAddress: r.RequestingAddress, UserAgent: r.UserAgent,
	}
}

// Entity is the row as the event stream carries it: the document with the
// token hash.
func (r UserSession) Entity() *apigen.UserSession {
	value := r.Proto()
	value.TokenHash = r.TokenHash
	return value
}

const userSessionColumns = `id, session_id, user_id, created_at, expires_at, token_hash, revoked_at, kind, requesting_address, user_agent, seq, event_time, author`

func scanUserSession(row scanner) (UserSession, error) {
	var i UserSession
	err := row.Scan(&i.ID, &i.SessionID, &i.UserID, &i.CreatedAt, &i.ExpiresAt, &i.TokenHash, &i.RevokedAt, &i.Kind, &i.RequestingAddress, &i.UserAgent, &i.Seq, &i.EventTime, &i.Author)
	return i, err
}

func (q *Queries) listUserSessions(ctx context.Context, where string, args ...any) ([]UserSession, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+userSessionColumns+` FROM user_sessions `+where, args...)
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
	return scanUserSession(q.db.QueryRowContext(ctx, `SELECT `+userSessionColumns+` FROM user_sessions WHERE session_id = ?`, sessionID))
}

func (q *Queries) ListUserSessionsForUser(ctx context.Context, userID int64) ([]UserSession, error) {
	return q.listUserSessions(ctx, `WHERE user_id = ? ORDER BY created_at DESC, id DESC`, userID)
}

func (q *Queries) ListAllUserSessions(ctx context.Context) ([]UserSession, error) {
	return q.listUserSessions(ctx, `ORDER BY created_at, session_id`)
}

func (q *Queries) reduceUserSession(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id int64, s *apigen.UserSession) error {
	if s == nil {
		return fmt.Errorf("payload has no session")
	}
	return q.upsert(ctx, meta, `INSERT INTO user_sessions (id, session_id, user_id, created_at, expires_at, token_hash, revoked_at, kind, requesting_address, user_agent, seq, event_time, author)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET session_id = excluded.session_id, user_id = excluded.user_id, expires_at = excluded.expires_at,
  token_hash = excluded.token_hash, revoked_at = excluded.revoked_at, kind = excluded.kind, requesting_address = excluded.requesting_address,
  user_agent = excluded.user_agent, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_at`,
		id, s.ID, int64(s.UserID), env.EventTime, unixOrZero(s.ExpiresAt), notNullBlob(s.TokenHash), unixOrZero(s.RevokedAt), int64(s.Kind), s.RequestingAddress, s.UserAgent,
		env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteUserSessionRow(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM user_sessions WHERE id = ?`, id)
	return err
}
