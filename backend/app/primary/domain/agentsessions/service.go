package agentsessions

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

var ErrNotFound = errors.New("agent session not found")

type Update struct {
	UserID   int32
	Sessions []*apigen.AgentSession
}

// Service owns the session table and its unsequenced, owner-scoped stream.
// Its mutex is independent of the core state's writer freeze.
type Service struct {
	mu      sync.Mutex
	q       *pq.Queries
	updates pubsubu.PubSub[Update]
}

func New(q *pq.Queries) *Service { return &Service{q: q} }

// Record is a stored agent session. TokenHash is the SHA-256 of the
// issued token; the plaintext is never persisted. A session that is still a
// pending request has no token at all: TokenHash, TokenPrefix, and ExpiresAt
// stay zero until it is approved and collected.
type Record struct {
	ID                string
	UserID            int32
	CreatedAt         time.Time
	ExpiresAt         time.Time
	TokenHash         []byte
	TokenPrefix       string
	RevokedAt         time.Time
	Scopes            []string
	Status            apigen.AgentSessionStatus
	RequestingAddress string
	ApprovalCode      string
	ApprovedAt        time.Time
}

// Collected reports whether this session's token has been minted. Approval on
// its own does not mint one, so this is what separates a session waiting to be
// picked up from a live one.
func (r Record) Collected() bool { return len(r.TokenHash) > 0 }

func (s *Service) InsertAgentSession(rec Record) error {
	tokenHash := rec.TokenHash
	if tokenHash == nil {
		tokenHash = []byte{}
	}
	_, err := s.mutateAgentSession(rec.ID, func(q *pq.Queries) (bool, error) {
		err := q.InsertAgentSession(context.Background(), pq.InsertAgentSessionParams{
			ID:                rec.ID,
			UserID:            int64(rec.UserID),
			CreatedAt:         rec.CreatedAt.Unix(),
			ExpiresAt:         unixOrZero(rec.ExpiresAt),
			TokenHash:         tokenHash,
			TokenPrefix:       rec.TokenPrefix,
			Scopes:            strings.Join(rec.Scopes, ","),
			Status:            int64(rec.Status),
			RequestingAddress: rec.RequestingAddress,
			ApprovalCode:      rec.ApprovalCode,
			ApprovedAt:        unixOrZero(rec.ApprovedAt),
		})
		return err == nil, err
	})
	return err
}

// FetchAgentSession returns ErrNotFound when no session carries the id, which
// is the normal case for a token minted before this table existed.
func (s *Service) FetchAgentSession(id string) (Record, error) {
	row, err := s.q.GetAgentSession(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	return agentSessionRowToRecord(row), nil
}

func (s *Service) ListAgentSessionsForUser(userID int32) ([]Record, error) {
	rows, err := s.q.ListAgentSessionsForUser(context.Background(), int64(userID))
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentSessionRowToRecord(row))
	}
	return out, nil
}

// ListPendingAgentSessionsForUser returns the user's open session requests,
// newest first. There is normally at most one, but stale requests are only
// closed lazily, so a caller has to be prepared for several.
func (s *Service) ListPendingAgentSessionsForUser(userID int32) ([]Record, error) {
	rows, err := s.q.ListPendingAgentSessionsForUser(context.Background(), int64(userID))
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentSessionRowToRecord(row))
	}
	return out, nil
}

// SetAgentSessionStatus moves a session to a new state. approvedAt and revokedAt
// are written together with it so the row can never claim a state whose
// timestamp is missing.
func (s *Service) SetAgentSessionStatus(id string, status apigen.AgentSessionStatus, approvedAt, revokedAt time.Time) error {
	_, err := s.mutateAgentSession(id, func(q *pq.Queries) (bool, error) {
		err := q.SetAgentSessionStatus(context.Background(), pq.SetAgentSessionStatusParams{
			Status:     int64(status),
			ApprovedAt: unixOrZero(approvedAt),
			RevokedAt:  unixOrZero(revokedAt),
			ID:         id,
		})
		return err == nil, err
	})
	return err
}

// ApproveAgentSession approves a pending request and records the approver's
// scopes in the same statement. It reports false when the row was not pending,
// which is how a second approval of the same request is rejected.
func (s *Service) ApproveAgentSession(id string, userID int32, scopes []string, at time.Time) (bool, error) {
	return s.mutateAgentSession(id, func(q *pq.Queries) (bool, error) {
		rows, err := q.ApproveAgentSession(context.Background(), pq.ApproveAgentSessionParams{
			ApprovedAt: at.Unix(),
			Scopes:     strings.Join(scopes, ","),
			ID:         id,
			UserID:     int64(userID),
		})
		return rows > 0, err
	})
}

// ClaimAgentSessionToken records a freshly minted token against an approved
// session and reports whether this caller was the one that claimed it. A false
// return means another request got there first; the caller must discard the
// token it minted rather than hand out a second working credential.
func (s *Service) ClaimAgentSessionToken(id string, tokenHash []byte, tokenPrefix string, expiresAt time.Time, scopes []string) (bool, error) {
	return s.mutateAgentSession(id, func(q *pq.Queries) (bool, error) {
		rows, err := q.ClaimAgentSessionToken(context.Background(), pq.ClaimAgentSessionTokenParams{
			TokenHash:   tokenHash,
			TokenPrefix: tokenPrefix,
			ExpiresAt:   unixOrZero(expiresAt),
			Scopes:      strings.Join(scopes, ","),
			ID:          id,
		})
		return rows > 0, err
	})
}

// RevokeAgentSession is scoped by user id so one operator cannot revoke
// another's session by guessing its id.
func (s *Service) RevokeAgentSession(id string, userID int32, status apigen.AgentSessionStatus, at time.Time) error {
	_, err := s.mutateAgentSession(id, func(q *pq.Queries) (bool, error) {
		err := q.RevokeAgentSession(context.Background(), pq.RevokeAgentSessionParams{
			RevokedAt: at.Unix(),
			Status:    int64(status),
			ID:        id,
			UserID:    int64(userID),
		})
		return err == nil, err
	})
	return err
}

func publicRows(ctx context.Context, q *pq.Queries, userID int32) ([]*apigen.AgentSession, error) {
	rows, err := q.ListAgentSessionsForUser(ctx, int64(userID))
	if err != nil {
		return nil, err
	}
	out := make([]*apigen.AgentSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, ToProto(agentSessionRowToRecord(row)))
	}
	return out, nil
}

func (s *Service) Snapshot(userID int32) ([]*apigen.AgentSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return publicRows(context.Background(), s.q, userID)
}

func (s *Service) SnapshotAndSubscribe(userID int32) ([]*apigen.AgentSession, *pubsubu.Sub[Update], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := publicRows(context.Background(), s.q, userID)
	if err != nil {
		return nil, nil, err
	}
	sub := s.updates.Subscribe(func(_, next Update) bool { return next.UserID == userID })
	return items, sub, nil
}

func (s *Service) mutateAgentSession(id string, mutate func(*pq.Queries) (bool, error)) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	var changed bool
	var update Update
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		var err error
		changed, err = mutate(q)
		if err != nil || !changed {
			return err
		}
		row, err := q.GetAgentSession(ctx, id)
		if err != nil {
			return err
		}
		update.UserID = int32(row.UserID)
		update.Sessions, err = publicRows(ctx, q, update.UserID)
		return err
	})
	if err == nil && changed {
		s.updates.Notify(update)
	}
	return changed, err
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
func timeOrZero(unix int64) time.Time {
	if unix == 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}
