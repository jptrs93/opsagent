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
	"github.com/jptrs93/opsagent/backend/app/primary/netmappublisher"
	"github.com/jptrs93/opsagent/backend/app/primary/webui"
	"github.com/jptrs93/opsagent/backend/app/primary/webuihandler"
	"github.com/jptrs93/opsagent/backend/lib/middleware/clientaddr"
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
	primaryRuntime, err := newRuntime()
	if err != nil {
		return fmt.Errorf("creating primary runtime: %w", err)
	}
	primaryRuntime.assets.BeforeLocalMigration = backup.StopReplicationForAssetMigration
	clusterMaterial, err := certu.LoadPrimary(primaryRuntime.secrets)
	if err != nil {
		return fmt.Errorf("loading cluster TLS material: %w", err)
	}
	certificateIdentifier := certu.MustCertCommonNameFromPEM(clusterMaterial.PrimaryCert)
	primaryNode := primaryRuntime.store.EnsurePrimaryNode("primary", certificateIdentifier)
	initialConfig := primaryRuntime.configService.Snapshot()
	underlayAddress := ainit.StaticConfig.UnderlayAddress
	if underlayAddress == "" {
		clusterListen := primaryRuntime.configService.MustLoadConfigStringValue(initialConfig.Settings.Cluster.Listen)
		underlayAddress, err = resolvePrimaryUnderlayAddress(clusterListen)
		if err != nil {
			return err
		}
	}
	primaryNode = primaryRuntime.store.MustSetNodeAddresses(primaryNode.ID, []string{underlayAddress})
	nodeIdentifier := primaryNode.Identifier
	slog.Info(fmt.Sprintf("opendeploy starting primary version=%v nodeIdentifier=%v", version.Version, nodeIdentifier))
	webUIHandler, err := webuihandler.New(staticFS, primaryNode.ID, primaryRuntime.webUIHandlerDependencies())
	if err != nil {
		return fmt.Errorf("creating web UI handler: %w", err)
	}
	// The publisher is created before the runtime starts: the scheduler waits on
	// its applied-sequence barrier before retiring a drained placement, so it
	// cannot be started without one.
	networkMaps, err := netmappublisher.New(primaryRuntime.store, primaryRuntime.configService.NetworkPrefix())
	if err != nil {
		return fmt.Errorf("creating network map publisher: %w", err)
	}
	go networkMaps.Run(ctx)
	primaryRuntime.start(ctx, primaryNode.ID, nodeIdentifier, networkMaps)
	assetReconcileDone := primaryRuntime.assets.StartReconciler(ctx)
	backupDone := backup.StartReplication(ctx, primaryRuntime.configService, primaryRuntime.secrets, primaryRuntime.store, primaryRuntime.assets)
	defer func() {
		cancel()
		<-backupDone
		<-assetReconcileDone
	}()
	enrollmentFingerprint, err := certu.CertificatePEMSPKISHA256(clusterMaterial.PrimaryCert)
	if err != nil {
		return fmt.Errorf("computing enrollment TLS fingerprint: %w", err)
	}
	// Primary cluster and enrollment listeners start for every primary.
	clusterHandler := clusterhandler.New(primaryRuntime.store, primaryRuntime.assets, primaryRuntime.github, primaryRuntime.secrets, primaryRuntime.configService.NetworkPrefix(), networkMaps, primaryRuntime.acmeHolder)
	enrollmentHandler := enrollmenthandler.New(primaryRuntime.store, primaryRuntime.secrets, primaryRuntime.configService, enrollmentFingerprint, networkMaps)
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
		clientaddr.Middleware(),
		ratelimit.PerIP(rate.Limit(40), 100, time.Minute),
		ratelimit.PerIPAndPrefix("/v1/auth", rate.Limit(1), 10, time.Minute),
		ratelimit.PerIPAndPrefix("/v1/auth/master", rate.Limit(0.2), 10, time.Minute),
		// The agent-session family is reachable without a credential. The broad
		// budget accommodates a 5s get-session poll while an operator decides;
		// the tight one on request-start is what stops a single address from
		// repeatedly occupying an operator's only open request slot. Nested
		// prefixes stack, as with /v1/auth above.
		ratelimit.PerIPAndPrefix("/v1/agent-sessions", rate.Limit(2), 30, time.Minute),
		ratelimit.PerIPAndPrefix("/v1/agent-sessions/instructions", rate.Limit(0.2), 5, time.Minute),
		ratelimit.PerIPAndPrefix("/v1/agent-sessions/request-start", rate.Limit(0.05), 3, 10*time.Minute),
		// Secret generation writes a row nothing ever collects, and the caller
		// cannot read back what it made, so a retry loop here is both plausible
		// and silent. A burst of 10 refilling at 6/min covers wiring up a
		// deployment's credentials in one go while capping a loop's damage.
		ratelimit.PerIPAndPrefix("/v1/secrets/generate", rate.Limit(0.1), 10, time.Minute),
	}
	m := apigen.CreateApiServerMux(webUIHandler, &apigen.MuxConfig{
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
