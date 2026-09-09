package pq

import (
	"context"
)

type PersonalSession struct {
	ID                string
	UserID            int64
	CreatedAt         int64
	ExpiresAt         int64
	TokenHash         []byte
	RevokedAt         int64
	RequestingAddress string
	UserAgent         string
	LastActiveAt      int64
}

const getPersonalSession = `SELECT id, user_id, created_at, expires_at, token_hash, revoked_at,
       requesting_address, user_agent, last_active_at
FROM personal_sessions WHERE id = ?
`

func (q *Queries) GetPersonalSession(ctx context.Context, id string) (PersonalSession, error) {
	row := q.db.QueryRowContext(ctx, getPersonalSession, id)
	var i PersonalSession
	err := row.Scan(
		&i.ID,
		&i.UserID,
		&i.CreatedAt,
		&i.ExpiresAt,
		&i.TokenHash,
		&i.RevokedAt,
		&i.RequestingAddress,
		&i.UserAgent,
		&i.LastActiveAt,
	)
	return i, err
}

const insertPersonalSession = `INSERT INTO personal_sessions (id, user_id, created_at, expires_at, token_hash, revoked_at,
                               requesting_address, user_agent, last_active_at)
VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?)
`

type InsertPersonalSessionParams struct {
	ID                string
	UserID            int64
	CreatedAt         int64
	ExpiresAt         int64
	TokenHash         []byte
	RequestingAddress string
	UserAgent         string
	LastActiveAt      int64
}

func (q *Queries) InsertPersonalSession(ctx context.Context, arg InsertPersonalSessionParams) error {
	_, err := q.db.ExecContext(ctx, insertPersonalSession,
		arg.ID,
		arg.UserID,
		arg.CreatedAt,
		arg.ExpiresAt,
		arg.TokenHash,
		arg.RequestingAddress,
		arg.UserAgent,
		arg.LastActiveAt,
	)
	return err
}

const listPersonalSessionsForUser = `SELECT id, user_id, created_at, expires_at, token_hash, revoked_at,
       requesting_address, user_agent, last_active_at
FROM personal_sessions WHERE user_id = ? ORDER BY created_at DESC
`

func (q *Queries) ListPersonalSessionsForUser(ctx context.Context, userID int64) ([]PersonalSession, error) {
	rows, err := q.db.QueryContext(ctx, listPersonalSessionsForUser, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []PersonalSession
	for rows.Next() {
		var i PersonalSession
		if err := rows.Scan(
			&i.ID,
			&i.UserID,
			&i.CreatedAt,
			&i.ExpiresAt,
			&i.TokenHash,
			&i.RevokedAt,
			&i.RequestingAddress,
			&i.UserAgent,
			&i.LastActiveAt,
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

const revokePersonalSession = `UPDATE personal_sessions SET revoked_at = ?
WHERE id = ? AND user_id = ? AND revoked_at = 0
`

type RevokePersonalSessionParams struct {
	RevokedAt int64
	ID        string
	UserID    int64
}

func (q *Queries) RevokePersonalSession(ctx context.Context, arg RevokePersonalSessionParams) (int64, error) {
	result, err := q.db.ExecContext(ctx, revokePersonalSession, arg.RevokedAt, arg.ID, arg.UserID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

const touchPersonalSessionActivity = `UPDATE personal_sessions SET last_active_at = ? WHERE id = ?
`

type TouchPersonalSessionActivityParams struct {
	LastActiveAt int64
	ID           string
}

func (q *Queries) TouchPersonalSessionActivity(ctx context.Context, arg TouchPersonalSessionActivityParams) error {
	_, err := q.db.ExecContext(ctx, touchPersonalSessionActivity, arg.LastActiveAt, arg.ID)
	return err
}
