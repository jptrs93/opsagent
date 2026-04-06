package handler

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/preparer"
)

var NoPreparerErr = apigen.NewApiErr("Config has no preparer configured", "no_preparer", http.StatusBadRequest)
var DeploymentNotFoundErr = apigen.NewApiErr("Config not found", "deployment_not_found", http.StatusNotFound)
var InvalidRequestBodyErr = apigen.NewApiErr("Invalid request body", "invalid_request_body", http.StatusBadRequest)
var MissingKeyErr = apigen.NewApiErr("Missing deployment identifier", "missing_key", http.StatusBadRequest)
var InvalidKeyErr = apigen.NewApiErr("Invalid deployment identifier", "invalid_key", http.StatusBadRequest)
var NoPrepareLogErr = apigen.NewApiErr("No prepare log found", "prepare_log_not_found", http.StatusNotFound)
var NoActivePrepareErr = apigen.NewApiErr("No active prepare found", "no_active_prepare", http.StatusNotFound)
var NoRunOutputErr = apigen.NewApiErr("No run output found", "run_output_not_found", http.StatusNotFound)
var StaleSeqNoErr = apigen.NewApiErr("Stale sequence number", "stale_seq_no", http.StatusConflict)

// PostV1DeploymentUpdate forwards a user deploy / stop action to the store.
// The store is responsible for merging the requested DesiredState into the
// owning DeploymentConfig record and bumping its SeqNo; the operator picks up
// the change via its subscription.
func (h *Handler) PostV1DeploymentUpdate(ctx apigen.Context, req *apigen.DeploymentUpdateRequest) (*apigen.DesiredState, error) {
	if req.ID == nil || req.ID.Name == "" || req.ID.Machine == "" {
		return nil, MissingKeyErr
	}

	desired := apigen.DesiredState{}
	if req.Stop {
		desired.Running = false
	} else if req.TargetVersion != "" {
		desired.Version = req.TargetVersion
		desired.Running = true
	}
	h.Store.MustSetDeploymentDesiredState(ctx, *req.ID, desired)
	return &desired, nil
}

func (h *Handler) PostV1PrepareOutput(ctx apigen.Context, r *http.Request, w http.ResponseWriter) error {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("reading prepare output request body: %w", err)
	}

	req, err := apigen.DecodePrepareOutputRequest(bodyBytes)
	if err != nil {
		respondErr(w, InvalidRequestBodyErr)
		return nil
	}
	if req.ID == nil || req.ID.Name == "" {
		respondErr(w, MissingKeyErr)
		return nil
	}

	if req.ID.Machine != "" && req.ID.Machine != h.MachineName && h.ClusterPrimary != nil {
		clusterReq := &apigen.MsgToWorker{PrepareLogRequest: req}
		return h.proxyRemoteLogs(w, req.ID.Machine, clusterReq)
	}

	logPath := req.OutputPath()
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			respondErr(w, NoPrepareLogErr)
			return nil
		}
		return fmt.Errorf("opening prepare log: %w", err)
	}
	defer f.Close()

	// TODO: tail the prepare log until the preparer reaches a terminal
	// state. Without a status-by-id read on the store we can't poll, so
	// for now we stream the static snapshot of whatever is on disk.
	return streamLogFile(ctx, w, f, func() bool { return false })
}

func (h *Handler) PostV1RunOutput(ctx apigen.Context, r *http.Request, w http.ResponseWriter) error {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("reading run output request body: %w", err)
	}

	req, err := apigen.DecodeRunOutputRequest(bodyBytes)
	if err != nil {
		respondErr(w, InvalidRequestBodyErr)
		return nil
	}
	if req.ID == nil || req.ID.Name == "" {
		respondErr(w, MissingKeyErr)
		return nil
	}

	if req.ID.Machine != "" && req.ID.Machine != h.MachineName && h.ClusterPrimary != nil {
		clusterReq := &apigen.MsgToWorker{RunLogRequest: req}
		return h.proxyRemoteLogs(w, req.ID.Machine, clusterReq)
	}

	logPath := req.OutputPath()
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			respondErr(w, NoRunOutputErr)
			return nil
		}
		return fmt.Errorf("opening run output: %w", err)
	}
	defer f.Close()

	// TODO: tail the run log while the runner is still active. Needs a
	// per-id status read on the store.
	return streamLogFile(ctx, w, f, func() bool { return false })
}

// proxyRemoteLogs streams log data from a remote worker back to the HTTP
// response.
func (h *Handler) proxyRemoteLogs(w http.ResponseWriter, machine string, req *apigen.MsgToWorker) error {
	reader, err := h.ClusterPrimary.RequestLogs(machine, req)
	if err != nil {
		respondErr(w, apigen.NewApiErr("Worker not connected: "+machine, "worker_not_connected", 502))
		return nil
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, canFlush := w.(http.Flusher)

	buf := make([]byte, 32*1024)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return nil
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return nil
		}
	}
}

// streamLogFile writes the current contents of f to w, then tails the file as
// long as keepTailing returns true. It bails out if the client disconnects or
// the underlying writer returns an error.
func streamLogFile(ctx apigen.Context, w http.ResponseWriter, f *os.File, keepTailing func() bool) error {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, canFlush := w.(http.Flusher)

	buf := make([]byte, 4096)
	drain := func() (eof bool, err error) {
		for {
			n, readErr := f.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return false, werr
				}
			}
			if readErr == io.EOF {
				return true, nil
			}
			if readErr != nil {
				return false, readErr
			}
		}
	}

	if _, err := drain(); err != nil {
		return nil
	}
	if canFlush {
		flusher.Flush()
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := drain(); err != nil {
				return nil
			}
			if canFlush {
				flusher.Flush()
			}
			if !keepTailing() {
				return nil
			}
		}
	}
}

// PostV1ListScopes looks up the latest DeploymentConfig for the requested
// environment+name, then dispatches to the correct preparer variant for
// scope discovery (branches for nix, nothing for github releases).
func (h *Handler) PostV1ListScopes(ctx apigen.Context, req *apigen.ListScopesRequest) (*apigen.ListScopesResponse, error) {
	dep, err := h.findLatestDeployment(req.Environment, req.DeploymentName)
	if err != nil {
		return nil, err
	}
	prepCfg := dep.Spec.Prepare
	switch {
	case prepCfg.NixBuild != nil:
		scopes, err := preparer.Nix.ListScopes(ctx, prepCfg)
		if err != nil {
			return nil, fmt.Errorf("listing scopes: %w", err)
		}
		return &apigen.ListScopesResponse{Scopes: scopes}, nil
	case prepCfg.GithubRelease != nil:
		scopes, err := preparer.GHRel.ListScopes(ctx, prepCfg)
		if err != nil {
			return nil, fmt.Errorf("listing scopes: %w", err)
		}
		return &apigen.ListScopesResponse{Scopes: scopes}, nil
	}
	return nil, NoPreparerErr
}

// PostV1ListVersions returns the available versions for the requested
// deployment's preparer variant under the given scope.
func (h *Handler) PostV1ListVersions(ctx apigen.Context, req *apigen.ListVersionsRequest) (*apigen.ListVersionsResponse, error) {
	dep, err := h.findLatestDeployment(req.Environment, req.DeploymentName)
	if err != nil {
		return nil, err
	}
	prepCfg := dep.Spec.Prepare
	switch {
	case prepCfg.NixBuild != nil:
		vs, err := preparer.Nix.ListVersions(ctx, prepCfg, req.Scope)
		if err != nil {
			return nil, fmt.Errorf("listing versions: %w", err)
		}
		return &apigen.ListVersionsResponse{Versions: vs}, nil
	case prepCfg.GithubRelease != nil:
		vs, err := preparer.GHRel.ListVersions(ctx, prepCfg, req.Scope)
		if err != nil {
			return nil, fmt.Errorf("listing versions: %w", err)
		}
		return &apigen.ListVersionsResponse{Versions: vs}, nil
	}
	return nil, NoPreparerErr
}

// findLatestDeployment resolves (environment, name) to the most recent
// DeploymentConfig by scanning the snapshot returned from the store. It does
// not filter by machine since list-scopes/list-versions can legitimately
// be invoked for remote-machine deployments.
//
// TODO: the store will eventually expose a direct (env,name) lookup.
// For now we scan the full snapshot, which is fine at current scale.
func (h *Handler) findLatestDeployment(environment, name string) (*apigen.DeploymentConfig, error) {
	snapshot, _ := h.Store.MustFetchSnapshotAndSubscribe(nil, "")
	for _, dws := range snapshot {
		c := dws.Config
		if c == nil || c.ID == nil {
			continue
		}
		if c.ID.Environment == environment && c.ID.Name == name {
			if c.Spec == nil || c.Spec.Prepare == nil {
				return nil, NoPreparerErr
			}
			return c, nil
		}
	}
	return nil, DeploymentNotFoundErr
}

func isPrepareInProgress(status apigen.PreparationStatus) bool {
	return status == apigen.PreparationStatus_PREPARING ||
		status == apigen.PreparationStatus_DOWNLOADING
}
