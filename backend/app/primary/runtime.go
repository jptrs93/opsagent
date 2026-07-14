package primary

import (
	"context"
	"path/filepath"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/netproxy"
	"github.com/jptrs93/opsagent/backend/app/primary/webuihandler"
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
	"github.com/jptrs93/opsagent/backend/lib/network"
	repogit "github.com/jptrs93/opsagent/backend/lib/repo/git"
	githubrepo "github.com/jptrs93/opsagent/backend/lib/repo/github"
	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
	"github.com/jptrs93/opsagent/backend/util/version"
)

type runtime struct {
	store                 *sqlite.PrimaryStorage
	assets                *assetstore.Store
	configService         *config.Service
	github                githubcredentials.Provider
	gitVersions           *versionprovider.GitVersionProvider
	githubReleaseVersions *versionprovider.GithubReleaseVersionProvider
	secrets               *secrets.Manager
	operator              engine.DeploymentOperator
}

func newRuntime() (*runtime, error) {
	store := sqlite.NewPrimaryStorage(filepath.Join(ainit.StaticConfig.DataDir, "primary.db"))
	secretsMgr, err := secrets.Open(ainit.StaticConfig.DataDir, store)
	if err != nil {
		return nil, err
	}
	configService, err := config.NewServiceWithInitialConfigHook(store, initialWebTLSCertPEMHook(secretsMgr))
	if err != nil {
		return nil, err
	}
	network.Default.SetPrefix(configService.NetworkPrefix())
	assetStore := &assetstore.Store{
		DB:            store,
		Secrets:       secretsMgr,
		Loader:        configService,
		MigrationWake: configService.AssetMigrationWake(),
		Config: func() *apigen.Settings {
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
	runtimeInputs := runtimeinputs.New(assetStore, secretProvider, configProvider)
	gitManager := repogit.NewManager(ainit.StaticConfig.GitCacheDir, githubCredentials)
	githubClient := githubrepo.NewClient(githubCredentials)

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
			GithubRelease:      githubrelease.New(ainit.StaticConfig.ReleasesDir, githubClient, githubCredentials),
			NixDocker:          nixdocker.New(gitManager),
			GithubReleaseImage: githubreleaseimage.New(ainit.StaticConfig.ReleasesDir, githubClient),
			RuntimeInputs:      runtimeInputs,
		},
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
	}
}

func (r *runtime) start(ctx context.Context, machineName string) {
	r.store.EnsureSystemDeployment(machineName, version.Version)
	r.store.EnsurePrimaryNode(machineName)
	r.store.SetNodeStatusByName(machineName, true, time.Now())
	netproxyCfg := r.store.EnsureNetproxyDeployment(machineName, version.Version)
	network.Default.SetNetproxyDeploymentID(netproxyCfg.ID)

	go netproxy.RunNetStateWriter(ctx, r.store, machineName, ainit.StaticConfig.NetproxyStatePath)
	go r.operator.RunAll(machineName)
}
