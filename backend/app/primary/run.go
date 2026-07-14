package primary

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/backup"
	"github.com/jptrs93/opsagent/backend/app/primary/clusterhandler"
	"github.com/jptrs93/opsagent/backend/app/primary/clusterserver"
	"github.com/jptrs93/opsagent/backend/app/primary/enrollmenthandler"
	"github.com/jptrs93/opsagent/backend/app/primary/webui"
	"github.com/jptrs93/opsagent/backend/app/primary/webuihandler"
	"github.com/jptrs93/opsagent/backend/lib/middleware/ratelimit"
	"github.com/jptrs93/opsagent/backend/util/certu"
	"github.com/jptrs93/opsagent/backend/util/version"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

// ErrRestartRequired signals that listener configuration changed and the
// process supervisor should restart OpenDeploy.
var ErrRestartRequired = errors.New("primary restart required")

// Run starts all primary services and blocks until shutdown or failure.
func Run(parentCtx context.Context, embeddedFS fs.FS) error {
	lifecycleCtx, cancel := context.WithCancel(parentCtx)
	defer cancel()
	g, ctx := errgroup.WithContext(lifecycleCtx)
	staticFS, err := fs.Sub(embeddedFS, "web/dist")
	if err != nil {
		return fmt.Errorf("creating embedded sub fs: %w", err)
	}
	machineName := ainit.StaticConfig.PrimaryName
	slog.Info(fmt.Sprintf("opendeploy starting primary version=%v machine=%v", version.Version, machineName))
	primaryRuntime, err := newRuntime()
	if err != nil {
		return fmt.Errorf("creating primary runtime: %w", err)
	}
	primaryRuntime.assets.BeforeLocalMigration = backup.StopReplicationForAssetMigration
	webUIHandler, err := webuihandler.New(staticFS, machineName, primaryRuntime.webUIHandlerDependencies())
	if err != nil {
		return fmt.Errorf("creating web UI handler: %w", err)
	}
	primaryRuntime.start(ctx, machineName)
	assetReconcileDone := primaryRuntime.assets.StartReconciler(ctx)
	backupDone := backup.StartReplication(ctx, primaryRuntime.configService, primaryRuntime.secrets, primaryRuntime.store, primaryRuntime.assets)
	defer func() {
		cancel()
		<-backupDone
		<-assetReconcileDone
	}()
	initialConfig := primaryRuntime.configService.Snapshot()
	clusterMaterial, err := certu.BootstrapPrimary(primaryRuntime.secrets, machineName)
	if err != nil {
		return fmt.Errorf("bootstrapping cluster TLS material: %w", err)
	}
	enrollmentFingerprint, err := certu.CertificatePEMSPKISHA256(clusterMaterial.PrimaryCert)
	if err != nil {
		return fmt.Errorf("computing enrollment TLS fingerprint: %w", err)
	}
	// Primary cluster and enrollment listeners start for every primary.
	clusterHandler := clusterhandler.New(primaryRuntime.store, primaryRuntime.assets, primaryRuntime.github, primaryRuntime.secrets, primaryRuntime.configService.NetworkPrefix())
	enrollmentHandler := enrollmenthandler.New(primaryRuntime.store, primaryRuntime.secrets, primaryRuntime.configService, enrollmentFingerprint)
	webUIHandler.Cluster = clusterHandler
	webUIHandler.Enrollment = enrollmentHandler
	enrollmentMiddlewares := []apigen.MiddlewareFunc{
		ratelimit.PerIP(rate.Limit(0.2), 5, time.Minute),
	}
	g.Go(func() error {
		return clusterserver.RunPrimary(ctx, clusterHandler, primaryRuntime.configService, clusterMaterial, initialConfig.Settings.Cluster.Listen)
	})
	g.Go(func() error {
		return clusterserver.RunEnrollment(ctx, enrollmentHandler, enrollmentHandler.VerifyEnrollmentRequest, primaryRuntime.configService, clusterMaterial, initialConfig.Settings.Cluster.EnrollmentListen, enrollmentMiddlewares...)
	})
	middlewares := []apigen.MiddlewareFunc{
		ratelimit.PerIP(rate.Limit(40), 100, time.Minute),
		ratelimit.PerIPAndPrefix("/v1/auth", rate.Limit(1), 10, time.Minute),
		ratelimit.PerIPAndPrefix("/v1/auth/master", rate.Limit(0.2), 10, time.Minute),
	}
	m := apigen.CreateOpsagentHttpV1Mux(webUIHandler, &apigen.MuxConfig{
		VerifyAuth:         webUIHandler.VerifyAuth,
		MaxRequestBodySize: 20_000_000,
		Middlewares:        middlewares,
	})
	g.Go(func() error { return webui.RunPrimaryHTTPWebUI(ctx, primaryRuntime.configService, m) })
	g.Go(func() error {
		return webui.RunPrimaryHTTPSWebUI(ctx, primaryRuntime.configService, primaryRuntime.secrets, m)
	})
	g.Go(func() error { return watchServerConfig(ctx, primaryRuntime.configService, initialConfig) })
	err1 := g.Wait()
	err2 := parentCtx.Err()
	return errors.Join(err1, err2)
}
