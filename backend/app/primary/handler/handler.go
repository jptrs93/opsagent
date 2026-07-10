package handler

import (
	"context"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/dataplane"
	"github.com/jptrs93/opsagent/backend/app/primary/clusterserver"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/engine"
	"github.com/jptrs93/opsagent/backend/lib/engine/assetstore"
	"github.com/jptrs93/opsagent/backend/lib/engine/configdist"
	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	preparer2 "github.com/jptrs93/opsagent/backend/lib/engine/preparer"
	runner2 "github.com/jptrs93/opsagent/backend/lib/engine/runner"
	"github.com/jptrs93/opsagent/backend/lib/engine/secretdist"
	versionprovider2 "github.com/jptrs93/opsagent/backend/lib/engine/versionprovider"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
	"github.com/jptrs93/opsagent/backend/util/version"
)

type Handler struct {
	staticFS       fs.FS
	PasskeyService *authu.PasskeyService[*apigen.InternalUser]
	jwtAuth        *authu.JWTAuth[*apigen.InternalUser, int32]

	// Store is the primary-side storage adapter. Handles both deployment
	// state and auth (users + JWT keys).
	Store         *sqlite.PrimaryStorage
	Assets        *assetstore.Store
	ConfigService *config.Service
	Config        *apigen.Settings
	Github        githubcredentials.Provider

	// Secrets is the primary-only encrypted secrets store. Deployment preparation
	// decrypts referenced secret IDs into an in-memory runner cache.
	Secrets *secrets.Manager

	// MachineName identifies this node when deciding whether a log request
	// is local or must be proxied to a remote worker.
	MachineName string

	// ClusterPrimary is set when running in primary cluster mode. Used by
	// handlers to proxy log requests to remote workers. Nil in standalone
	// or slave mode.
	ClusterPrimary *clusterserver.Primary

	// EnrollmentTLSFingerprint is the sha256 SPKI fingerprint for the TLS
	// certificate presented by the unauthenticated enrollment listener.
	EnrollmentTLSFingerprint string

	enrollmentMu       sync.Mutex
	enrollmentSessions map[int32]*enrollmentSession
}

func (h *Handler) Get(ctx apigen.Context, request *http.Request, writer http.ResponseWriter) error {
	if h.staticFS == nil {
		return errors.New("static fs is not configured")
	}
	setStaticSecurityHeaders(request, writer)
	assetPath := strings.TrimPrefix(request.URL.Path, "/")
	if assetPath == "" {
		assetPath = "index.html"
	}
	b, err := fs.ReadFile(h.staticFS, assetPath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		assetPath = "index.html"
		b, err = fs.ReadFile(h.staticFS, assetPath)
		if err != nil {
			return err
		}
	}
	if contentType := mime.TypeByExtension(filepath.Ext(assetPath)); contentType != "" {
		writer.Header().Set("Content-Type", contentType)
	}
	_, err = writer.Write(b)
	return err
}

func setStaticSecurityHeaders(request *http.Request, writer http.ResponseWriter) {
	h := writer.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), serial=(), bluetooth=()")
	if request.TLS != nil {
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
}

func (h *Handler) GetV1Healthz(ctx apigen.Context, request *http.Request, writer http.ResponseWriter) error {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, err := writer.Write([]byte("OK"))
	return err
}

func New(ctx context.Context, staticFS fs.FS, machineName string) (*Handler, error) {
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
	snapshot := configService.Snapshot()
	cfg := &snapshot.Settings
	assetStore := &assetstore.Store{
		DB:      store,
		Secrets: secretsMgr,
		Loader:  configService,
		Config: func() *apigen.Settings {
			snapshot := configService.Snapshot()
			return &snapshot.Settings
		},
	}
	githubCredentials := githubcredentials.SecretProvider{
		Secrets: secretsMgr,
		SecretRef: func(context.Context) apigen.SecretRef {
			return configService.Snapshot().Settings.Repo.GithubToken
		},
	}

	preparer2.GHRel = preparer2.NewGithubReleaseDownloader(ainit.StaticConfig.DataDir, githubCredentials)

	// Shared containerd client for the container image preparer and runner. It
	// connects lazily, so opendeploy still starts on hosts without containerd.
	ctrdClient := ctrd.Connect(ainit.StaticConfig.ContainerdAddress, ainit.StaticConfig.ContainerdNamespace)
	preparer2.NixDocker = preparer2.NewNixDockerBuilder(ainit.StaticConfig.DataDir, githubCredentials, ctrdClient)
	preparer2.ContainerImg = preparer2.NewContainerImagePuller(ctrdClient, preparer2.GHRel)
	preparer2.Assets = assetStore
	secretProvider := secretdist.NewPrimaryProvider(secretsMgr)
	preparer2.Secrets = secretProvider
	configProvider := configdist.NewPrimaryProvider(store)
	preparer2.Configs = configProvider
	runner2.Containerd = ctrdClient

	versionprovider2.Git = versionprovider2.NewGitVersionProvider(preparer2.NixDocker.Git)
	versionprovider2.GHRel = versionprovider2.NewGithubReleaseVersionProvider(githubCredentials)

	// Wire prepared in-memory secrets/configs for typed env refs.
	runner2.Secrets = secretProvider
	runner2.Configs = configProvider

	h := &Handler{
		staticFS:           staticFS,
		Store:              store,
		Assets:             assetStore,
		ConfigService:      configService,
		Config:             cfg,
		Github:             githubCredentials,
		Secrets:            secretsMgr,
		MachineName:        machineName,
		enrollmentSessions: make(map[int32]*enrollmentSession),
	}
	h.jwtAuth = authu.NewJWTAuth[*apigen.InternalUser, int32](
		func(kid string, key []byte) error {
			h.Store.WritePublicKey(&apigen.PublicKeyRecord{Kid: kid, KeyBytes: key})
			return nil
		},
		func(kid string) ([]byte, error) {
			rec, err := h.Store.FetchPublicKey(kid)
			if err != nil {
				return nil, err
			}
			return rec.KeyBytes, nil
		},
		func(id int32) (*apigen.InternalUser, error) {
			return h.Store.FetchUserByID(id)
		},
	)
	if err := h.initPasskeyService(); err != nil {
		return nil, err
	}

	// Ensure the system self-management deployment exists for the primary.
	h.Store.EnsureSystemDeployment(machineName, version.Version)
	h.Store.EnsureNodesForSystemDeployments(machineName)
	dataplaneCfg := h.Store.EnsureDataplaneDeployment(machineName, version.Version)
	network.Default.SetDataplaneDeploymentID(dataplaneCfg.ID)

	// Kick off the deployment operator for this machine. RunAll pulls the
	// current snapshot from the store and spawns a per-deployment reconciler
	// for each entry, plus a forwarder that fans store updates out to them.
	go dataplane.RunNetStateWriter(ctx, h.Store, machineName, dataplane.NetStatePath(ainit.StaticConfig.DataDir))
	go engine.DeploymentOperator{Store: h.Store}.RunAll(machineName)

	return h, nil
}

func respond(w http.ResponseWriter, msg interface{ Encode() []byte }) {
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(msg.Encode())
}

func respondErr(w http.ResponseWriter, err apigen.ApiErr) {
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(int(err.Code))
	w.Write(err.Encode())
}
