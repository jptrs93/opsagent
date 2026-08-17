package apigen

import (
	"context"
	"time"
)

type Context struct {
	Ctx   context.Context
	User  *InternalUser
	Token string
}

// AttributionUserID is the id recorded on rows this request creates or
// updates (author): the user id, negated when the session
// acts with delegated (agent) authority, 0 when unauthenticated. Only for
// attribution — authz lookups and session ownership checks key on the real
// User.ID and must never see the negated form.
func (c Context) AttributionUserID() int32 {
	if c.User == nil {
		return 0
	}
	if c.User.Delegated {
		return -c.User.ID
	}
	return c.User.ID
}

func (c Context) Deadline() (deadline time.Time, ok bool) {
	return c.Ctx.Deadline()
}

func (c Context) Done() <-chan struct{} {
	return c.Ctx.Done()
}

func (c Context) Err() error {
	return c.Ctx.Err()
}

func (c Context) Value(key any) any {
	return c.Ctx.Value(key)
}
