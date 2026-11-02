package users

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var ErrLinkInvalid = errors.New("link is not redeemable")

// UserSession is a session row in handler form. UserID is 0 on a root
// session. Author is who wrote the row last; IssuedBy is set once on a
// link to the user who issued it (0 for root) and is what the issuer's
// list, revoke and stream visibility key on, since the author changes when
// the link is redeemed.
type UserSession struct {
	ID                string
	EntityID          uint64
	UserID            uint64
	Author            int64
	IssuedBy          uint64
	CreatedAt         time.Time
	ExpiresAt         time.Time
	TokenHash         []byte
	RevokedAt         time.Time
	RedeemedAt        time.Time
	OpenedBy          uint64
	Kind              apigen.UserSessionKind
	RequestingAddress string
	UserAgent         string
}

func timeOrZero(unix int64) time.Time {
	if unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

func userSessionFromRow(row pq.UserSession) UserSession {
	return UserSession{
		ID: row.SessionID, EntityID: row.ID, UserID: row.UserID, Author: row.Author, CreatedAt: time.UnixMilli(row.CreatedAt), ExpiresAt: timeOrZero(row.ExpiresAt),
		TokenHash: row.TokenHash, RevokedAt: timeOrZero(row.RevokedAt), RedeemedAt: timeOrZero(row.RedeemedAt), OpenedBy: row.OpenedBy, IssuedBy: row.IssuedBy,
		Kind: apigen.UserSessionKind(row.Kind), RequestingAddress: row.RequestingAddress, UserAgent: row.UserAgent,
	}
}

func (rec UserSession) entity() *apigen.UserSession {
	doc := &apigen.UserSession{SessionID: rec.ID, ExpiresAt: rec.ExpiresAt, TokenHash: apigen.Some(rec.TokenHash),
		Kind: rec.Kind, RequestingAddress: rec.RequestingAddress, UserAgent: rec.UserAgent}
	if rec.UserID != 0 {
		doc.UserID = apigen.Some(rec.UserID)
	}
	if rec.OpenedBy != 0 {
		doc.OpenedBy = apigen.Some(rec.OpenedBy)
	}
	if rec.IssuedBy != 0 {
		doc.IssuedBy = apigen.Some(rec.IssuedBy)
	}
	return doc
}

// sessionMutations builds the create of a session row and, for a full
// session of a pending user, the activation of that user, as one commit's
// mutations.
func sessionMutations(ctx context.Context, q *pq.Queries, seq int64, rec UserSession) ([]pq.Mutation, uint64, error) {
	id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_USER_SESSION)
	if err != nil {
		return nil, 0, err
	}
	now := time.Now()
	meta := pq.EventMeta{GlobalSeq: seq, EventTime: now.UnixMilli(), Author: rec.Author, EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}
	ms := []pq.Mutation{pq.UserSessionMutation(meta, id, rec.entity())}
	if rec.Kind == apigen.UserSessionKind_USER_SESSION_KIND_FULL && rec.UserID != 0 {
		user, err := q.GetFullUser(ctx, rec.UserID)
		if err != nil {
			return nil, 0, err
		}
		if !user.ActivatedAt.Present {
			user.ActivatedAt = apigen.Some(now)
			ms = append(ms, pq.UserMutation(pq.EventMeta{GlobalSeq: seq, EventTime: now.UnixMilli(), Author: rec.Author, EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}, *user))
		}
	}
	return ms, id, nil
}

// InsertUserSession writes the session row; a full session for a pending
// user activates the user in the same commit.
func InsertUserSession(store *state.Service, rec UserSession) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		ms, _, err := sessionMutations(ctx, q, seq, rec)
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(ms...), nil
	})
}

// RedeemLink turns an unredeemed, unexpired, unrevoked link whose hash
// matches into the full session described by open, in one commit: the link
// is stamped redeemed, the full session row is created with opened_by set,
// and a pending user is activated. Any failed check is ErrLinkInvalid, so
// a caller cannot tell which check failed.
func RedeemLink(store *state.Service, linkSessionID string, tokenHash []byte, open UserSession, now time.Time) (UserSession, error) {
	ctx := context.Background()
	var opened UserSession
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		row, err := q.GetUserSession(ctx, linkSessionID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrLinkInvalid
		}
		if err != nil {
			return nil, err
		}
		link := userSessionFromRow(row)
		if link.Kind != apigen.UserSessionKind_USER_SESSION_KIND_LINK || link.UserID == 0 ||
			!link.RevokedAt.IsZero() || !link.RedeemedAt.IsZero() || !now.Before(link.ExpiresAt) ||
			subtle.ConstantTimeCompare(link.TokenHash, tokenHash) != 1 {
			return nil, ErrLinkInvalid
		}
		row.RedeemedAt = now.Unix()
		open.UserID = link.UserID
		open.Author = int64(link.UserID)
		open.OpenedBy = link.EntityID
		open.Kind = apigen.UserSessionKind_USER_SESSION_KIND_FULL
		ms, _, err := sessionMutations(ctx, q, seq, open)
		if err != nil {
			return nil, err
		}
		redeemMeta := pq.EventMeta{GlobalSeq: seq, EventTime: now.UnixMilli(), Author: int64(link.UserID), EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}
		ms = append([]pq.Mutation{pq.UserSessionMutation(redeemMeta, row.ID, row.Entity())}, ms...)
		opened = open
		return pq.NewUpdate(ms...), nil
	})
	return opened, err
}

func UserSessionByID(q *pq.Queries, id string) (UserSession, error) {
	row, err := q.GetUserSession(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return UserSession{}, ErrNotFound
	}
	if err != nil {
		return UserSession{}, err
	}
	return userSessionFromRow(row), nil
}

// ListUserSessions lists the user's own sessions and the links the user
// issued for others.
func ListUserSessions(q *pq.Queries, userID uint64) ([]UserSession, error) {
	rows, err := q.ListUserSessionsForUser(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]UserSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, userSessionFromRow(row))
	}
	return out, nil
}

// RevokeUserSession revokes the row when it belongs to the user, or is a
// link the user issued. It reports false when nothing changed.
func RevokeUserSession(store *state.Service, id string, userID uint64, at time.Time) (bool, error) {
	ctx := context.Background()
	var revoked bool
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		row, err := q.GetUserSession(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if !CanRevoke(userSessionFromRow(row), userID) || row.RevokedAt != 0 {
			return nil, nil
		}
		row.RevokedAt = at.Unix()
		revoked = true
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: at.UnixMilli(), Author: int64(userID), EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}
		return pq.NewUpdate(pq.UserSessionMutation(meta, row.ID, row.Entity())), nil
	})
	return revoked, err
}

// CanRevoke reports whether the user owns the session, or issued it as a
// link.
func CanRevoke(rec UserSession, userID uint64) bool {
	if userID == 0 {
		return false
	}
	return rec.UserID == userID || IssuedLink(rec, userID)
}

// IssuedLink reports whether the row is a link the user issued.
func IssuedLink(rec UserSession, userID uint64) bool {
	return userID != 0 && rec.Kind == apigen.UserSessionKind_USER_SESSION_KIND_LINK && rec.IssuedBy == userID
}
