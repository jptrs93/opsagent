package handler

import (
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
	"github.com/jptrs93/opsagent/backend/engine"
	"github.com/jptrs93/opsagent/backend/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/engine/preparer"
	"github.com/jptrs93/opsagent/backend/engine/runner"
	"github.com/jptrs93/opsagent/backend/engine/versionprovider"
	"github.com/jptrs93/opsagent/backend/primary"
	"github.com/jptrs93/opsagent/backend/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

type Handler struct {
	staticFS       fs.FS
	PasskeyService *authu.PasskeyService[*apigen.InternalUser]
	jwtAuth        *authu.JWTAuth[*apigen.InternalUser, int32]

	// Store is the primary-side storage adapter. Handles both deployment
	// state and auth (users + JWT keys).
	Store *sqlite.StorageAdapter

	// Secrets is the primary-only encrypted secrets store. It resolves
	// ${name} env placeholders at deployment spawn time.
	Secrets *secrets.Manager

	// MachineName identifies this node when deciding whether a log request
	// is local or must be proxied to a remote worker.
	MachineName string

	// ClusterPrimary is set when running in primary cluster mode. Used by
	// handlers to proxy log requests to remote workers. Nil in standalone
	// or slave mode.
	ClusterPrimary *primary.Primary

	enrollmentMu       sync.Mutex
	enrollmentSessions map[int32]*enrollmentSession
}

func (h *Handler) Get(ctx apigen.Context, request *http.Request, writer http.ResponseWriter) error {
	if h.staticFS == nil {
		return errors.New("static fs is not configured")
	}
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

func (h *Handler) GetV1Healthz(ctx apigen.Context, request *http.Request, writer http.ResponseWriter) error {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, err := writer.Write([]byte("OK"))
	return err
}

func New(staticFS fs.FS, machineName string) (*Handler, error) {
	store := sqlite.NewStorageAdapter(filepath.Join(ainit.Config.DataDir, "primary.db"))

	preparer.Nix = preparer.NewNixBuilder(ainit.Config.DataDir, ainit.Config.GithubToken)
	preparer.GHRel = preparer.NewGithubReleaseDownloader(ainit.Config.DataDir, ainit.Config.GithubToken)

	// Shared containerd client for the container image preparer and runner. It
	// connects lazily, so opendeploy still starts on hosts without containerd.
	ctrdClient := ctrd.Connect(ainit.Config.ContainerdAddress, ainit.Config.ContainerdNamespace)
	preparer.ContainerImg = preparer.NewContainerImagePuller(ctrdClient)
	runner.Containerd = ctrdClient

	versionprovider.Git = versionprovider.NewGitVersionProvider(preparer.Nix.Git)
	versionprovider.GHRel = versionprovider.NewGithubReleaseVersionProvider(ainit.Config.GithubToken)

	// Open the primary-only secrets store and wire it as the runner's secret
	// resolver so ${name} env placeholders resolve at spawn time.
	secretsMgr, err := secrets.Open(ainit.Config.DataDir, store)
	if err != nil {
		return nil, err
	}
	runner.Secrets = secretsMgr

	h := &Handler{
		staticFS:           staticFS,
		Store:              store,
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
	h.Store.EnsureSystemDeployment(machineName)

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
