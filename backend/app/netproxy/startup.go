package netproxy

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/app/netproxy/netstatewatch"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

const (
	operationalWarningInterval = 30 * time.Second
	netproxyDNSPort            = int32(53)
)

// Run starts the netproxy DNS and ingress services using process configuration.
func Run(ctx context.Context) error {
	statePath := ainit.StaticConfig.NetproxyStatePath
	listen := os.Getenv("OPENDEPLOY_NETPROXY_DNS_LISTEN")
	if listen == "" {
		listen = ":53"
	}
	slog.Info("starting opendeploy-net", "state_path", statePath, "dns_listen", listen)
	states := netstatewatch.New(statePath)
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return states.Run(ctx) })
	g.Go(func() error { return RunDNS(ctx, states, listen) })
	g.Go(func() error { return RunTLSIngress(ctx, states) })
	return g.Wait()
}

func newOperationalWarningLimiter() *rate.Limiter {
	return rate.NewLimiter(rate.Every(operationalWarningInterval), 3)
}

func allowOperationalWarning(limiter *rate.Limiter) bool {
	return limiter == nil || limiter.Allow()
}
