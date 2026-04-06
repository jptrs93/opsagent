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
