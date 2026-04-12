package handler

import (
	"context"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine"
	"github.com/jptrs93/opsagent/backend/engine/preparer"
	"github.com/jptrs93/opsagent/backend/engine/versionprovider"
	"github.com/jptrs93/opsagent/backend/primary"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

type Handler struct {
	staticFS       fs.FS
	PasskeyService *authu.PasskeyService[*apigen.InternalUser]
	jwtAuth        *authu.JWTAuth[*apigen.InternalUser, int32]

	// Store is the primary-side storage adapter. Handles both deployment
	// state and auth (users + JWT keys).
	Store *sqlite.StorageAdapter

	// MachineName identifies this node when deciding whether a log request
	// is local or must be proxied to a remote worker.
	MachineName string

	// ClusterPrimary is set when running in primary cluster mode. Used by
	// handlers to proxy log requests to remote workers. Nil in standalone
	// or slave mode.
	ClusterPrimary *primary.Primary
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

	versionprovider.Git = versionprovider.NewGitVersionProvider(preparer.Nix.Git)
	versionprovider.GHRel = versionprovider.NewGithubReleaseVersionProvider(ainit.Config.GithubToken)

	h := &Handler{
		staticFS:    staticFS,
		Store:       store,
		MachineName: machineName,
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

	// Kick off the deployment operator for this machine. RunAll pulls the
	// current snapshot from the store and spawns a per-deployment reconciler
	// for each entry, plus a forwarder that fans store updates out to them.
	go engine.DeploymentOperator{Store: h.Store}.RunAll(context.Background(), machineName)

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
