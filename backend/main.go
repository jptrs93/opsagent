package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/app/installer"
	"github.com/jptrs93/opsagent/backend/app/logconsumer"
	"github.com/jptrs93/opsagent/backend/app/netproxy"
	"github.com/jptrs93/opsagent/backend/app/primary"
	"github.com/jptrs93/opsagent/backend/app/secondary"
)

//go:generate sh -c "cd ../frontend && pnpm install && pnpm run build"
//go:embed web/dist
var fsys embed.FS

func main() {
	switch ainit.Args.Command {
	case ainit.CommandInstall, ainit.CommandUninstall:
		if err := installer.Run(os.Args); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
			os.Exit(1)
		}
	case ainit.CommandRawLogConsumer:
		if err := logconsumer.RunRawBinaryProcess(os.Args); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
			os.Exit(1)
		}
	case ainit.CommandNetproxy:
		// Let the DNS servers stop cleanly when the netproxy process is terminated.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err := netproxy.Run(ctx)
		if err != nil {
			slog.Error("opendeploy net stopped with error", "err", err)
			stop() // Clean up before os.Exit, which skips deferred calls.
			os.Exit(1)
		}
		slog.Info("opendeploy net stopped")
	case ainit.CommandPrimary:
		// Stop the primary's web, cluster, enrollment, backup, and config-watcher work.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err := primary.Run(ctx, fsys)
		if err != nil {
			if errors.Is(err, primary.ErrRestartRequired) {
				slog.Info("opendeploy primary stopped for restart", "err", err)
			} else {
				slog.Error("opendeploy primary stopped with error", "err", err)
			}
			stop() // Clean up before os.Exit, which skips deferred calls.
			os.Exit(1)
		}
	case ainit.CommandSecondary:
		// Stop the secondary's cluster session and netstate writer on termination.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		secondary.Run(ctx)
	default:
		panic(fmt.Sprintf("unsupported command after argument parsing: %s", ainit.Args.Command))
	}
}
