package clusterserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/clusterhandler"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

const primaryServerShutdownTimeout = 20 * time.Second

func RunPrimary(
	ctx context.Context,
	h apigen.OpsagentClusterV1Handler,
	loader config.Loader,
	material *certu.Material,
	listenSetting apigen.StringSetting,
) error {
	tlsCfg := certu.MustLoadTLSConfigFromPEM(material.CACert, material.PrimaryCert, material.PrimaryKey)
	listen := loader.MustLoadConfigStringValue(listenSetting)

	// The cluster transport is a separate mTLS HTTP/2-only listener; peer
	// identity comes from the client cert CN. The server emits health-check PINGs
	// so it detects a dead worker.
	clusterMux := apigen.CreateOpsagentClusterV1Mux(h, &apigen.MuxConfig{
		VerifyAuth:         clusterhandler.VerifyClusterPeer,
		MaxRequestBodySize: 16 * 1024 * 1024, // 16 MB cap on a single inbound stream frame
	})
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	srv := &http.Server{
		Addr:        listen,
		Handler:     clusterMux,
		TLSConfig:   tlsCfg,
		Protocols:   protocols,
		BaseContext: primaryServerBaseContext(ctx),
		HTTP2: &http.HTTP2Config{
			SendPingTimeout: 5 * time.Second,  // PING a silent worker after 5s idle
			PingTimeout:     10 * time.Second, // tear down if no ACK within 10s (~15s total to detect a dead worker)
		},
	}
	slog.Info(fmt.Sprintf("starting primary cluster addr=%v", listen))
	return runManagedPrimaryServer(ctx, "cluster", srv, func() error {
		return srv.ListenAndServeTLS("", "")
	})
}

func RunEnrollment(
	ctx context.Context,
	h apigen.EnrollmentV1Handler,
	verifyAuth apigen.VerifyAuthFunc,
	loader config.Loader,
	material *certu.Material,
	listenSetting apigen.StringSetting,
	middlewares ...apigen.MiddlewareFunc,
) error {
	listen := loader.MustLoadConfigStringValue(listenSetting)
	streamMiddlewares := []apigen.MiddlewareFunc{
		func(next apigen.HandlerFunc) apigen.HandlerFunc {
			return func(requestCtx context.Context, w http.ResponseWriter, r *http.Request) {
				_ = http.NewResponseController(w).EnableFullDuplex()
				next(requestCtx, w, r)
			}
		},
	}
	streamMiddlewares = append(streamMiddlewares, middlewares...)
	mux := apigen.CreateEnrollmentV1Mux(h, &apigen.MuxConfig{
		VerifyAuth:         verifyAuth,
		MaxRequestBodySize: 1 * 1024 * 1024,
		Middlewares:        streamMiddlewares,
	})
	srv := &http.Server{
		Addr:        listen,
		Handler:     mux,
		TLSConfig:   certu.MustLoadServerTLSConfigFromPEM(material.PrimaryCert, material.PrimaryKey),
		BaseContext: primaryServerBaseContext(ctx),
	}
	slog.Info(fmt.Sprintf("starting primary enrollment addr=%v", listen))
	return runManagedPrimaryServer(ctx, "enrollment", srv, func() error {
		return srv.ListenAndServeTLS("", "")
	})
}

func runManagedPrimaryServer(ctx context.Context, name string, srv *http.Server, serve func() error) error {
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serve()
	}()

	select {
	case err := <-serveDone:
		return managedPrimaryServerResult(ctx, name, err)
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), primaryServerShutdownTimeout)
		defer shutdownCancel()
		slog.Info(fmt.Sprintf("stopping primary server server=%v", name))
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Warn(fmt.Sprintf("primary server graceful shutdown failed; closing server=%v", name), "err", err)
			_ = srv.Close()
		}
		if err := managedPrimaryServerResult(ctx, name, <-serveDone); err != nil {
			slog.Warn(fmt.Sprintf("primary server ended during shutdown server=%v", name), "err", err)
		}
		return nil
	}
}

func managedPrimaryServerResult(ctx context.Context, name string, err error) error {
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s server ended: %w", name, err)
	}
	if ctx.Err() == nil {
		return fmt.Errorf("%s server ended unexpectedly", name)
	}
	return nil
}

func primaryServerBaseContext(ctx context.Context) func(net.Listener) context.Context {
	return func(net.Listener) context.Context { return ctx }
}
