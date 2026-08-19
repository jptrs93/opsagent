//go:build !linux

package netaudit

import (
	"context"

	"github.com/jptrs93/opsagent/backend/lib/network"
)

const supported = false

func auditOnce(context.Context, *network.Manager) {}
