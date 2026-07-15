package secondary

import (
	"context"
	"crypto/tls"
	"net/http"
	"path/filepath"
	"time"

	"github.com/jptrs93/opsagent/backend/app/netproxy"
	"github.com/jptrs93/opsagent/backend/lib/engine"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/githubrelease"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/githubreleaseimage"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/nixdocker"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/lib/repo/git"
	githubrepo "github.com/jptrs93/opsagent/backend/lib/repo/github"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

type runtimeConfig struct {
	TLS                *tls.Config
	PrimaryClusterAddr string
	PrimaryName        string // primary certificate DNS/IP SAN for TLS server name verification
	MachineName        string
	DataDir            string
	GitCacheDir        string
	ReleasesDir        string
	NetproxyStatePath  string
	ClusterPrefix      network.Prefix
	NetDeploymentID    int32
}

// Run boots the local store, starts the deployment operator, and then maintains
// a persistent connection to the primary. It intentionally runs forever; fatal
// failures should panic and let the service manager restart the process.
func run(ctx context.Context, cfg runtimeConfig) {

	store := sqlite.NewSecondaryStorage(filepath.Join(cfg.DataDir, "secondary.db"))
	primaryHTTPClient := newPrimaryHTTPClient(cfg.TLS, cfg.PrimaryName)
	primaryURL := "https://" + cfg.PrimaryClusterAddr
	githubCredentials := NewPrimaryGithubCredentialsProvider(primaryURL, primaryHTTPClient)
	network.SetDefault(network.New(cfg.ClusterPrefix, cfg.NetDeploymentID))

	assetProvider := NewPrimaryAssetProvider(primaryURL, primaryHTTPClient)
	secretProvider := NewPrimarySecretProvider(primaryURL, primaryHTTPClient)
	configProvider := NewPrimaryConfigProvider(primaryURL, primaryHTTPClient)
	runtimeInputs := runtimeinputs.New(assetProvider, secretProvider, configProvider)
	gitManager := git.NewManager(cfg.GitCacheDir, githubCredentials)
	githubClient := githubrepo.NewClient(githubCredentials)
	githubReleasePreparer := githubrelease.New(cfg.ReleasesDir, githubClient, githubCredentials)
	nixDockerPreparer := nixdocker.New(gitManager)
	githubReleaseImagePreparer := githubreleaseimage.New(cfg.ReleasesDir, githubClient)

	go netproxy.RunNetStateWriter(ctx, store, cfg.MachineName, cfg.NetproxyStatePath)
	go engine.DeploymentOperator{
		Store:              store,
		GithubRelease:      githubReleasePreparer,
		NixDocker:          nixDockerPreparer,
		GithubReleaseImage: githubReleaseImagePreparer,
		RuntimeInputs:      runtimeInputs,
	}.RunAll(cfg.MachineName)

	runPrimaryConnLoop(ctx, cfg, store, primaryHTTPClient)
}

// newPrimaryHTTPClient builds the HTTP/2-only client a worker uses to dial the
// primary's cluster endpoint over mTLS. serverName overrides the TLS SNI/verify
// name, needed when dialing the primary by IP (so verification matches the
// cert's DNS SAN rather than requiring an IP SAN).
//
// No http.Client.Timeout is set: it is a whole-request deadline and would abort
// the long-lived cluster stream. Connection liveness is handled by the HTTP/2
// PING-based health check.
func newPrimaryHTTPClient(tlsConfig *tls.Config, serverName string) *http.Client {
	cfg := tlsConfig.Clone()
	if serverName != "" {
		cfg.ServerName = serverName
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: cfg,
			Protocols:       protocols,
			HTTP2: &http.HTTP2Config{
				SendPingTimeout: 5 * time.Second,  // PING a silent primary after 5s idle
				PingTimeout:     10 * time.Second, // tear down if no ACK within 10s (~15s total to detect a dead primary)
			},
		},
	}
}
