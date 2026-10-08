package primary

import (
	"context"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/pki"
	"io/fs"
	"log/slog"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/backup"
	"github.com/jptrs93/opsagent/backend/app/primary/clusterhandler"
	"github.com/jptrs93/opsagent/backend/app/primary/clusterserver"
	"github.com/jptrs93/opsagent/backend/app/primary/enrollmenthandler"
	"github.com/jptrs93/opsagent/backend/app/primary/nodepublisher"
	"github.com/jptrs93/opsagent/backend/app/primary/webuihandler"
	"github.com/jptrs93/opsagent/backend/app/primarybootstrap"
	"github.com/jptrs93/opsagent/backend/lib/log/logmanager"
	"github.com/jptrs93/opsagent/backend/lib/metrics/metricstore"
	"github.com/jptrs93/opsagent/backend/lib/middleware/clientaddr"
	"github.com/jptrs93/opsagent/backend/lib/middleware/ratelimit"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/lib/runtimebin"
	"github.com/jptrs93/opsagent/backend/lib/wgkey"
	"github.com/jptrs93/opsagent/backend/util/certu"
	"github.com/jptrs93/opsagent/backend/util/version"
	"github.com/klauspost/compress/gzhttp"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

// ErrRestartRequired signals that listener configuration changed and the
// process supervisor should restart OpenDeploy.
var ErrRestartRequired = errors.New("primary restart required")

func Run(parentCtx context.Context, embeddedFS fs.FS) error {
	parentCtx = logu.AddTag(parentCtx, "Primary")
	runtimebin.ReconcileAtStartup(parentCtx)
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
	clusterMaterial, err := pki.LoadPrimary(primaryRuntime.secrets)
	if err != nil {
		return fmt.Errorf("loading cluster TLS material: %w", err)
	}
	certificateIdentifier := certu.MustCertCommonNameFromPEM(clusterMaterial.PrimaryCert)
	initialConfig := primaryRuntime.configService.Snapshot()
	clusterListen := primaryRuntime.configService.MustLoadStringSetting(initialConfig.Settings.Cluster.Listen)
	underlayAddress, err := primarybootstrap.ResolvePrimaryUnderlayAddress(ainit.StaticConfig.UnderlayAddress, clusterListen)
	if err != nil {
		return err
	}
	if _, err := nodes.NormalizeNodeUnderlay(primaryRuntime.store.Queries(), certificateIdentifier, underlayAddress.String()); err != nil {
		return fmt.Errorf("primary underlay address %s: %w", underlayAddress, err)
	}
	// The primary's WireGuard key follows the same custody rule as secondaries:
	// generated locally, private key only ever in the data directory, public
	// key registered on the node row. It is loaded before the row is written
	// so no render ever sees a keyless member; WireGuard is the only cross-node
	// transport, so a key failure blocks boot.
	nodeKey, err := wgkey.LoadOrGenerate(ainit.StaticConfig.DataDir)
	if err != nil {
		return fmt.Errorf("loading WireGuard node key: %w", err)
	}
	network.Default.SetWGPrivateKey(nodeKey.Private)
	primaryNode := nodes.EnsurePrimaryNode(primaryRuntime.store, "primary", certificateIdentifier, underlayAddress, nodeKey.PublicBase64())
	nodeIdentifier := primaryNode.Identifier
	slog.InfoContext(ctx, fmt.Sprintf("opendeploy starting primary version=%v nodeIdentifier=%v", version.Version, nodeIdentifier))
	webUIHandler, err := webuihandler.New(staticFS, primaryNode.ID, primaryRuntime.webUIHandlerDependencies())
	if err != nil {
		return fmt.Errorf("creating web UI handler: %w", err)
	}
	// The publisher is created before the runtime starts: the scheduler waits on
	// its applied-sequence barrier before retiring a drained placement, so it
	// cannot be started without one.
	projection, err := nodepublisher.New(primaryRuntime.store, primaryRuntime.configService.NetworkPrefix(), primaryRuntime.acmeHolder)
	if err != nil {
		return fmt.Errorf("creating node publisher: %w", err)
	}
	go projection.Run(ctx)
	if network.TopologySupported {
		go newNetMapApplier(primaryNode.ID, primaryRuntime.configService.NetworkPrefix(), projection).run(ctx)
	}
	primaryRuntime.start(ctx, primaryNode.ID, nodeIdentifier, projection)
	assetReconcileDone := primaryRuntime.assets.StartReconciler(ctx)
	backupDone := backup.StartReplication(ctx, primaryRuntime.configService, primaryRuntime.secrets, primaryRuntime.backupStatus, primaryRuntime.assets)
	defer func() {
		cancel()
		<-backupDone
		<-assetReconcileDone
	}()
	enrollmentFingerprint, err := certu.CertificatePEMSPKISHA256(clusterMaterial.PrimaryCert)
	if err != nil {
		return fmt.Errorf("computing enrollment TLS fingerprint: %w", err)
	}
	clusterHandler := clusterhandler.New(primaryRuntime.store, primaryRuntime.assets, primaryRuntime.github, primaryRuntime.secrets, primaryRuntime.configService.NetworkPrefix(), projection, primaryRuntime.acmeHolder, primaryRuntime.nixStores, primaryRuntime.issuedTLS)
	enrollmentHandler := enrollmenthandler.New(primaryRuntime.store, primaryRuntime.secrets, primaryRuntime.configService, enrollmentFingerprint, projection)
	go clusterHandler.RunEvictionWatch(ctx)
	webUIHandler.Cluster = clusterHandler
	webUIHandler.IngressDiagnostics = projection
	webUIHandler.LogManager = logmanager.StartManager(ctx, primaryRuntime.store, func(state apigen.ScheduledInstanceState) bool {
		return state.Instance.NodeID == primaryNode.ID
	})
	webUIHandler.Metrics = metricstore.Default
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
		ratelimit.PerIPAndPrefix("/v1/auth/password/login", rate.Limit(0.2), 10, time.Minute),
		// The agent-session family is reachable without a credential.
		ratelimit.PerIPAndPrefix("/v1/agent-sessions", rate.Limit(2), 30, time.Minute),
		ratelimit.PerIPAndPrefix("/v1/agent-sessions/instructions", rate.Limit(0.2), 5, time.Minute),
		ratelimit.PerIPAndPrefix("/v1/agent-sessions/request-start", rate.Limit(0.05), 3, 10*time.Minute),
		ratelimit.PerIPAndPrefix("/v1/secrets/generate", rate.Limit(0.1), 10, time.Minute),
	}
	m := apigen.CreateApiServerMux(webUIHandler, &apigen.MuxConfig{
		VerifyAuth:         webUIHandler.VerifyAuth,
		MaxRequestBodySize: 20_000_000,
		UnaryCompression:   erru.Must(gzhttp.NewWrapper(gzhttp.MinSize(2048))),
		StreamCompression:  erru.Must(gzhttp.NewWrapper(gzhttp.MinSize(0))),
		Middlewares:        middlewares,
	})
	g.Go(func() error { return webUIHandler.WebUI.RunHTTP(ctx, m) })
	g.Go(func() error { return webUIHandler.WebUI.RunHTTPS(ctx, m) })
	g.Go(func() error { return watchServerConfig(ctx, primaryRuntime.configService, initialConfig) })
	err1 := g.Wait()
	err2 := parentCtx.Err()
	return errors.Join(err1, err2)
}
