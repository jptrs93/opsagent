package pq

import (
	"context"
)

type AgentSession struct {
	ID                string
	UserID            int64
	CreatedAt         int64
	ExpiresAt         int64
	TokenHash         []byte
	TokenPrefix       string
	RevokedAt         int64
	Scopes            string
	Status            int64
	RequestingAddress string
	ApprovalCode      string
	ApprovedAt        int64
}

const approveAgentSession = `UPDATE agent_sessions SET status = 2, approved_at = ?, scopes = ?
WHERE id = ? AND user_id = ? AND status = 1
`

type ApproveAgentSessionParams struct {
	ApprovedAt int64
	Scopes     string
	ID         string
	UserID     int64
}

// ApproveAgentSession records the approver's scopes and the approval together,
// and only from PENDING, so two operators racing on the same request cannot
// both approve it and the second one's scopes cannot overwrite the first's.
func (q *Queries) ApproveAgentSession(ctx context.Context, arg ApproveAgentSessionParams) (int64, error) {
	result, err := q.db.ExecContext(ctx, approveAgentSession,
		arg.ApprovedAt,
		arg.Scopes,
		arg.ID,
		arg.UserID,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

const claimAgentSessionToken = `UPDATE agent_sessions SET token_hash = ?, token_prefix = ?, expires_at = ?, scopes = ?
WHERE id = ? AND status = 2 AND length(token_hash) = 0
`

type ClaimAgentSessionTokenParams struct {
	TokenHash   []byte
	TokenPrefix string
	ExpiresAt   int64
	Scopes      string
	ID          string
}

// ClaimAgentSessionToken mints a session's token exactly once. The empty
// token_hash in the WHERE clause is the guard: two concurrent pickups race here
// and only the one that finds the row unclaimed reports a row affected, so a
// session can never hand out two working tokens.
func (q *Queries) ClaimAgentSessionToken(ctx context.Context, arg ClaimAgentSessionTokenParams) (int64, error) {
	result, err := q.db.ExecContext(ctx, claimAgentSessionToken,
		arg.TokenHash,
		arg.TokenPrefix,
		arg.ExpiresAt,
		arg.Scopes,
		arg.ID,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

const getAgentSession = `SELECT id, user_id, created_at, expires_at, token_hash, token_prefix, revoked_at, scopes,
       status, requesting_address, approval_code, approved_at
FROM agent_sessions WHERE id = ?
`

func (q *Queries) GetAgentSession(ctx context.Context, id string) (AgentSession, error) {
	row := q.db.QueryRowContext(ctx, getAgentSession, id)
	var i AgentSession
	err := row.Scan(
		&i.ID,
		&i.UserID,
		&i.CreatedAt,
		&i.ExpiresAt,
		&i.TokenHash,
		&i.TokenPrefix,
		&i.RevokedAt,
		&i.Scopes,
		&i.Status,
		&i.RequestingAddress,
		&i.ApprovalCode,
		&i.ApprovedAt,
	)
	return i, err
}

const insertAgentSession = `INSERT INTO agent_sessions (id, user_id, created_at, expires_at, token_hash, token_prefix, revoked_at, scopes,
                            status, requesting_address, approval_code, approved_at)
VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?)
`

type InsertAgentSessionParams struct {
	ID                string
	UserID            int64
	CreatedAt         int64
	ExpiresAt         int64
	TokenHash         []byte
	TokenPrefix       string
	Scopes            string
	Status            int64
	RequestingAddress string
	ApprovalCode      string
	ApprovedAt        int64
}

func (q *Queries) InsertAgentSession(ctx context.Context, arg InsertAgentSessionParams) error {
	_, err := q.db.ExecContext(ctx, insertAgentSession,
		arg.ID,
		arg.UserID,
		arg.CreatedAt,
		arg.ExpiresAt,
		arg.TokenHash,
		arg.TokenPrefix,
		arg.Scopes,
		arg.Status,
		arg.RequestingAddress,
		arg.ApprovalCode,
		arg.ApprovedAt,
	)
	return err
}

const listAgentSessionsForUser = `SELECT id, user_id, created_at, expires_at, token_hash, token_prefix, revoked_at, scopes,
       status, requesting_address, approval_code, approved_at
FROM agent_sessions WHERE user_id = ? ORDER BY created_at DESC
`

func (q *Queries) ListAgentSessionsForUser(ctx context.Context, userID int64) ([]AgentSession, error) {
	rows, err := q.db.QueryContext(ctx, listAgentSessionsForUser, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []AgentSession
	for rows.Next() {
		var i AgentSession
		if err := rows.Scan(
			&i.ID,
			&i.UserID,
			&i.CreatedAt,
			&i.ExpiresAt,
			&i.TokenHash,
			&i.TokenPrefix,
			&i.RevokedAt,
			&i.Scopes,
			&i.Status,
			&i.RequestingAddress,
			&i.ApprovalCode,
			&i.ApprovedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const listPendingAgentSessionsForUser = `SELECT id, user_id, created_at, expires_at, token_hash, token_prefix, revoked_at, scopes,
       status, requesting_address, approval_code, approved_at
FROM agent_sessions WHERE user_id = ? AND status = 1 ORDER BY created_at DESC
`

func (q *Queries) ListPendingAgentSessionsForUser(ctx context.Context, userID int64) ([]AgentSession, error) {
	rows, err := q.db.QueryContext(ctx, listPendingAgentSessionsForUser, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []AgentSession
	for rows.Next() {
		var i AgentSession
		if err := rows.Scan(
			&i.ID,
			&i.UserID,
			&i.CreatedAt,
			&i.ExpiresAt,
			&i.TokenHash,
			&i.TokenPrefix,
			&i.RevokedAt,
			&i.Scopes,
			&i.Status,
			&i.RequestingAddress,
			&i.ApprovalCode,
			&i.ApprovedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const revokeAgentSession = `UPDATE agent_sessions SET revoked_at = ?, status = ?
WHERE id = ? AND user_id = ? AND revoked_at = 0
`

type RevokeAgentSessionParams struct {
	RevokedAt int64
	Status    int64
	ID        string
	UserID    int64
}

func (q *Queries) RevokeAgentSession(ctx context.Context, arg RevokeAgentSessionParams) error {
	_, err := q.db.ExecContext(ctx, revokeAgentSession,
		arg.RevokedAt,
		arg.Status,
		arg.ID,
		arg.UserID,
	)
	return err
}

const setAgentSessionStatus = `UPDATE agent_sessions SET status = ?, approved_at = ?, revoked_at = ?
WHERE id = ?
`

type SetAgentSessionStatusParams struct {
	Status     int64
	ApprovedAt int64
	RevokedAt  int64
	ID         string
}

func (q *Queries) SetAgentSessionStatus(ctx context.Context, arg SetAgentSessionStatusParams) error {
	_, err := q.db.ExecContext(ctx, setAgentSessionStatus,
		arg.Status,
		arg.ApprovedAt,
		arg.RevokedAt,
		arg.ID,
	)
	return err
}
