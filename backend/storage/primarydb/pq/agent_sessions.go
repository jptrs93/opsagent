package pq

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// AgentSession is one live session: the facts of the AgentSession payload
// in storage form (unix seconds, the status as its enum value), its creation
// time in epoch ms, and the envelope of the last write. ID is the stream
// entity id, SessionID the id inside the token. A pending request has no
// token: TokenHash, TokenPrefix, and ExpiresAt are empty until approval.
type AgentSession struct {
	ID                uint64
	SessionID         string
	UserID            uint64
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

// Collected reports whether the session's token was minted.
func (r AgentSession) Collected() bool { return len(r.TokenHash) > 0 }

// Proto is the wire document without the token hash.
func (r AgentSession) Proto() *apigen.AgentSession {
	s := r.Entity()
	if s.Token.Present {
		s.Token.Value.Hash = apigen.Maybe[[]byte]{}
	}
	return s
}

// Entity is the row as the event stream carries it: the document with the
// token hash.
func (r AgentSession) Entity() *apigen.AgentSession {
	s := &apigen.AgentSession{
		ID: r.ID, SessionID: r.SessionID, UserID: r.UserID, Status: apigen.AgentSessionStatus(r.Status),
		RequestingAddress: r.RequestingAddress, ApprovedAt: unixTime(r.ApprovedAt),
	}
	if r.ApprovalCode != "" {
		s.ApprovalCode = apigen.Some(r.ApprovalCode)
	}
	if r.Collected() {
		s.Token = apigen.Some(apigen.AgentToken{Hash: apigen.Some(r.TokenHash), Prefix: r.TokenPrefix, ExpiresAt: unixTime(r.ExpiresAt).Value})
	}
	return s
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

func (q *Queries) ListAgentSessionsForUser(ctx context.Context, userID uint64) ([]AgentSession, error) {
	return q.listAgentSessions(ctx, `WHERE user_id = ? ORDER BY created_at DESC, id DESC`, userID)
}

func (q *Queries) ListPendingAgentSessionsForUser(ctx context.Context, userID uint64) ([]AgentSession, error) {
	return q.listAgentSessions(ctx, `WHERE user_id = ? AND status = ? ORDER BY created_at DESC, id DESC`, userID, int64(apigen.AgentSessionStatus_AGENT_SESSION_STATUS_PENDING))
}

func (q *Queries) ListAllAgentSessions(ctx context.Context) ([]AgentSession, error) {
	return q.listAgentSessions(ctx, `ORDER BY created_at, session_id`)
}

func (q *Queries) reduceAgentSession(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, s *apigen.AgentSession) error {
	if s == nil {
		return fmt.Errorf("payload has no session")
	}
	var tokenHash []byte
	var tokenPrefix string
	var expiresAt int64
	if s.Token.Present {
		tokenHash, tokenPrefix, expiresAt = s.Token.Value.Hash.Value, s.Token.Value.Prefix, s.Token.Value.ExpiresAt.Unix()
	}
	return q.upsert(ctx, meta, `INSERT INTO agent_sessions (id, session_id, user_id, created_at, expires_at, token_hash, token_prefix, status, requesting_address, approval_code, approved_at, seq, event_time, author)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET session_id = excluded.session_id, user_id = excluded.user_id, expires_at = excluded.expires_at,
  token_hash = excluded.token_hash, token_prefix = excluded.token_prefix, status = excluded.status, requesting_address = excluded.requesting_address,
  approval_code = excluded.approval_code, approved_at = excluded.approved_at, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_at`,
		id, s.SessionID, s.UserID, env.EventTime, expiresAt, notNullBlob(tokenHash), tokenPrefix, int64(s.Status), s.RequestingAddress, s.ApprovalCode.Value, unixOrZero(s.ApprovedAt),
		env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteAgentSessionRow(ctx context.Context, id uint64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM agent_sessions WHERE id = ?`, id)
	return err
}
