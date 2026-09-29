package agentsessions

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var ErrNotFound = errors.New("agent session not found")

// Service writes agent sessions under Commit. Every change publishes the
// session's latest document in CoreUpdate.AgentSessions; a write that changed
// nothing consumes no seq.
type Service struct {
	store *state.Service
}

func New(store *state.Service) *Service { return &Service{store: store} }

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
	Status            apigen.AgentSessionStatus
	RequestingAddress string
	ApprovalCode      string
	ApprovedAt        time.Time
}

// Collected reports whether this session's token has been minted. Approval on
// its own does not mint one, so this is what separates a session waiting to be
// picked up from a live one.
func (r Record) Collected() bool { return len(r.TokenHash) > 0 }

func eventMeta(seq int64, author int32, eventType apigen.AuthzVerb) pq.EventMeta {
	return pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), Author: int64(author), EventType: eventType}
}

func (s *Service) InsertAgentSession(rec Record, author int32) error {
	_, err := s.mutateAgentSession(rec.ID, func(q *pq.Queries, seq int64) (bool, error) {
		err := q.InsertAgentSessionEvent(context.Background(), pq.AgentSessionEventParams{
			EventMeta:         eventMeta(seq, author, apigen.AuthzVerb_AUTHZ_VERB_CREATE),
			SessionID:         rec.ID,
			UserID:            int64(rec.UserID),
			CreatedAt:         rec.CreatedAt.Unix(),
			ExpiresAt:         unixOrZero(rec.ExpiresAt),
			TokenHash:         rec.TokenHash,
			TokenPrefix:       rec.TokenPrefix,
			Status:            int64(rec.Status),
			RequestingAddress: rec.RequestingAddress,
			ApprovalCode:      rec.ApprovalCode,
			ApprovedAt:        unixOrZero(rec.ApprovedAt),
		})
		return err == nil, err
	})
	return err
}

// FetchAgentSession returns ErrNotFound when no session carries the id.
func (s *Service) FetchAgentSession(id string) (Record, error) {
	row, err := s.store.Queries().GetAgentSession(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	return agentSessionRowToRecord(row), nil
}

func (s *Service) ListAgentSessionsForUser(userID int32) ([]Record, error) {
	rows, err := s.store.Queries().ListAgentSessionsForUser(context.Background(), int64(userID))
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
	rows, err := s.store.Queries().ListPendingAgentSessionsForUser(context.Background(), int64(userID))
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentSessionRowToRecord(row))
	}
	return out, nil
}

func (s *Service) appendTransition(id string, author int32, change func(row pq.AgentSession, event *pq.AgentSessionEventParams) bool) (bool, error) {
	return s.mutateAgentSession(id, func(q *pq.Queries, seq int64) (bool, error) {
		ctx := context.Background()
		row, err := q.GetAgentSession(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		event := row.Event(eventMeta(seq, author, apigen.AuthzVerb_AUTHZ_VERB_UPDATE))
		if !change(row, &event) {
			return false, nil
		}
		if err := q.InsertAgentSessionEvent(ctx, event); err != nil {
			return false, err
		}
		return true, nil
	})
}

// SetAgentSessionStatus moves a session to a new state. approvedAt and revokedAt
// are written together with it so the row can never claim a state whose
// timestamp is missing.
func (s *Service) SetAgentSessionStatus(id string, status apigen.AgentSessionStatus, approvedAt, revokedAt time.Time, author int32) error {
	_, err := s.appendTransition(id, author, func(_ pq.AgentSession, event *pq.AgentSessionEventParams) bool {
		event.Status, event.ApprovedAt, event.RevokedAt = int64(status), unixOrZero(approvedAt), unixOrZero(revokedAt)
		return true
	})
	return err
}

// ApproveAgentSession approves one of userID's pending requests. It reports
// false when the row was not pending, which is how a second approval of the
// same request is rejected.
func (s *Service) ApproveAgentSession(id string, userID int32, at time.Time) (bool, error) {
	return s.appendTransition(id, userID, func(row pq.AgentSession, event *pq.AgentSessionEventParams) bool {
		if row.UserID != int64(userID) || row.Status != int64(apigen.AgentSessionStatus_AGENT_SESSION_PENDING) {
			return false
		}
		event.Status, event.ApprovedAt = int64(apigen.AgentSessionStatus_AGENT_SESSION_APPROVED), at.Unix()
		return true
	})
}

// ClaimAgentSessionToken records a freshly minted token against an approved
// session and reports whether this caller was the one that claimed it. A false
// return means another request got there first; the caller must discard the
// token it minted rather than hand out a second working credential.
func (s *Service) ClaimAgentSessionToken(id string, tokenHash []byte, tokenPrefix string, expiresAt time.Time) (bool, error) {
	return s.appendTransition(id, 0, func(row pq.AgentSession, event *pq.AgentSessionEventParams) bool {
		if row.Status != int64(apigen.AgentSessionStatus_AGENT_SESSION_APPROVED) || len(row.TokenHash) != 0 {
			return false
		}
		event.TokenHash, event.TokenPrefix, event.ExpiresAt = tokenHash, tokenPrefix, unixOrZero(expiresAt)
		return true
	})
}

// RevokeAgentSession is scoped by user id so one operator cannot revoke
// another's session by guessing its id. author is the caller's attribution
// id, negative when the session revokes itself.
func (s *Service) RevokeAgentSession(id string, userID int32, status apigen.AgentSessionStatus, at time.Time, author int32) error {
	_, err := s.appendTransition(id, author, func(row pq.AgentSession, event *pq.AgentSessionEventParams) bool {
		if row.UserID != int64(userID) || row.RevokedAt != 0 {
			return false
		}
		event.RevokedAt, event.Status = at.Unix(), int64(status)
		return true
	})
	return err
}

func (s *Service) mutateAgentSession(id string, mutate func(*pq.Queries, int64) (bool, error)) (bool, error) {
	ctx := context.Background()
	var changed bool
	err := s.store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		var err error
		changed, err = mutate(q, seq)
		if err != nil || !changed {
			return nil, err
		}
		row, err := q.GetAgentSession(ctx, id)
		if err != nil {
			return nil, err
		}
		return &apigen.CoreUpdate{AgentSessions: []*apigen.AgentSession{row.Proto()}}, nil
	})
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
