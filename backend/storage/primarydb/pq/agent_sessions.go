package pq

import (
	"context"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// AgentSession is one live session: the facts of the AgentSession payload
// in storage form (unix seconds, the status as its enum value), its creation
// time in epoch ms, and the envelope of the last write. ID is the stream
// entity id, SessionID the id inside the token.
type AgentSession struct {
	ID                int64
	SessionID         string
	UserID            int64
	CreatedAt         int64
	ExpiresAt         int64
	TokenHash         []byte
	TokenPrefix       string
	Status            int64
	RequestingAddress string
	ApprovalCode      string
	ApprovedAt        int64
	Seq               int64
	EventTime         int64
	Author            int64
}

func unixTime(unix int64) time.Time {
	if unix == 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// Proto is the wire document: everything but the token hash.
func (r AgentSession) Proto() *apigen.AgentSession {
	return &apigen.AgentSession{
		ID: r.SessionID, UserID: int32(r.UserID), ExpiresAt: unixTime(r.ExpiresAt),
		TokenPrefix: r.TokenPrefix, Status: apigen.AgentSessionStatus(r.Status),
		RequestingAddress: r.RequestingAddress, ApprovalCode: r.ApprovalCode, ApprovedAt: unixTime(r.ApprovedAt),
	}
}

// Entity is the row as the event stream carries it: the document with the
// token hash.
func (r AgentSession) Entity() *apigen.AgentSession {
	value := r.Proto()
	value.TokenHash = r.TokenHash
	return value
}

const agentSessionColumns = `id, session_id, user_id, created_at, expires_at, token_hash, token_prefix, status, requesting_address, approval_code, approved_at, seq, event_time, author`

func scanAgentSession(row scanner) (AgentSession, error) {
	var i AgentSession
	err := row.Scan(&i.ID, &i.SessionID, &i.UserID, &i.CreatedAt, &i.ExpiresAt, &i.TokenHash, &i.TokenPrefix, &i.Status, &i.RequestingAddress, &i.ApprovalCode, &i.ApprovedAt, &i.Seq, &i.EventTime, &i.Author)
	return i, err
}

func (q *Queries) listAgentSessions(ctx context.Context, where string, args ...any) ([]AgentSession, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+agentSessionColumns+` FROM agent_sessions `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []AgentSession
	for rows.Next() {
		i, err := scanAgentSession(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

func (q *Queries) GetAgentSession(ctx context.Context, sessionID string) (AgentSession, error) {
	return scanAgentSession(q.db.QueryRowContext(ctx, `SELECT `+agentSessionColumns+` FROM agent_sessions WHERE session_id = ?`, sessionID))
}

func (q *Queries) ListAgentSessionsForUser(ctx context.Context, userID int64) ([]AgentSession, error) {
	return q.listAgentSessions(ctx, `WHERE user_id = ? ORDER BY created_at DESC, id DESC`, userID)
}

func (q *Queries) ListPendingAgentSessionsForUser(ctx context.Context, userID int64) ([]AgentSession, error) {
	return q.listAgentSessions(ctx, `WHERE user_id = ? AND status = 1 ORDER BY created_at DESC, id DESC`, userID)
}

func (q *Queries) ListAllAgentSessions(ctx context.Context) ([]AgentSession, error) {
	return q.listAgentSessions(ctx, `ORDER BY created_at, session_id`)
}

func (q *Queries) reduceAgentSession(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id int64, s *apigen.AgentSession) error {
	if s == nil {
		return fmt.Errorf("payload has no session")
	}
	return q.upsert(ctx, meta, `INSERT INTO agent_sessions (id, session_id, user_id, created_at, expires_at, token_hash, token_prefix, status, requesting_address, approval_code, approved_at, seq, event_time, author)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET session_id = excluded.session_id, user_id = excluded.user_id, expires_at = excluded.expires_at,
  token_hash = excluded.token_hash, token_prefix = excluded.token_prefix, status = excluded.status, requesting_address = excluded.requesting_address,
  approval_code = excluded.approval_code, approved_at = excluded.approved_at, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_at`,
		id, s.ID, int64(s.UserID), env.EventTime, unixOrZero(s.ExpiresAt), notNullBlob(s.TokenHash), s.TokenPrefix, int64(s.Status), s.RequestingAddress, s.ApprovalCode, unixOrZero(s.ApprovedAt),
		env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteAgentSessionRow(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM agent_sessions WHERE id = ?`, id)
	return err
}
