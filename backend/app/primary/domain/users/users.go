package users

import (
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

func update(ctx context.Context, q *pq.Queries, id int64) (*state.Update, error) {
	row, err := q.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	return &apigen.CoreUpdate{Users: []*apigen.User{&row}}, nil
}

func Write(store *state.Service, user *apigen.InternalUser) {
	ctx := context.Background()
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		if err := q.UpsertUser(ctx, pq.UpsertUserParams{ID: int64(user.ID), Name: user.Name, DataBlob: user.Encode(), CreatedAt: time.Now().UnixMilli()}); err != nil {
			return nil, err
		}
		return update(ctx, q, int64(user.ID))
	}); err != nil {
		panic(err)
	}
}

func TouchLastLogin(store *state.Service, userID int32) {
	ctx := context.Background()
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		if err := q.TouchUserLastLogin(ctx, pq.TouchUserLastLoginParams{ID: int64(userID), LastLoginAt: time.Now().UnixMilli()}); err != nil {
			return nil, err
		}
		return update(ctx, q, int64(userID))
	}); err != nil {
		panic(err)
	}
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
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		user, err := matching(ctx, q, predicate)
		if err != nil {
			return nil, err
		}
		f(user)
		if err := q.UpsertUser(ctx, pq.UpsertUserParams{ID: int64(user.ID), Name: user.Name, DataBlob: user.Encode(), CreatedAt: time.Now().UnixMilli()}); err != nil {
			return nil, err
		}
		return update(ctx, q, int64(user.ID))
	}); err != nil {
		panic(err)
	}
}

func WritePublicKey(q *pq.Queries, rec *apigen.PublicKeyRecord) {
	if err := q.UpsertPublicKey(context.Background(), pq.UpsertPublicKeyParams{Kid: rec.Kid, KeyBytes: rec.KeyBytes}); err != nil {
		panic(err)
	}
}

func PublicKey(q *pq.Queries, kid string) (*apigen.PublicKeyRecord, error) {
	row, err := q.GetPublicKey(context.Background(), kid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}
