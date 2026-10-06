package pq

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// UserSession is one live session: the facts of the UserSession payload in
// storage form (unix seconds, the kind as its enum value), its creation
// time in epoch ms, and the envelope of the last write. ID is the stream
// entity id, SessionID the id inside the token.
type UserSession struct {
	ID                uint64
	SessionID         string
	UserID            uint64
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

// Proto is the wire document without the token hash.
func (r UserSession) Proto() *apigen.UserSession {
	s := r.Entity()
	s.TokenHash = apigen.Maybe[[]byte]{}
	return s
}

// Entity is the row as the event stream carries it: the document with the
// token hash.
func (r UserSession) Entity() *apigen.UserSession {
	return &apigen.UserSession{
		ID: r.ID, SessionID: r.SessionID, UserID: r.UserID, ExpiresAt: unixTime(r.ExpiresAt).Value,
		RevokedAt: unixTime(r.RevokedAt), Kind: apigen.UserSessionKind(r.Kind), RequestingAddress: r.RequestingAddress, UserAgent: r.UserAgent,
		TokenHash: apigen.Some(r.TokenHash),
	}
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

func (q *Queries) ListUserSessionsForUser(ctx context.Context, userID uint64) ([]UserSession, error) {
	return q.listUserSessions(ctx, `WHERE user_id = ? ORDER BY created_at DESC, id DESC`, userID)
}

func (q *Queries) ListAllUserSessions(ctx context.Context) ([]UserSession, error) {
	return q.listUserSessions(ctx, `ORDER BY created_at, session_id`)
}

func (q *Queries) reduceUserSession(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, s *apigen.UserSession) error {
	if s == nil {
		return fmt.Errorf("payload has no session")
	}
	if !s.TokenHash.Present {
		return fmt.Errorf("payload has no token hash")
	}
	return q.upsert(ctx, meta, `INSERT INTO user_sessions (id, session_id, user_id, created_at, expires_at, token_hash, revoked_at, kind, requesting_address, user_agent, seq, event_time, author)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET session_id = excluded.session_id, user_id = excluded.user_id, expires_at = excluded.expires_at,
  token_hash = excluded.token_hash, revoked_at = excluded.revoked_at, kind = excluded.kind, requesting_address = excluded.requesting_address,
  user_agent = excluded.user_agent, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_at`,
		id, s.SessionID, s.UserID, env.EventTime, s.ExpiresAt.Unix(), notNullBlob(s.TokenHash.Value), unixOrZero(s.RevokedAt), int64(s.Kind), s.RequestingAddress, s.UserAgent,
		env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteUserSessionRow(ctx context.Context, id uint64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM user_sessions WHERE id = ?`, id)
	return err
}
