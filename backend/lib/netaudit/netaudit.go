// Package netaudit periodically compares the kernel networking state the
// network manager believes it has installed — nftables DNAT and masquerade
// rules plus /128 workload routes — against what the kernel actually holds,
// and logs the result. It is strictly observational: it detects and reports
// divergence but never repairs it, so its log lines are trustworthy evidence
// of what the kernel held at a point in time.
package netaudit

import (
	"context"
	"time"

	"github.com/jptrs93/opsagent/backend/lib/network"
)

const DefaultInterval = 60 * time.Second

// recheckDelay separates the two passes of a divergence check. Container
// starts, rollovers, and teardowns legitimately change kernel state between
// the manager snapshot and the kernel read, so a divergence is only reported
// after it survives a full re-audit.
const recheckDelay = 2 * time.Second

// Run audits every interval until ctx is cancelled. No-op on platforms
// without the kernel dataplane.
func Run(ctx context.Context, m *network.Manager, interval time.Duration) {
	if !supported {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			auditOnce(ctx, m)
		}
	}
}
