package state

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

type PersonalSessionRecord struct {
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

func personalSessionRowToRecord(row pq.PersonalSession) PersonalSessionRecord {
	return PersonalSessionRecord{
		ID:                row.ID,
		UserID:            int32(row.UserID),
		CreatedAt:         time.Unix(row.CreatedAt, 0),
		ExpiresAt:         timeOrZero(row.ExpiresAt),
		TokenHash:         row.TokenHash,
		RevokedAt:         timeOrZero(row.RevokedAt),
		RequestingAddress: row.RequestingAddress,
		UserAgent:         row.UserAgent,
		LastActiveAt:      timeOrZero(row.LastActiveAt),
	}
}

func (s *Service) InsertPersonalSession(rec PersonalSessionRecord) error {
	tokenHash := rec.TokenHash
	if tokenHash == nil {
		tokenHash = []byte{}
	}
	return s.q.InsertPersonalSession(context.Background(), pq.InsertPersonalSessionParams{
		ID:                rec.ID,
		UserID:            int64(rec.UserID),
		CreatedAt:         rec.CreatedAt.Unix(),
		ExpiresAt:         unixOrZero(rec.ExpiresAt),
		TokenHash:         tokenHash,
		RequestingAddress: rec.RequestingAddress,
		UserAgent:         rec.UserAgent,
		LastActiveAt:      unixOrZero(rec.LastActiveAt),
	})
}

func (s *Service) FetchPersonalSession(id string) (PersonalSessionRecord, error) {
	row, err := s.q.GetPersonalSession(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return PersonalSessionRecord{}, ErrNotFound
	}
	if err != nil {
		return PersonalSessionRecord{}, err
	}
	return personalSessionRowToRecord(row), nil
}

func (s *Service) ListPersonalSessionsForUser(userID int32) ([]PersonalSessionRecord, error) {
	rows, err := s.q.ListPersonalSessionsForUser(context.Background(), int64(userID))
	if err != nil {
		return nil, err
	}
	out := make([]PersonalSessionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, personalSessionRowToRecord(row))
	}
	return out, nil
}

func (s *Service) RevokePersonalSession(id string, userID int32, at time.Time) (bool, error) {
	rows, err := s.q.RevokePersonalSession(context.Background(), pq.RevokePersonalSessionParams{
		RevokedAt: at.Unix(),
		ID:        id,
		UserID:    int64(userID),
	})
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func (s *Service) TouchPersonalSessionActivity(id string, at time.Time) error {
	return s.q.TouchPersonalSessionActivity(context.Background(), pq.TouchPersonalSessionActivityParams{
		LastActiveAt: at.Unix(),
		ID:           id,
	})
}
