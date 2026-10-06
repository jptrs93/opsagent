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
	UserID            uint64
	CreatedAt         time.Time
	ExpiresAt         time.Time
	TokenHash         []byte
	TokenPrefix       string
	Status            apigen.AgentSessionStatus
	RequestingAddress string
	ApprovalCode      string
	ApprovedAt        time.Time
}

// Collected reports whether this session's token has been minted. Approval on
// its own does not mint one, so this is what separates a session waiting to be
// picked up from a live one.
func (r Record) Collected() bool { return len(r.TokenHash) > 0 }

func eventMeta(seq int64, author int64, eventType apigen.AuthzVerb) pq.EventMeta {
	return pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), Author: author, EventType: eventType}
}

func (s *Service) InsertAgentSession(rec Record, author int64) error {
	ctx := context.Background()
	return s.store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION)
		if err != nil {
			return nil, err
		}
		doc := ToProto(rec)
		if doc.Token.Present {
			doc.Token.Value.Hash = apigen.Some(rec.TokenHash)
		}
		return pq.NewUpdate(pq.AgentSessionMutation(eventMeta(seq, author, apigen.AuthzVerb_AUTHZ_VERB_CREATE), id, doc)), nil
	})
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

func (s *Service) ListAgentSessionsForUser(userID uint64) ([]Record, error) {
	rows, err := s.store.Queries().ListAgentSessionsForUser(context.Background(), userID)
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
func (s *Service) ListPendingAgentSessionsForUser(userID uint64) ([]Record, error) {
	rows, err := s.store.Queries().ListPendingAgentSessionsForUser(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentSessionRowToRecord(row))
	}
	return out, nil
}

// appendTransition applies change to the live row and publishes the result
// as an update; a change that returns false writes nothing.
func (s *Service) appendTransition(id string, author int64, change func(row *pq.AgentSession) bool) (bool, error) {
	ctx := context.Background()
	var changed bool
	err := s.store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		row, err := q.GetAgentSession(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if !change(&row) {
			return nil, nil
		}
		changed = true
		return pq.NewUpdate(pq.AgentSessionMutation(eventMeta(seq, author, apigen.AuthzVerb_AUTHZ_VERB_UPDATE), row.ID, row.Entity())), nil
	})
	return changed, err
}

// SetAgentSessionStatus moves a session to a new state. approvedAt is
// written together with it so the row can never claim a state whose
// timestamp is missing.
func (s *Service) SetAgentSessionStatus(id string, status apigen.AgentSessionStatus, approvedAt time.Time, author int64) error {
	_, err := s.appendTransition(id, author, func(row *pq.AgentSession) bool {
		row.Status, row.ApprovedAt = int64(status), unixOrZero(approvedAt)
		return true
	})
	return err
}

// ApproveAgentSession approves one of userID's pending requests. It reports
// false when the row was not pending, which is how a second approval of the
// same request is rejected.
func (s *Service) ApproveAgentSession(id string, userID uint64, at time.Time) (bool, error) {
	return s.appendTransition(id, int64(userID), func(row *pq.AgentSession) bool {
		if row.UserID != userID || row.Status != int64(apigen.AgentSessionStatus_AGENT_SESSION_STATUS_PENDING) {
			return false
		}
		row.Status, row.ApprovedAt = int64(apigen.AgentSessionStatus_AGENT_SESSION_STATUS_APPROVED), at.Unix()
		return true
	})
}

// ClaimAgentSessionToken records a freshly minted token against an approved
// session and reports whether this caller was the one that claimed it. A false
// return means another request got there first; the caller must discard the
// token it minted rather than hand out a second working credential.
func (s *Service) ClaimAgentSessionToken(id string, tokenHash []byte, tokenPrefix string, expiresAt time.Time) (bool, error) {
	return s.appendTransition(id, 0, func(row *pq.AgentSession) bool {
		if row.Status != int64(apigen.AgentSessionStatus_AGENT_SESSION_STATUS_APPROVED) || len(row.TokenHash) != 0 {
			return false
		}
		row.TokenHash, row.TokenPrefix, row.ExpiresAt = tokenHash, tokenPrefix, unixOrZero(expiresAt)
		return true
	})
}

// RevokeAgentSession is scoped by user id so one operator cannot revoke
// another's session by guessing its id. author is the caller's attribution
// id, negative when the session revokes itself. A session already rejected
// or revoked is left as it is.
func (s *Service) RevokeAgentSession(id string, userID uint64, status apigen.AgentSessionStatus, author int64) error {
	_, err := s.appendTransition(id, author, func(row *pq.AgentSession) bool {
		if row.UserID != userID || row.Status == int64(apigen.AgentSessionStatus_AGENT_SESSION_STATUS_REJECTED) || row.Status == int64(apigen.AgentSessionStatus_AGENT_SESSION_STATUS_REVOKED) {
			return false
		}
		row.Status = int64(status)
		return true
	})
	return err
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
