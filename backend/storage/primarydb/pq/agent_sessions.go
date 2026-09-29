package pq

import (
	"context"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// AgentSession is one event row; the newest row per SessionID is the live
// session. Sessions are never deleted.
type AgentSession struct {
	EventMeta
	ID                int64
	SessionID         string
	UserID            int64
	CreatedAt         int64
	ExpiresAt         int64
	TokenHash         []byte
	TokenPrefix       string
	RevokedAt         int64
	Status            int64
	RequestingAddress string
	ApprovalCode      string
	ApprovedAt        int64
}

func unixTime(unix int64) time.Time {
	if unix == 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

// Proto is the wire document: everything but the token hash.
func (r AgentSession) Proto() *apigen.AgentSession {
	return &apigen.AgentSession{
		ID: r.SessionID, UserID: int32(r.UserID), CreatedAt: time.Unix(r.CreatedAt, 0), ExpiresAt: unixTime(r.ExpiresAt),
		TokenPrefix: r.TokenPrefix, Status: apigen.AgentSessionStatus(r.Status),
		RequestingAddress: r.RequestingAddress, ApprovalCode: r.ApprovalCode, ApprovedAt: unixTime(r.ApprovedAt),
	}
}

// Event carries this row's document forward into a new event row.
func (r AgentSession) Event(meta EventMeta) AgentSessionEventParams {
	return AgentSessionEventParams{
		EventMeta: meta, SessionID: r.SessionID, UserID: r.UserID, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
		TokenHash: r.TokenHash, TokenPrefix: r.TokenPrefix, RevokedAt: r.RevokedAt, Status: r.Status,
		RequestingAddress: r.RequestingAddress, ApprovalCode: r.ApprovalCode, ApprovedAt: r.ApprovedAt,
	}
}

const agentSessionColumns = `id, global_seq, event_time, author, session_id, event_type, user_id, created_at, expires_at, token_hash, token_prefix, revoked_at,
       status, requesting_address, approval_code, approved_at`

func scanAgentSession(row scanner) (AgentSession, error) {
	var i AgentSession
	err := row.Scan(&i.ID, &i.GlobalSeq, &i.EventTime, &i.Author, &i.SessionID, &i.EventType, &i.UserID, &i.CreatedAt, &i.ExpiresAt, &i.TokenHash, &i.TokenPrefix, &i.RevokedAt,
		&i.Status, &i.RequestingAddress, &i.ApprovalCode, &i.ApprovedAt)
	return i, err
}

const liveAgentSessions = `FROM agent_session_event_log WHERE id IN (SELECT MAX(id) FROM agent_session_event_log GROUP BY session_id)`

func (q *Queries) listAgentSessions(ctx context.Context, where string, args ...any) ([]AgentSession, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+agentSessionColumns+` `+where, args...)
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
	return scanAgentSession(q.db.QueryRowContext(ctx, `SELECT `+agentSessionColumns+` FROM agent_session_event_log WHERE session_id = ? ORDER BY id DESC LIMIT 1`, sessionID))
}

func (q *Queries) ListAgentSessionsForUser(ctx context.Context, userID int64) ([]AgentSession, error) {
	return q.listAgentSessions(ctx, liveAgentSessions+` AND user_id = ? ORDER BY created_at DESC, id DESC`, userID)
}

func (q *Queries) ListPendingAgentSessionsForUser(ctx context.Context, userID int64) ([]AgentSession, error) {
	return q.listAgentSessions(ctx, liveAgentSessions+` AND user_id = ? AND status = 1 ORDER BY created_at DESC, id DESC`, userID)
}

func (q *Queries) ListAllAgentSessions(ctx context.Context) ([]AgentSession, error) {
	return q.listAgentSessions(ctx, liveAgentSessions+` ORDER BY created_at, session_id`)
}

func (q *Queries) ListAgentSessionsAtSeq(ctx context.Context, seq int64) ([]AgentSession, error) {
	return q.listAgentSessions(ctx, `FROM agent_session_event_log WHERE global_seq = ? ORDER BY id`, seq)
}

type AgentSessionEventParams struct {
	EventMeta
	SessionID         string
	UserID            int64
	CreatedAt         int64
	ExpiresAt         int64
	TokenHash         []byte
	TokenPrefix       string
	RevokedAt         int64
	Status            int64
	RequestingAddress string
	ApprovalCode      string
	ApprovedAt        int64
}

func (q *Queries) InsertAgentSessionEvent(ctx context.Context, arg AgentSessionEventParams) error {
	tokenHash := arg.TokenHash
	if tokenHash == nil {
		tokenHash = []byte{}
	}
	_, err := q.db.ExecContext(ctx, `INSERT INTO agent_session_event_log (global_seq, event_time, author, session_id, event_type, user_id, created_at, expires_at, token_hash, token_prefix, revoked_at,
                            status, requesting_address, approval_code, approved_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		arg.GlobalSeq, arg.EventTime, arg.Author, arg.SessionID, arg.EventType, arg.UserID, arg.CreatedAt, arg.ExpiresAt, tokenHash, arg.TokenPrefix, arg.RevokedAt,
		arg.Status, arg.RequestingAddress, arg.ApprovalCode, arg.ApprovedAt)
	return err
}
