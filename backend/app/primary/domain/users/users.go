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

func userMutation(ctx context.Context, q *pq.Queries, seq int64, user *apigen.User) (*state.WriteUpdate, error) {
	now := time.Now().UnixMilli()
	meta := pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: int64(user.ID), EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}
	_, err := q.GetUserRow(ctx, user.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		meta.EventType = apigen.AuthzVerb_AUTHZ_VERB_CREATE
	case err != nil:
		return nil, err
	}
	return pq.NewUpdate(pq.UserMutation(meta, *user)), nil
}

// Write stores the account, allocating its id when it has none.
func Write(store *state.Service, user *apigen.User) {
	ctx := context.Background()
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if user.ID == 0 {
			id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_USER)
			if err != nil {
				return nil, err
			}
			user.ID = id
		}
		return userMutation(ctx, q, seq, user)
	}); err != nil {
		panic(err)
	}
}

// SetCredential stores a passkey by credential id. The library hands back the
// same credential after every login with a fresh sign counter and flags, so
// an existing entry is replaced in place rather than appended.
func SetCredential(u *apigen.User, id, data []byte) {
	creds := u.Authentication.Credentials
	for i := range creds {
		if bytes.Equal(creds[i].ID, id) {
			creds[i].Data = data
			return
		}
	}
	u.Authentication.Credentials = append(creds, apigen.WebAuthnCredential{ID: id, Data: data})
}

func ByID(q *pq.Queries, id uint64) (*apigen.User, error) {
	row, err := q.GetFullUser(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

func matching(ctx context.Context, q *pq.Queries, predicate func(*apigen.User) bool) (*apigen.User, error) {
	rows, err := q.ListFullUsers(ctx)
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

func Matching(q *pq.Queries, predicate func(*apigen.User) bool) (*apigen.User, error) {
	return matching(context.Background(), q, predicate)
}

func UpdateMatching(store *state.Service, predicate func(*apigen.User) bool, f func(*apigen.User)) {
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
