package primary

import (
	"context"
	"log/slog"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
)

func watchServerConfig(ctx context.Context, cs *config.Service, initial apigen.Config) error {
	sub := cs.SnapshotAndSubscribe(serverConfigChanged)
	defer sub.UnsubscribeFunc()
	if serverConfigChanged(initial, sub.InitialValue) {
		slog.Info("primary server config changed; restarting")
		return ErrRestartRequired
	}
	select {
	case <-ctx.Done():
		return nil
	case _, ok := <-sub.Ch:
		if !ok {
			return nil
		}
		slog.Info("primary server config changed; restarting")
		return ErrRestartRequired
	}
}

func serverConfigChanged(prev, next apigen.Config) bool {
	return prev.Settings.HttpWeb != next.Settings.HttpWeb ||
		prev.Settings.HttpsWeb != next.Settings.HttpsWeb ||
		prev.Settings.Cluster != next.Settings.Cluster
}
