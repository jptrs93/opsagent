package primary

import (
	"context"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/agentsessions"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nixstores"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/pki"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/scheduledinstances"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/netproxy"
	"github.com/jptrs93/opsagent/backend/app/primary/backup"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/acmeissue"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/assets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"github.com/jptrs93/opsagent/backend/app/primary/netmappublisher"
	"github.com/jptrs93/opsagent/backend/app/primary/scheduler"
	"github.com/jptrs93/opsagent/backend/app/primary/webuihandler"
	"github.com/jptrs93/opsagent/backend/lib/acmestate"
	"github.com/jptrs93/opsagent/backend/lib/engine"
	"github.com/jptrs93/opsagent/backend/lib/engine/configdist"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/nixdocker"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/opendeployrelease"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/engine/runner"
	"github.com/jptrs93/opsagent/backend/lib/engine/versionprovider"
	"github.com/jptrs93/opsagent/backend/lib/metrics"
	"github.com/jptrs93/opsagent/backend/lib/metrics/metricstore"
	"github.com/jptrs93/opsagent/backend/lib/netaudit"
	"github.com/jptrs93/opsagent/backend/lib/network"
	repogit "github.com/jptrs93/opsagent/backend/lib/repo/git"
	githubrepo "github.com/jptrs93/opsagent/backend/lib/repo/github"
	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/util/version"
)

type runtime struct {
	backupStatus          *backup.StatusPublisher
	store                 *state.Service
	assets                *assets.Store
	configService         *systemconfig.Service
	github                githubcredentials.Provider
	gitVersions           *versionprovider.GitVersionProvider
	githubReleaseVersions *versionprovider.GithubReleaseVersionProvider
	secrets               *secrets.Manager
	operator              engine.DeploymentOperator
	acmeHolder            *acmestate.Holder
	acmeIssuer            *acmeissue.Manager
	issuedTLS             *pki.Issuer
	nixDocker             *nixdocker.Preparer
	nixStores             *nixstores.Service
}

func newRuntime() (*runtime, error) {
	dbPath := filepath.Join(ainit.StaticConfig.DataDir, "primary.db")
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("primary is not initialized: %s does not exist", dbPath)
		}
		return nil, fmt.Errorf("checking primary database: %w", err)
	}
	store := state.Open(dbPath)
	secretsMgr, err := secrets.Open(ainit.StaticConfig.DataDir, store)
	if err != nil {
		return nil, err
	}
	configService, err := systemconfig.NewService(store)
	if err != nil {
		return nil, err
	}
	network.Default.SetPrefix(configService.NetworkPrefix())
	assetStore := &assets.Store{
		DB:            store,
		Secrets:       secretsMgr,
		Loader:        configService,
		MigrationWake: configService.AssetMigrationWake(),
		Config: func() *apigen.ClusterSettings {
			snapshot := configService.Snapshot()
			return &snapshot.Settings
		},
	}
	configService.AssetOperationMu = assetStore.AssetOperationLocker()
	configService.ValidateSettingsUpdate = assetStore.ValidateSettingsUpdate
	githubCredentials := secrets.GithubCredentialsProvider{
		Secrets: secretsMgr,
		SecretRef: func(context.Context) apigen.SecretRef {
			return configService.Snapshot().Settings.Repo.GithubToken
		},
	}

	secretProvider := secretsMgr
	configProvider := configdist.NewPrimaryProvider(func(ids []int32) (map[int32]string, error) {
		return values.ResolveConfigs(store.Queries(), ids)
	})
	runtimeInputs := runtimeinputs.New(localAssetProvider{assetStore}, secretProvider, configProvider)
	tlsIssuer := &pki.Issuer{Secrets: secretsMgr}
	runtimeInputs.SetIssuedTLSProvider(&pki.IssuedTLSProvider{
		Issuer: tlsIssuer,
		Snapshot: func() []apigen.DeploymentEvent {
			return deployments.Active(store.Queries(), nil)
		},
	})
	gitManager := repogit.NewManager(ainit.StaticConfig.GitCacheDir, githubCredentials)
	githubClient := githubrepo.NewClient(githubrepo.WithTokenSource(func(ctx context.Context) string {
		creds, err := githubCredentials.LoadCredentials(ctx)
		if err != nil || creds == nil {
			return ""
		}
		return creds.Token
	}))
	nixDocker := nixdocker.New(gitManager)
	nixStores, err := nixstores.New(store, nixDocker.Stores().RequestReset)
	if err != nil {
		return nil, fmt.Errorf("loading nix store resets: %w", err)
	}
	acmeHolder := acmestate.NewHolder()
	acmeIssuer := acmeissue.New(secretsMgr, func() []apigen.DeploymentEvent {
		return deployments.Active(store.Queries(), nil)
	}, store, acmeHolder)

	return &runtime{
		backupStatus:          &backup.StatusPublisher{},
		store:                 store,
		assets:                assetStore,
		configService:         configService,
		github:                githubCredentials,
		gitVersions:           versionprovider.NewGitVersionProvider(gitManager),
		githubReleaseVersions: versionprovider.NewGithubReleaseVersionProvider(githubClient),
		secrets:               secretsMgr,
		operator: engine.DeploymentOperator{
			GithubCredentials: githubCredentials,
			Store:             scheduledinstances.Store{Service: store},
			OpendeployRelease: opendeployrelease.New(ainit.StaticConfig.ReleasesDir, githubClient),
			NixDocker:         nixDocker,
			RuntimeInputs:     runtimeInputs,
		},
		acmeHolder: acmeHolder,
		acmeIssuer: acmeIssuer,
		issuedTLS:  tlsIssuer,
		nixDocker:  nixDocker,
		nixStores:  nixStores,
	}, nil
}

func (r *runtime) webUIHandlerDependencies() webuihandler.Dependencies {
	return webuihandler.Dependencies{
		BackupStatus:          r.backupStatus,
		Store:                 r.store,
		AgentSessions:         agentsessions.New(r.store.Queries()),
		Assets:                r.assets,
		SystemConfig:          r.configService,
		GitVersions:           r.gitVersions,
		GithubReleaseVersions: r.githubReleaseVersions,
		GithubCredentials:     r.github,
		Secrets:               r.secrets,
		NixStores:             r.nixStores,
	}
}

func (r *runtime) start(ctx context.Context, nodeID int32, nodeIdentifier string, networkMaps *netmappublisher.Publisher) {
	deployments.EnsureSystem(r.store, nodeID, version.Version)
	nodes.SetNodeStatusByIdentifier(r.store, nodeIdentifier, true, time.Now())
	nodes.UpdateNodeObservedMeta(r.store, nodeIdentifier, "", version.Version)
	go r.runHostAddressInventory(ctx, nodeIdentifier)
	netproxyCfg := deployments.EnsureNetproxy(r.store, nodeID, version.Version)
	network.Default.SetNetproxyDeploymentID(netproxyCfg.DeploymentID)
	for _, node := range nodes.ListNodes(r.store.Queries()) {
		if node.ID != nodeID {
			deployments.EnsureNetproxy(r.store, node.ID, version.Version)
		}
	}

	predicate := storage.ScheduledInstancePredicate(func(state apigen.ScheduledInstanceState) bool {
		return state.Instance.NodeID == nodeID
	})
	scheduling := scheduler.New(r.store, networkMaps)
	if err := scheduling.Start(ctx); err != nil {
		panic(fmt.Sprintf("start scheduler: %v", err))
	}
	go scheduling.Run(ctx)
	go r.acmeIssuer.Run(ctx)
	netMapSource := netproxy.ClusterNetMapSourceFunc(func() (*apigen.ClusterNetMap, <-chan *apigen.ClusterNetMap, func()) {
		return networkMaps.SnapshotAndSubscribe(nodeID)
	})
	go netproxy.RunNetStateWriter(ctx, r.store, predicate, nodeIdentifier, ainit.StaticConfig.NetproxyStatePath, netproxy.CertSecretResolverFunc(r.secrets.Resolve), r.acmeHolder, netMapSource, nil)
	go netaudit.Run(ctx, network.Default, netaudit.DefaultInterval)
	go r.nixDocker.RunMaintenance(ctx)
	metricstore.Default = metricstore.Start(ctx, ainit.StaticConfig.MetricsDir, nodeID)
	go metrics.Default.Run(ctx, metrics.DefaultInterval, metricstore.Default)
	go func() {
		runner.SweepForeignContainers(ctx, scheduledinstances.Store{Service: r.store}, predicate)
		r.operator.RunAll(predicate)
	}()
}

// runHostAddressInventory keeps the primary's own host address inventory
// current: the set is stored on startup and whenever a poll observes a change,
// mirroring what secondaries report through ClusterHello.
func (r *runtime) runHostAddressInventory(ctx context.Context, nodeIdentifier string) {
	ctx = logu.AddTag(ctx, "HostAddresses")
	var last []string
	haveInventory := false
	for {
		prefix, hasPrefix := network.Default.PrefixValue()
		addrs, err := network.EnumerateHostAddresses(prefix, hasPrefix)
		if err != nil {
			slog.WarnContext(ctx, "enumerating host addresses failed", "err", err)
		} else if current := network.HostAddressStrings(addrs); !haveInventory || !slices.Equal(current, last) {
			for _, node := range nodes.ListNodes(r.store.Queries()) {
				if node.Identifier == nodeIdentifier {
					reported := node.Reported()
					reported.HostAddresses = current
					nodes.ReportNode(r.store, nodeIdentifier, reported)
					break
				}
			}
			last = current
			haveInventory = true
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(network.HostAddressPollInterval):
		}
	}
}

// localAssetProvider narrows the assets package's OpenAsset to the operator's pure
// id-to-stream contract.
type localAssetProvider struct {
	store *assets.Store
}

func (p localAssetProvider) OpenAsset(ctx context.Context, assetVersionID int32) (io.ReadCloser, error) {
	_, body, err := p.store.OpenAsset(ctx, assetVersionID)
	return body, err
}
