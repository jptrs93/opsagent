package users

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

type PersonalSession struct {
	ID                string
	UserID            int32
	CreatedAt         time.Time
	ExpiresAt         time.Time
	TokenHash         []byte
	RevokedAt         time.Time
	RequestingAddress string
	UserAgent         string
	LastActiveAt      time.Time
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

func personalSessionFromRow(row pq.PersonalSession) PersonalSession {
	return PersonalSession{
		ID: row.ID, UserID: int32(row.UserID), CreatedAt: time.Unix(row.CreatedAt, 0), ExpiresAt: timeOrZero(row.ExpiresAt),
		TokenHash: row.TokenHash, RevokedAt: timeOrZero(row.RevokedAt), RequestingAddress: row.RequestingAddress,
		UserAgent: row.UserAgent, LastActiveAt: timeOrZero(row.LastActiveAt),
	}
}

func InsertPersonalSession(q *pq.Queries, rec PersonalSession) error {
	tokenHash := rec.TokenHash
	if tokenHash == nil {
		tokenHash = []byte{}
	}
	return q.InsertPersonalSession(context.Background(), pq.InsertPersonalSessionParams{
		ID: rec.ID, UserID: int64(rec.UserID), CreatedAt: rec.CreatedAt.Unix(), ExpiresAt: unixOrZero(rec.ExpiresAt), TokenHash: tokenHash,
		RequestingAddress: rec.RequestingAddress, UserAgent: rec.UserAgent, LastActiveAt: unixOrZero(rec.LastActiveAt),
	})
}

func PersonalSessionByID(q *pq.Queries, id string) (PersonalSession, error) {
	row, err := q.GetPersonalSession(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return PersonalSession{}, ErrNotFound
	}
	if err != nil {
		return PersonalSession{}, err
	}
	return personalSessionFromRow(row), nil
}

func ListPersonalSessions(q *pq.Queries, userID int32) ([]PersonalSession, error) {
	rows, err := q.ListPersonalSessionsForUser(context.Background(), int64(userID))
	if err != nil {
		return nil, err
	}
	out := make([]PersonalSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, personalSessionFromRow(row))
	}
	return out, nil
}

func RevokePersonalSession(q *pq.Queries, id string, userID int32, at time.Time) (bool, error) {
	rows, err := q.RevokePersonalSession(context.Background(), pq.RevokePersonalSessionParams{RevokedAt: at.Unix(), ID: id, UserID: int64(userID)})
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func TouchPersonalSession(q *pq.Queries, id string, at time.Time) error {
	return q.TouchPersonalSessionActivity(context.Background(), pq.TouchPersonalSessionActivityParams{LastActiveAt: at.Unix(), ID: id})
}
