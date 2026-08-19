package primary

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/netproxy"
	"github.com/jptrs93/opsagent/backend/app/primary/netmappublisher"
	"github.com/jptrs93/opsagent/backend/app/primary/scheduler"
	"github.com/jptrs93/opsagent/backend/app/primary/webuihandler"
	"github.com/jptrs93/opsagent/backend/lib/acmeissue"
	"github.com/jptrs93/opsagent/backend/lib/acmestate"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/engine"
	"github.com/jptrs93/opsagent/backend/lib/engine/assetstore"
	"github.com/jptrs93/opsagent/backend/lib/engine/configdist"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/githubrelease"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/githubreleaseimage"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/nixdocker"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/engine/secretdist"
	"github.com/jptrs93/opsagent/backend/lib/engine/versionprovider"
	"github.com/jptrs93/opsagent/backend/lib/issuedtls"
	"github.com/jptrs93/opsagent/backend/lib/netaudit"
	"github.com/jptrs93/opsagent/backend/lib/network"
	repogit "github.com/jptrs93/opsagent/backend/lib/repo/git"
	githubrepo "github.com/jptrs93/opsagent/backend/lib/repo/github"
	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/util/version"
)

type runtime struct {
	store                 *state.Service
	assets                *assetstore.Store
	configService         *config.Service
	github                githubcredentials.Provider
	gitVersions           *versionprovider.GitVersionProvider
	githubReleaseVersions *versionprovider.GithubReleaseVersionProvider
	secrets               *secrets.Manager
	operator              engine.DeploymentOperator
	acmeHolder            *acmestate.Holder
	acmeIssuer            *acmeissue.Manager
	issuedTLS             *issuedtls.Issuer
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
	configService, err := config.NewService(store)
	if err != nil {
		return nil, err
	}
	network.Default.SetPrefix(configService.NetworkPrefix())
	assetStore := &assetstore.Store{
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
	githubCredentials := githubcredentials.SecretProvider{
		Secrets: secretsMgr,
		SecretRef: func(context.Context) apigen.SecretRef {
			return configService.Snapshot().Settings.Repo.GithubToken
		},
	}

	secretProvider := secretdist.NewPrimaryProvider(secretsMgr)
	configProvider := configdist.NewPrimaryProvider(store)
	runtimeInputs := runtimeinputs.New(localAssetProvider{assetStore}, secretProvider, configProvider)
	tlsIssuer := &issuedtls.Issuer{Secrets: secretsMgr}
	runtimeInputs.SetIssuedTLSProvider(&issuedtls.PrimaryProvider{
		Issuer: tlsIssuer,
		Snapshot: func() []apigen.DeploymentConfig {
			return store.FetchDeploymentSnapshot(nil)
		},
	})
	gitManager := repogit.NewManager(ainit.StaticConfig.GitCacheDir, githubCredentials)
	githubClient := githubrepo.NewClient(githubCredentials)
	acmeHolder := acmestate.NewHolder()
	acmeIssuer := acmeissue.New(secretsMgr, func() []apigen.DeploymentConfig {
		return store.FetchDeploymentSnapshot(nil)
	}, acmeHolder)

	return &runtime{
		store:                 store,
		assets:                assetStore,
		configService:         configService,
		github:                githubCredentials,
		gitVersions:           versionprovider.NewGitVersionProvider(gitManager),
		githubReleaseVersions: versionprovider.NewGithubReleaseVersionProviderWithClient(githubClient),
		secrets:               secretsMgr,
		operator: engine.DeploymentOperator{
			Store:              store,
			GithubRelease:      githubrelease.New(ainit.StaticConfig.ReleasesDir, githubClient),
			NixDocker:          nixdocker.New(gitManager),
			GithubReleaseImage: githubreleaseimage.New(ainit.StaticConfig.ReleasesDir, githubClient),
			RuntimeInputs:      runtimeInputs,
		},
		acmeHolder: acmeHolder,
		acmeIssuer: acmeIssuer,
		issuedTLS:  tlsIssuer,
	}, nil
}

func (r *runtime) webUIHandlerDependencies() webuihandler.Dependencies {
	return webuihandler.Dependencies{
		Store:                 r.store,
		Assets:                r.assets,
		ConfigService:         r.configService,
		Github:                r.github,
		GitVersions:           r.gitVersions,
		GithubReleaseVersions: r.githubReleaseVersions,
		Secrets:               r.secrets,
		AcmeWake:              r.acmeIssuer.Wake,
	}
}

func (r *runtime) start(ctx context.Context, nodeID int32, nodeIdentifier string, networkMaps *netmappublisher.Publisher) {
	r.store.EnsureSystemDeployment(nodeID, version.Version)
	r.store.SetNodeStatusByIdentifier(nodeIdentifier, true, time.Now())
	netproxyCfg := r.store.EnsureNetproxyDeployment(nodeID, version.Version)
	network.Default.SetNetproxyDeploymentID(netproxyCfg.ID)
	for _, node := range r.store.ListNodes() {
		if node.ID != nodeID {
			r.store.EnsureNetproxyDeployment(node.ID, version.Version)
		}
	}

	predicate := storage.ScheduledInstancePredicate(func(state apigen.ScheduledInstanceState) bool {
		return state.Instance.NodeID == nodeID
	})
	go scheduler.New(r.store, networkMaps).Run()
	go r.acmeIssuer.Run(ctx)
	go netproxy.RunNetStateWriter(ctx, r.store, predicate, nodeIdentifier, ainit.StaticConfig.NetproxyStatePath, netproxy.CertSecretResolverFunc(r.secrets.Resolve), r.acmeHolder, nil)
	go netaudit.Run(ctx, network.Default, netaudit.DefaultInterval)
	go r.operator.RunAll(predicate)
}

// localAssetProvider narrows assetstore's OpenAsset to the operator's pure
// id-to-stream contract.
type localAssetProvider struct {
	store *assetstore.Store
}

func (p localAssetProvider) OpenAsset(ctx context.Context, assetVersionID int32) (io.ReadCloser, error) {
	_, body, err := p.store.OpenAsset(ctx, assetVersionID)
	return body, err
}
