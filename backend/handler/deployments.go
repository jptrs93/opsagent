package handler

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/versionprovider"
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

func (h *Handler) PostV1DeploymentLogs(ctx apigen.Context, r *http.Request, w http.ResponseWriter) error {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("reading deployment log request body: %w", err)
	}

	req, err := apigen.DecodeDeploymentLogRequest(bodyBytes)
	if err != nil {
		respondErr(w, InvalidRequestBodyErr)
		return nil
	}

	var id *apigen.DeploymentIdentifier
	if req.RunnerOutput != nil {
		id = req.RunnerOutput.ID
	} else if req.PreparerOutput != nil {
		id = req.PreparerOutput.ID
	}
	if id == nil || id.Name == "" {
		respondErr(w, MissingKeyErr)
		return nil
	}

	// Forward to remote worker without resolving — the worker resolves locally.
	if id.Machine != "" && id.Machine != h.MachineName && h.ClusterPrimary != nil {
		clusterReq := &apigen.MsgToWorker{DeploymentLogRequest: req}
		return h.proxyRemoteLogs(w, id.Machine, clusterReq)
	}

	// Resolve seqNo=0 to latest from local status.
	if req.RunnerOutput != nil {
		if req.RunnerOutput.SeqNo == 0 {
			st := h.Store.FetchDeploymentStatus(*id)
			if st != nil && st.Runner != nil {
				req.RunnerOutput.SeqNo = st.Runner.DeploymentSeqNo
			}
		}
		return h.streamRunLog(ctx, w, req.RunnerOutput)
	}
	if req.PreparerOutput.SeqNo == 0 {
		st := h.Store.FetchDeploymentStatus(*id)
		if st != nil && st.Preparer != nil {
			req.PreparerOutput.SeqNo = st.Preparer.DeploymentSeqNo
		}
	}
	return h.streamPrepareLog(ctx, w, req.PreparerOutput)
}

func (h *Handler) streamRunLog(ctx apigen.Context, w http.ResponseWriter, req *apigen.RunOutputRequest) error {
	logPath := req.OutputPath()
	f, err := waitForFile(ctx, logPath)
	if err != nil {
		respondErr(w, NoRunOutputErr)
		return nil
	}
	defer f.Close()
	return streamLogFile(ctx, w, f, func() bool {
		st := h.Store.FetchDeploymentStatus(*req.ID)
		return st != nil && st.Runner != nil && isRunnerActive(st.Runner.Status)
	})
}

func (h *Handler) streamPrepareLog(ctx apigen.Context, w http.ResponseWriter, req *apigen.PrepareOutputRequest) error {
	logPath := req.OutputPath()
	f, err := waitForFile(ctx, logPath)
	if err != nil {
		respondErr(w, NoPrepareLogErr)
		return nil
	}
	defer f.Close()
	return streamLogFile(ctx, w, f, func() bool {
		st := h.Store.FetchDeploymentStatus(*req.ID)
		return st != nil && st.Preparer != nil && isPrepareInProgress(st.Preparer.Status)
	})
}

// waitForFile tries to open a file, retrying for up to 5 seconds if it
// doesn't exist yet (the runner/preparer may still be starting up).
func waitForFile(ctx apigen.Context, path string) (*os.File, error) {
	f, err := os.Open(path)
	if err == nil {
		return f, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, os.ErrNotExist
		case <-ticker.C:
			f, err = os.Open(path)
			if err == nil {
				return f, nil
			}
			if !os.IsNotExist(err) {
				return nil, err
			}
		}
	}
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
// environment+name, then dispatches to the correct version provider for
// scope discovery (branches for nix, nothing for github releases).
func (h *Handler) PostV1ListScopes(ctx apigen.Context, req *apigen.ListScopesRequest) (*apigen.ListScopesResponse, error) {
	dep, err := h.findLatestDeployment(req.Environment, req.DeploymentName)
	if err != nil {
		return nil, err
	}
	provider, err := versionprovider.ForConfig(dep.Spec.Prepare)
	if err != nil {
		return nil, NoPreparerErr
	}
	scopes, err := provider.ListScopes(ctx, dep.Spec.Prepare)
	if err != nil {
		return nil, fmt.Errorf("listing scopes: %w", err)
	}
	return &apigen.ListScopesResponse{Scopes: scopes}, nil
}

// PostV1ListVersions returns the available versions for the requested
// deployment's prepare config under the given scope.
func (h *Handler) PostV1ListVersions(ctx apigen.Context, req *apigen.ListVersionsRequest) (*apigen.ListVersionsResponse, error) {
	dep, err := h.findLatestDeployment(req.Environment, req.DeploymentName)
	if err != nil {
		return nil, err
	}
	provider, err := versionprovider.ForConfig(dep.Spec.Prepare)
	if err != nil {
		return nil, NoPreparerErr
	}
	vs, err := provider.ListVersions(ctx, dep.Spec.Prepare, req.Scope)
	if err != nil {
		return nil, fmt.Errorf("listing versions: %w", err)
	}
	return &apigen.ListVersionsResponse{Versions: vs}, nil
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

func isRunnerActive(status apigen.RunningStatus) bool {
	return status == apigen.RunningStatus_RUNNING ||
		status == apigen.RunningStatus_STARTING
}
