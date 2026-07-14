package netproxy

import (
	"context"
	"os"

	"github.com/jptrs93/opsagent/backend/ainit"
)

// Run starts the netproxy DNS service using process configuration.
func Run(ctx context.Context) error {
	statePath := ainit.StaticConfig.NetproxyStatePath
	listen := os.Getenv("OPENDEPLOY_NETPROXY_DNS_LISTEN")
	if listen == "" {
		listen = ":53"
	}
	return RunDNS(ctx, statePath, listen)
}
