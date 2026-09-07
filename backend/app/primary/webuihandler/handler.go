package webuihandler

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/clusterhandler"
	"github.com/jptrs93/opsagent/backend/app/primary/enrollmenthandler"
	"github.com/jptrs93/opsagent/backend/lib/authz"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/engine/assetstore"
	"github.com/jptrs93/opsagent/backend/lib/engine/versionprovider"
	"github.com/jptrs93/opsagent/backend/lib/log/logmanager"
	"github.com/jptrs93/opsagent/backend/lib/metrics/metricstore"
	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

type GitSourceProvider interface {
	ListBranches(context.Context, string) ([]string, error)
	ListCommits(context.Context, string, string, int) ([]*apigen.Version, error)
	DiscoverVersions(context.Context, string, string, int) ([]string, string, []*apigen.Version, error)
	DefaultCommit(context.Context, string) (string, string, error)
	ValidateCommit(context.Context, string, string) error
	ValidateNixSource(context.Context, string, string, string) (bool, error)
}

type Handler struct {
	staticFS       fs.FS
	PasskeyService *authu.PasskeyService[*apigen.InternalUser]
	jwtAuth        *authu.JWTAuth[*apigen.InternalUser, int32]

	// Store is the primary-side storage adapter. Handles both deployment
	// state and auth (users + JWT keys).
	Store                 *state.Service
	Authz                 *authz.Service
	Assets                *assetstore.Store
	ConfigService         *config.Service
	Config                *apigen.ClusterSettings
	GitVersions           GitSourceProvider
	GithubReleaseVersions *versionprovider.GithubReleaseVersionProvider
	GithubCredentials     githubcredentials.Provider

	// Secrets is the primary-only encrypted secrets store. Deployment preparation
	// decrypts referenced secret IDs into the shared RuntimeInputs cache.
	Secrets *secrets.Manager

	// NodeID identifies this node when deciding whether a log request is local
	// or must be proxied to a remote secondary.
	NodeID int32

	// Cluster is used to inspect secondary connections and proxy remote logs.
	Cluster *clusterhandler.Handler

	// LogManager serves log searches for deployments running on this node.
	LogManager *logmanager.Manager

	// Metrics serves container metrics for deployments running on this node.
	Metrics *metricstore.Store

	// Enrollment owns the enrollment stream and operator enrollment actions.
	Enrollment *enrollmenthandler.Handler

	// IngressDiagnostics supplies the ingress evaluation warnings shown per
	// deployment; nil when no publisher is wired (tests).
	IngressDiagnostics IngressDiagnosticsSource
}

type IngressDiagnosticsSource interface {
	DiagnosticsSnapshotAndSubscribe() (*apigen.IngressDiagnosticList, <-chan *apigen.IngressDiagnosticList, func())
}

type Dependencies struct {
	Store                 *state.Service
	Assets                *assetstore.Store
	ConfigService         *config.Service
	GitVersions           GitSourceProvider
	GithubReleaseVersions *versionprovider.GithubReleaseVersionProvider
	GithubCredentials     githubcredentials.Provider
	Secrets               *secrets.Manager
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

// New constructs the Web UI handler without starting application services.
func New(staticFS fs.FS, nodeID int32, deps Dependencies) (*Handler, error) {
	snapshot := deps.ConfigService.Snapshot()
	authzService, err := authz.Open(deps.Store)
	if err != nil {
		return nil, err
	}
	h := &Handler{
		staticFS:              staticFS,
		Store:                 deps.Store,
		Authz:                 authzService,
		Assets:                deps.Assets,
		ConfigService:         deps.ConfigService,
		Config:                &snapshot.Settings,
		GitVersions:           deps.GitVersions,
		GithubReleaseVersions: deps.GithubReleaseVersions,
		GithubCredentials:     deps.GithubCredentials,
		Secrets:               deps.Secrets,
		NodeID:                nodeID,
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
		// With password login on, a relying-party configuration the WebAuthn
		// library rejects must not take the whole UI down: passkeys become
		// unavailable and the login page says so.
		if !h.passwordLoginEnabled() {
			return nil, err
		}
		slog.Error("passkeys unavailable: relying-party configuration rejected", "err", err)
		h.PasskeyService = nil
	}
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
