package users

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var ErrNotFound = errors.New("not found")
var ErrNameTaken = errors.New("username already in use")

func userMutation(ctx context.Context, q *pq.Queries, seq int64, author int64, user *apigen.User) (pq.Mutation, error) {
	now := time.Now().UnixMilli()
	meta := pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: author, EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}
	_, err := q.GetUserRow(ctx, user.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		meta.EventType = apigen.AuthzVerb_AUTHZ_VERB_CREATE
	case err != nil:
		return pq.Mutation{}, err
	}
	return pq.UserMutation(meta, *user), nil
}

// Write stores the account, allocating its id when it has none. The account
// writes as its own author.
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
		m, err := userMutation(ctx, q, seq, int64(user.ID), user)
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(m), nil
	}); err != nil {
		panic(err)
	}
}

// Create writes a new account and whatever extra mutations the caller
// builds for its id (its grants) in one commit, as the given author. The
// name, when set, must be free. It returns the user mutation for the
// write receipt.
func Create(store *state.Service, user *apigen.User, author int64, extra func(q *pq.Queries, seq int64, userID uint64) ([]pq.Mutation, error)) (pq.Mutation, error) {
	ctx := context.Background()
	var userM pq.Mutation
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if user.Name.Present {
			taken, err := nameTaken(ctx, q, user.Name.Value, 0)
			if err != nil {
				return nil, err
			}
			if taken {
				return nil, ErrNameTaken
			}
		}
		id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_USER)
		if err != nil {
			return nil, err
		}
		user.ID = id
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), Author: author, EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}
		userM = pq.UserMutation(meta, *user)
		ms := []pq.Mutation{userM}
		if extra != nil {
			more, err := extra(q, seq, id)
			if err != nil {
				return nil, err
			}
			ms = append(ms, more...)
		}
		return pq.NewUpdate(ms...), nil
	})
	return userM, err
}

// Update applies f to the live account and writes it as the given author.
// f returning an error abandons the write. It returns the mutation for the
// write receipt.
func Update(store *state.Service, id uint64, author int64, f func(*apigen.User) error) (pq.Mutation, error) {
	ctx := context.Background()
	var m pq.Mutation
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		user, err := q.GetFullUser(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if err := f(user); err != nil {
			return nil, err
		}
		m, err = userMutation(ctx, q, seq, author, user)
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(m), nil
	})
	return m, err
}

// Rename sets the account's name, refusing one another account holds.
func Rename(store *state.Service, id uint64, author int64, name string) (pq.Mutation, error) {
	ctx := context.Background()
	var m pq.Mutation
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		taken, err := nameTaken(ctx, q, name, id)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, ErrNameTaken
		}
		user, err := q.GetFullUser(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		user.Name = apigen.Some(name)
		m, err = userMutation(ctx, q, seq, author, user)
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(m), nil
	})
	return m, err
}

func nameTaken(ctx context.Context, q *pq.Queries, name string, exceptID uint64) (bool, error) {
	rows, err := q.ListUserRows(ctx)
	if err != nil {
		return false, err
	}
	want := strings.TrimSpace(name)
	for _, r := range rows {
		if r.ID != exceptID && strings.TrimSpace(r.Name) == want {
			return true, nil
		}
	}
	return false, nil
}

// NameTaken reports whether an account other than exceptID holds the name,
// compared trimmed on both sides.
func NameTaken(q *pq.Queries, name string, exceptID uint64) (bool, error) {
	return nameTaken(context.Background(), q, name, exceptID)
}

// ByName finds the account with this name, compared trimmed on both sides.
func ByName(q *pq.Queries, name string) (*apigen.User, error) {
	want := strings.TrimSpace(name)
	return Matching(q, func(u *apigen.User) bool {
		return u.Name.Present && strings.TrimSpace(u.Name.Value) == want
	})
}

// SetCredential stores a passkey by credential id. The library hands back the
// same credential after every login with a fresh sign counter and flags, so
// an existing entry is replaced in place and stamped as used; a new entry
// records when it was registered.
func SetCredential(u *apigen.User, id, data []byte, now time.Time) {
	creds := u.Authentication.Credentials
	for i := range creds {
		if bytes.Equal(creds[i].ID, id) {
			creds[i].Data = data
			creds[i].LastUsedAt = apigen.Some(now)
			return
		}
	}
	u.Authentication.Credentials = append(creds, apigen.WebAuthnCredential{ID: id, Data: data, CreatedAt: now, Label: defaultCredentialLabel(len(creds) + 1)})
}

func defaultCredentialLabel(n int) string {
	if n == 1 {
		return "Passkey"
	}
	return "Passkey " + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// RemoveCredential drops the passkey with this id; false when none matched.
func RemoveCredential(u *apigen.User, id []byte) bool {
	creds := u.Authentication.Credentials
	for i := range creds {
		if bytes.Equal(creds[i].ID, id) {
			u.Authentication.Credentials = append(creds[:i:i], creds[i+1:]...)
			return true
		}
	}
	return false
}

// HasWayIn reports whether the account would still be able to sign in: a
// password, or at least one passkey.
func HasWayIn(u *apigen.User) bool {
	return u.PasswordHash.Present || len(u.Authentication.Credentials) > 0
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

// UpdateMatching applies f to the first account the predicate picks and
// writes it as its own author. Storage failure is a crash: the callers are
// the passkey library's persistence hooks, where there is no one to report
// to.
func UpdateMatching(store *state.Service, predicate func(*apigen.User) bool, f func(*apigen.User)) {
	ctx := context.Background()
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		user, err := matching(ctx, q, predicate)
		if err != nil {
			return nil, err
		}
		f(user)
		m, err := userMutation(ctx, q, seq, int64(user.ID), user)
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(m), nil
	}); err != nil {
		panic(err)
	}
}

// ActivateExisting marks every account from before pending users existed as
// activated when it has a passkey or ever held a full session, so it does
// not show as pending after the upgrade. It is idempotent and runs on every
// primary start.
func ActivateExisting(store *state.Service) error {
	ctx := context.Background()
	q := store.Queries()
	rows, err := q.ListUserRows(ctx)
	if err != nil {
		return err
	}
	sessions, err := q.ListAllUserSessions(ctx)
	if err != nil {
		return err
	}
	signedIn := map[uint64]bool{}
	for _, s := range sessions {
		if s.Kind == int64(apigen.UserSessionKind_USER_SESSION_KIND_FULL) {
			signedIn[s.UserID] = true
		}
	}
	for _, r := range rows {
		if r.ActivatedAt != 0 {
			continue
		}
		u, err := pq.UserEntity(r)
		if err != nil {
			return err
		}
		if len(u.Authentication.Credentials) == 0 && !signedIn[r.ID] {
			continue
		}
		if _, err := Update(store, r.ID, 0, func(u *apigen.User) error {
			u.ActivatedAt = apigen.Some(time.Now())
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
