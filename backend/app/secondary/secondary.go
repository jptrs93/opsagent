package secondary

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/netproxy"
	"github.com/jptrs93/opsagent/backend/lib/acmestate"
	"github.com/jptrs93/opsagent/backend/lib/engine"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/githubrelease"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/githubreleaseimage"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/nixdocker"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/localinputs"
	"github.com/jptrs93/opsagent/backend/lib/machinekey"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/lib/repo/git"
	githubrepo "github.com/jptrs93/opsagent/backend/lib/repo/github"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/state"
)

type runtimeConfig struct {
	TLS                *tls.Config
	PrimaryClusterAddr string
	PrimaryName        string // primary certificate DNS/IP SAN for TLS server name verification
	UnderlayAddress    string // explicit tunnel endpoint; otherwise resolved for every reconnect
	NodeIdentifier     string
	NodeID             int32
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

	store := state.Open(filepath.Join(cfg.DataDir, "secondary.db"))
	primaryHTTPClient := newPrimaryHTTPClient(cfg.TLS, cfg.PrimaryName)
	primaryURL := "https://" + cfg.PrimaryClusterAddr
	githubCredentials := NewPrimaryGithubCredentialsProvider(primaryURL, primaryHTTPClient)
	network.SetDefault(network.New(cfg.ClusterPrefix, cfg.NetDeploymentID))
	if clusterMap, _, ok, err := cachedClusterNetMap(store, cfg.NodeID, cfg.ClusterPrefix); err != nil {
		slog.Warn("loading cached cluster network map failed", "err", err)
	} else if ok {
		if err := reconcileClusterNetMap(clusterMap, cfg.NodeID, cfg.ClusterPrefix); err != nil {
			slog.Warn("reconciling cached cluster network map failed", "err", err)
		}
	}

	assetProvider := NewPrimaryAssetProvider(primaryURL, primaryHTTPClient)
	secretProvider := NewPrimarySecretProvider(primaryURL, primaryHTTPClient)
	configProvider := NewPrimaryConfigProvider(primaryURL, primaryHTTPClient)

	// Runtime inputs are loaded from local storage before the operator starts, so
	// a worker that reboots while the primary is unreachable can still resolve
	// every secret and config its workloads need. Failures here are not fatal:
	// NewPersistent still returns a usable RuntimeInputs, it just starts empty and
	// refetches, which is the pre-persistence behaviour.
	var inputPersistence runtimeinputs.Persistence
	localInputs, err := localinputs.Open(store, &machinekey.File{Path: filepath.Join(cfg.DataDir, machinekey.FileName)})
	if err != nil {
		slog.Warn("opening local runtime input store failed; runtime inputs will be fetched from the primary on every restart", "err", err)
	} else {
		inputPersistence = localInputs
	}
	runtimeInputs, err := runtimeinputs.NewPersistent(assetProvider, secretProvider, configProvider, inputPersistence)
	if err != nil {
		slog.Warn("loading persisted runtime inputs failed; they will be refetched from the primary", "err", err)
	}
	gitManager := git.NewManager(cfg.GitCacheDir, githubCredentials)
	githubClient := githubrepo.NewClient(githubCredentials)
	githubReleasePreparer := githubrelease.New(cfg.ReleasesDir, githubClient)
	nixDockerPreparer := nixdocker.New(gitManager)
	githubReleaseImagePreparer := githubreleaseimage.New(cfg.ReleasesDir, githubClient)

	acmeHolder := acmestate.NewHolder()
	if b, ok := store.FetchLocalKV(storage.LocalKVAcmeState); ok {
		if persisted, err := apigen.DecodeAcmeState(b); err != nil {
			slog.Warn("decoding persisted ACME state failed", "err", err)
		} else {
			acmeHolder.Set(persisted)
		}
	}
	go netproxy.RunNetStateWriter(ctx, store, scheduledInstancePredicateForNode(cfg.NodeID), cfg.NodeIdentifier, cfg.NetproxyStatePath, runtimeInputs, acmeHolder, runtimeInputs.EnsureSecretIDs)
	go runRuntimeInputRetention(ctx, store, runtimeInputs, scheduledInstancePredicateForNode(cfg.NodeID), acmeHolder)
	go engine.DeploymentOperator{
		Store:              store,
		GithubRelease:      githubReleasePreparer,
		NixDocker:          nixDockerPreparer,
		GithubReleaseImage: githubReleaseImagePreparer,
		RuntimeInputs:      runtimeInputs,
	}.RunAll(scheduledInstancePredicateForNode(cfg.NodeID))

	runPrimaryConnLoop(ctx, cfg, store, primaryHTTPClient, acmeHolder)
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
