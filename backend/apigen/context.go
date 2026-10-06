package apigen

import (
	"context"
	"time"
)

type Context struct {
	Ctx   context.Context
	User  *User
	Token string
	// Delegated is set while a request is verified under an agent-session
	// token: the session acts with its owner's authority but is attributed
	// and authorised as a delegate.
	Delegated bool
	// SessionID, SessionKind, and SessionExpiresAt describe the session row the
	// bearer token resolved to. Empty when the route needed no auth. An agent
	// token is always a FULL session; only master-password exchange opens a
	// BOOTSTRAP one.
	SessionID        string
	SessionKind      UserSessionKind
	SessionExpiresAt time.Time
}

// AttributionUserID is the id recorded on rows this request creates or
// updates (author): the user id, negated when the session acts with delegated
// (agent) authority, 0 when unauthenticated. Only for attribution: authz
// lookups and session ownership checks key on the real User.ID and must never
// see the negated form.
func (c Context) AttributionUserID() int64 {
	if c.User == nil {
		return 0
	}
	if c.Delegated {
		return -int64(c.User.ID)
	}
	return int64(c.User.ID)
}

func (c Context) Deadline() (deadline time.Time, ok bool) {
	if c.Ctx == nil {
		return
	}
	return c.Ctx.Deadline()
}

func (c Context) Done() <-chan struct{} {
	if c.Ctx == nil {
		return nil
	}
	return c.Ctx.Done()
}

func (c Context) Err() error {
	if c.Ctx == nil {
		return nil
	}
	return c.Ctx.Err()
}

func (c Context) Value(key any) any {
	if c.Ctx == nil {
		return nil
	}
	return c.Ctx.Value(key)
}
