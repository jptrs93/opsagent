package users

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var ErrNotFound = errors.New("not found")

func userMutation(ctx context.Context, q *pq.Queries, seq int64, user *apigen.InternalUser) (*state.WriteUpdate, error) {
	now := time.Now().UnixMilli()
	meta := pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: int64(user.ID), EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}
	_, err := q.GetUserRow(ctx, int64(user.ID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		meta.EventType = apigen.AuthzVerb_AUTHZ_VERB_CREATE
	case err != nil:
		return nil, err
	}
	return pq.NewUpdate(pq.UserMutation(meta, apigen.User{ID: user.ID, Name: user.Name, Credentials: user.Encode()})), nil
}

// Write stores the account, allocating its id when it has none.
func Write(store *state.Service, user *apigen.InternalUser) {
	ctx := context.Background()
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if user.ID == 0 {
			id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_USER)
			if err != nil {
				return nil, err
			}
			user.ID = int32(id)
		}
		return userMutation(ctx, q, seq, user)
	}); err != nil {
		panic(err)
	}
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
		return userMutation(ctx, q, seq, user)
	}); err != nil {
		panic(err)
	}
}
