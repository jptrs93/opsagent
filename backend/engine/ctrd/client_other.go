//go:build !linux

package ctrd

import (
	"context"
	"syscall"
)

// Client is the non-linux stub. Every operation returns ErrUnsupported.
type Client struct{}

func Connect(address, namespace string) *Client { return &Client{} }

func (c *Client) Supported() bool { return false }

func (c *Client) Pull(ctx context.Context, ref string) (string, error) { return "", ErrUnsupported }

func (c *Client) Import(ctx context.Context, image ImageStream) (string, error) {
	return "", ErrUnsupported
}

func (c *Client) RunTask(ctx context.Context, spec ContainerSpec) (*Task, error) {
	return nil, ErrUnsupported
}

func (c *Client) LoadTask(ctx context.Context, id string) (*Task, error) { return nil, ErrUnsupported }

// Task is the non-linux stub.
type Task struct{}

func (t *Task) Pid() uint32 { return 0 }

func (t *Task) Wait(ctx context.Context) (<-chan ExitStatus, error) { return nil, ErrUnsupported }

func (t *Task) Kill(ctx context.Context, sig syscall.Signal) error { return ErrUnsupported }

func (t *Task) Delete(ctx context.Context) error { return ErrUnsupported }
