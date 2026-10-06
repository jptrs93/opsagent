package primary

import (
	"bytes"
	"context"
	"log/slog"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
)

func watchServerConfig(ctx context.Context, cs *systemconfig.Service, initial apigen.SystemConfig) error {
	sub := cs.SnapshotAndSubscribe(serverConfigChanged)
	defer sub.Unsubscribe()
	if serverConfigChanged(initial, sub.InitialValue) {
		slog.InfoContext(ctx, "primary server config changed; restarting")
		return ErrRestartRequired
	}
	select {
	case <-ctx.Done():
		return nil
	case _, ok := <-sub.Ch:
		if !ok {
			return nil
		}
		slog.InfoContext(ctx, "primary server config changed; restarting")
		return ErrRestartRequired
	}
}

func serverConfigChanged(prev, next apigen.SystemConfig) bool {
	return !bytes.Equal(prev.Settings.HttpWeb.Encode(), next.Settings.HttpWeb.Encode()) ||
		!bytes.Equal(prev.Settings.HttpsWeb.Encode(), next.Settings.HttpsWeb.Encode()) ||
		!bytes.Equal(prev.Settings.Cluster.Encode(), next.Settings.Cluster.Encode())
}
