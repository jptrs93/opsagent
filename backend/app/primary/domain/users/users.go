package users

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var ErrNotFound = errors.New("not found")

func appendUser(ctx context.Context, q *pq.Queries, seq int64, user *apigen.InternalUser) (*state.WriteUpdate, error) {
	now := time.Now().UnixMilli()
	meta := pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: int64(user.ID), EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}
	createdAt := now
	previous, err := q.GetUserRow(ctx, int64(user.ID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		meta.EventType = apigen.AuthzVerb_AUTHZ_VERB_CREATE
	case err != nil:
		return nil, err
	default:
		createdAt = previous.CreatedAt
	}
	if err := q.InsertUserEvent(ctx, pq.UserEventParams{EventMeta: meta, UserID: int64(user.ID), Name: user.Name, DataBlob: user.Encode(), CreatedAt: createdAt}); err != nil {
		return nil, err
	}
	row, err := q.GetUserRow(ctx, int64(user.ID))
	if err != nil {
		return nil, err
	}
	return pq.NewUpdate(pq.UserMutation(row)), nil
}

func Write(store *state.Service, user *apigen.InternalUser) {
	ctx := context.Background()
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		return appendUser(ctx, q, seq, user)
	}); err != nil {
		panic(err)
	}
}

func NextID(q *pq.Queries) int32 {
	return int32(erru.Must(q.NextUserID(context.Background())))
}

// SetCredential stores a passkey by credential id. The library hands back the
// same credential after every login with a fresh sign counter and flags, so
// an existing entry is replaced in place rather than appended.
func SetCredential(u *apigen.InternalUser, id, data []byte) {
	for _, c := range u.Credentials {
		if bytes.Equal(c.ID, id) {
			c.Data = data
			return
		}
	}
	u.Credentials = append(u.Credentials, &apigen.WebAuthnCredential{ID: id, Data: data})
}

// DedupeCredentials collapses entries that share a credential id onto the
// last one, which carries the newest sign counter. It reports whether
// anything changed.
func DedupeCredentials(u *apigen.InternalUser) bool {
	last := make(map[string]*apigen.WebAuthnCredential, len(u.Credentials))
	for _, c := range u.Credentials {
		last[string(c.ID)] = c
	}
	if len(last) == len(u.Credentials) {
		return false
	}
	kept := make([]*apigen.WebAuthnCredential, 0, len(last))
	seen := make(map[string]bool, len(last))
	for _, c := range u.Credentials {
		if seen[string(c.ID)] {
			continue
		}
		seen[string(c.ID)] = true
		kept = append(kept, last[string(c.ID)])
	}
	u.Credentials = kept
	return true
}

func ListPublic(q *pq.Queries) []*apigen.User {
	rows := erru.Must(q.ListUsers(context.Background()))
	out := make([]*apigen.User, 0, len(rows))
	for i := range rows {
		out = append(out, &rows[i])
	}
	return out
}

func Count(q *pq.Queries) int {
	return len(erru.Must(q.ListUsers(context.Background())))
}

func ByID(q *pq.Queries, id int32) (*apigen.InternalUser, error) {
	row, err := q.GetInternalUser(context.Background(), int64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

func matching(ctx context.Context, q *pq.Queries, predicate func(*apigen.InternalUser) bool) (*apigen.InternalUser, error) {
	rows, err := q.ListInternalUsers(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range rows {
		if predicate(u) {
			return u, nil
		}
	}
	return nil, ErrNotFound
}

func Matching(q *pq.Queries, predicate func(*apigen.InternalUser) bool) (*apigen.InternalUser, error) {
	return matching(context.Background(), q, predicate)
}

func UpdateMatching(store *state.Service, predicate func(*apigen.InternalUser) bool, f func(*apigen.InternalUser)) {
	ctx := context.Background()
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		user, err := matching(ctx, q, predicate)
		if err != nil {
			return nil, err
		}
		f(user)
		return appendUser(ctx, q, seq, user)
	}); err != nil {
		panic(err)
	}
}
