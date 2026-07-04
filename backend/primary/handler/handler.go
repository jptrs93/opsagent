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
	"github.com/jptrs93/opsagent/backend/assetstore"
	"github.com/jptrs93/opsagent/backend/config"
	"github.com/jptrs93/opsagent/backend/engine"
	"github.com/jptrs93/opsagent/backend/engine/configdist"
	"github.com/jptrs93/opsagent/backend/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/engine/preparer"
	"github.com/jptrs93/opsagent/backend/engine/runner"
	"github.com/jptrs93/opsagent/backend/engine/secretdist"
	"github.com/jptrs93/opsagent/backend/engine/versionprovider"
	"github.com/jptrs93/opsagent/backend/primary/clusterserver"
	"github.com/jptrs93/opsagent/backend/repo/githubcredentials"
	"github.com/jptrs93/opsagent/backend/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
	"github.com/jptrs93/opsagent/backend/version"
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
	// Open the primary-only secrets store before config snapshotting so legacy
	// fixed secret references can be detected without revealing plaintext.
	secretsMgr, err := secrets.Open(ainit.StaticConfig.DataDir, store)
	if err != nil {
		return nil, err
	}
	configService, err := config.NewService(store)
	if err != nil {
		return nil, err
	}
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

	preparer.GHRel = preparer.NewGithubReleaseDownloader(ainit.StaticConfig.DataDir, githubCredentials)

	// Shared containerd client for the container image preparer and runner. It
	// connects lazily, so opendeploy still starts on hosts without containerd.
	ctrdClient := ctrd.Connect(ainit.StaticConfig.ContainerdAddress, ainit.StaticConfig.ContainerdNamespace)
	preparer.NixDocker = preparer.NewNixDockerBuilder(ainit.StaticConfig.DataDir, githubCredentials, ctrdClient)
	preparer.ContainerImg = preparer.NewContainerImagePuller(ctrdClient)
	preparer.Assets = assetStore
	secretProvider := secretdist.NewPrimaryProvider(secretsMgr)
	preparer.Secrets = secretProvider
	configProvider := configdist.NewPrimaryProvider(store)
	preparer.Configs = configProvider
	runner.Containerd = ctrdClient

	versionprovider.Git = versionprovider.NewGitVersionProvider(preparer.NixDocker.Git)
	versionprovider.GHRel = versionprovider.NewGithubReleaseVersionProvider(githubCredentials)

	// Wire prepared in-memory secrets/configs for typed env refs.
	runner.Secrets = secretProvider
	runner.Configs = configProvider

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

	// Log collector is parked while container logging writes merged binary files directly.
	// go logcollector.RunAll(ctx, h.Store, machineName)

	// Kick off the deployment operator for this machine. RunAll pulls the
	// current snapshot from the store and spawns a per-deployment reconciler
	// for each entry, plus a forwarder that fans store updates out to them.
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
