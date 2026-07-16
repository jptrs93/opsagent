package netproxy

import (
	"context"
	"os"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/app/netproxy/netstatewatch"
	"golang.org/x/sync/errgroup"
)

// Run starts the netproxy DNS and ingress services using process configuration.
func Run(ctx context.Context) error {
	statePath := ainit.StaticConfig.NetproxyStatePath
	listen := os.Getenv("OPENDEPLOY_NETPROXY_DNS_LISTEN")
	if listen == "" {
		listen = ":53"
	}
	states := netstatewatch.New(statePath)
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return states.Run(ctx) })
	g.Go(func() error { return RunDNS(ctx, states, listen) })
	g.Go(func() error { return RunTLSIngress(ctx, states) })
	return g.Wait()
}
