// Package primary implements the primary-side cluster handler. It is the
// server side of the generated OpsagentClusterV1 bidirectional stream: workers
// connect over mTLS, the primary sends them the current per-machine deployment
// snapshot, forwards ongoing config updates, and handles incoming status writes
// and log proxy requests. Peer identity is the worker's client-cert CN, lifted
// into the request context by VerifyClusterPeer.
package clusterserver

import (
	"context"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/jptrs93/goutil/pubsubu"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/repo/githubcredentials"
	"github.com/jptrs93/opsagent/backend/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

// machineCtxKey keys the worker's certificate CN in the request context.
type machineCtxKey struct{}

var clusterForbiddenErr = apigen.NewApiErr("Forbidden", "cluster_request_not_authorized", http.StatusForbidden)

// VerifyClusterPeer is the MuxConfig.VerifyAuth hook for the cluster mux. The
// worker is already authenticated by mTLS (the listener requires and verifies a
// client cert); this lifts the verified CN into the auth context so the handler
// can identify the machine. It rejects connections without a peer certificate.
func VerifyClusterPeer(ctx context.Context, _ http.ResponseWriter, r *http.Request, _ apigen.AccessPolicy) (apigen.Context, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return apigen.Context{}, fmt.Errorf("cluster peer presented no certificate")
	}
	machine := r.TLS.PeerCertificates[0].Subject.CommonName
	if machine == "" {
		return apigen.Context{}, fmt.Errorf("cluster peer certificate has no CN")
	}
	return apigen.Context{Ctx: context.WithValue(ctx, machineCtxKey{}, machine)}, nil
}

// machineFromContext returns the worker machine name stashed by VerifyClusterPeer.
func machineFromContext(ctx context.Context) string {
	name, _ := ctx.Value(machineCtxKey{}).(string)
	return name
}

func requireMachine(ctx context.Context) (string, error) {
	machine := machineFromContext(ctx)
	if machine == "" {
		return "", fmt.Errorf("cluster request missing machine identity")
	}
	return machine, nil
}

// Primary manages worker sessions and forwards state between the local store
// and connected workers. It implements apigen.OpsagentClusterV1Handler; the
// generated mux invokes PostV1ClusterConnect once per worker connection.
type Primary struct {
	store             *sqlite.PrimaryStorage
	assets            assetProvider
	githubCredentials githubcredentials.Provider
	secrets           *secrets.Manager

	mu          sync.RWMutex
	sessions    map[string]*Session  // machine name → session
	connectedAt map[string]time.Time // machine name → when session was accepted
	machineSubs *pubsubu.PubSub[apigen.ClusterMachine]
}

type assetProvider interface {
	OpenAsset(ctx context.Context, assetID int32) (*apigen.Asset, io.ReadCloser, error)
}

// New creates a Primary. RunPrimary mounts it on the mTLS HTTP/2 cluster
// listener.
func New(store *sqlite.PrimaryStorage, assets assetProvider, githubCredentials githubcredentials.Provider, secretsMgr *secrets.Manager) *Primary {
	return &Primary{
		store:             store,
		assets:            assets,
		githubCredentials: githubCredentials,
		secrets:           secretsMgr,
		sessions:          make(map[string]*Session),
		connectedAt:       make(map[string]time.Time),
		machineSubs:       &pubsubu.PubSub[apigen.ClusterMachine]{},
	}
}

func (p *Primary) GetV1ClusterGithubCredentials(authCtx apigen.Context) (*apigen.GithubCredentials, error) {
	machine, err := requireMachine(authCtx)
	if err != nil {
		return nil, err
	}
	if !p.allowedRefsForMachine(machine).usesGithub {
		return nil, clusterForbiddenErr
	}
	creds, err := p.githubCredentials.LoadCredentials(authCtx)
	if err != nil {
		return nil, err
	}
	return &apigen.GithubCredentials{Token: creds.Token, ChangedAt: creds.ChangedAt}, nil
}

func (p *Primary) GetV1ClusterAsset(authCtx apigen.Context, r *http.Request, w http.ResponseWriter) error {
	assetID, err := int32QueryParam(r, "asset_id")
	if err != nil {
		return err
	}
	machine, err := requireMachine(authCtx)
	if err != nil {
		return err
	}
	if !p.allowedRefsForMachine(machine).assetAllowed(assetID) {
		return clusterForbiddenErr
	}
	asset, body, err := p.assets.OpenAsset(authCtx, assetID)
	if err != nil {
		return err
	}
	defer body.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(int(asset.SizeBytes)))
	w.Header().Set("X-Opsagent-Asset-ID", strconv.Itoa(int(asset.ID)))
	w.Header().Set("X-Opsagent-Asset-Key", url.QueryEscape(asset.Key))
	w.Header().Set("X-Opsagent-Asset-Version", strconv.Itoa(int(asset.Version)))
	w.Header().Set("X-Opsagent-Asset-Format", asset.Format)
	if _, err := io.Copy(w, body); err != nil {
		slog.ErrorContext(authCtx, "stream cluster asset", "asset_id", assetID, "err", err)
	}
	return nil
}

func int32QueryParam(r *http.Request, name string) (int32, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive int32", name)
	}
	return int32(value), nil
}

type clusterAllowedRefs struct {
	deploymentIDs map[int32]struct{}
	secretIDs     map[int32]struct{}
	configIDs     map[int32]struct{}
	assetIDs      map[int32]struct{}
	usesGithub    bool
}

func (p *Primary) allowedRefsForMachine(machine string) clusterAllowedRefs {
	return buildAllowedRefs(p.store.FetchDeploymentSnapshot(machine))
}

func buildAllowedRefs(snapshot []apigen.DeploymentWithStatus) clusterAllowedRefs {
	refs := clusterAllowedRefs{
		deploymentIDs: make(map[int32]struct{}),
		secretIDs:     make(map[int32]struct{}),
		configIDs:     make(map[int32]struct{}),
		assetIDs:      make(map[int32]struct{}),
	}
	for _, dws := range snapshot {
		cfg := dws.Config
		if cfg.ID != 0 {
			refs.deploymentIDs[cfg.ID] = struct{}{}
		}
		if cfg.Spec.Prepare.NixDockerBuild != nil || cfg.Spec.Prepare.GithubRelease != nil {
			refs.usesGithub = true
		}
		container := cfg.Spec.Runner.Container
		for _, value := range container.EnvVars {
			if value == nil {
				continue
			}
			if value.SecretID != nil && *value.SecretID > 0 {
				refs.secretIDs[*value.SecretID] = struct{}{}
			}
			if value.ConfigID != nil && *value.ConfigID > 0 {
				refs.configIDs[*value.ConfigID] = struct{}{}
			}
			if value.AssetID > 0 {
				refs.assetIDs[value.AssetID] = struct{}{}
			}
		}
		for _, mount := range container.AssetMounts {
			if mount == nil || mount.AssetID <= 0 {
				continue
			}
			refs.assetIDs[mount.AssetID] = struct{}{}
		}
	}
	return refs
}

func (r clusterAllowedRefs) deploymentAllowed(id int32) bool {
	_, ok := r.deploymentIDs[id]
	return ok
}

func (r clusterAllowedRefs) allSecretsAllowed(ids []int32) bool {
	return allInt32RefsAllowed(ids, r.secretIDs)
}

func (r clusterAllowedRefs) allConfigsAllowed(ids []int32) bool {
	return allInt32RefsAllowed(ids, r.configIDs)
}

func (r clusterAllowedRefs) assetAllowed(assetID int32) bool {
	_, ok := r.assetIDs[assetID]
	return ok
}

func allInt32RefsAllowed(ids []int32, allowed map[int32]struct{}) bool {
	for _, id := range ids {
		if id <= 0 {
			return false
		}
		if _, ok := allowed[id]; !ok {
			return false
		}
	}
	return true
}

func (p *Primary) GetV1ClusterSecrets(authCtx apigen.Context, req *apigen.ClusterSecretsRequest) (*apigen.ClusterSecretsResponse, error) {
	if p.secrets == nil {
		return nil, fmt.Errorf("secrets manager is not configured")
	}
	if req == nil || len(req.Ids) == 0 {
		return nil, fmt.Errorf("at least one secret id is required")
	}
	machine, err := requireMachine(authCtx)
	if err != nil {
		return nil, err
	}
	if !p.allowedRefsForMachine(machine).allSecretsAllowed(req.Ids) {
		return nil, clusterForbiddenErr
	}
	values, err := p.secrets.ResolveMany(req.Ids)
	if err != nil {
		return nil, err
	}
	items := make([]*apigen.ClusterSecretValue, 0, len(values))
	for id, value := range values {
		items = append(items, &apigen.ClusterSecretValue{ID: id, Value: []byte(value)})
	}
	return &apigen.ClusterSecretsResponse{Items: items}, nil
}

func (p *Primary) GetV1ClusterConfigs(authCtx apigen.Context, req *apigen.ClusterConfigsRequest) (*apigen.ClusterConfigsResponse, error) {
	if req == nil || len(req.Ids) == 0 {
		return nil, fmt.Errorf("at least one config id is required")
	}
	machine, err := requireMachine(authCtx)
	if err != nil {
		return nil, err
	}
	if !p.allowedRefsForMachine(machine).allConfigsAllowed(req.Ids) {
		return nil, clusterForbiddenErr
	}
	values, err := p.store.ResolveConfigs(req.Ids)
	if err != nil {
		return nil, err
	}
	items := make([]*apigen.ClusterConfigValue, 0, len(values))
	for id, value := range values {
		items = append(items, &apigen.ClusterConfigValue{ID: id, Value: value})
	}
	return &apigen.ClusterConfigsResponse{Items: items}, nil
}

// PostV1ClusterConnect handles one worker's bidirectional stream for its full
// lifetime: it registers the session, sends the initial snapshot, forwards
// config updates plus keepalive frames, and ingests the worker's status writes
// and log chunks. It returns (ending the stream) when the worker disconnects,
// the request errors, or the context is cancelled.
func (p *Primary) PostV1ClusterConnect(authCtx apigen.Context, reqs iter.Seq2[*apigen.MsgToMaster, error]) iter.Seq2[*apigen.MsgToWorker, error] {
	return func(yield func(*apigen.MsgToWorker, error) bool) {
		machine := machineFromContext(authCtx)
		if machine == "" {
			yield(nil, fmt.Errorf("cluster connection missing machine identity"))
			return
		}

		sessCtx, cancel := context.WithCancel(authCtx)
		defer cancel()

		sess := newSession(sessCtx, cancel, machine, p.store)
		p.registerSession(machine, sess)
		defer p.unregisterSession(machine, sess)
		// Ensure the worker has its OPENDEPLOY deployment now that it has
		// connected and been registered.
		p.store.EnsureSystemDeployment(machine, "")

		sess.run(reqs, yield)
	}
}

func (p *Primary) registerSession(machine string, sess *Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.sessions[machine]; ok {
		old.cancel() // kick the stale session so its handler returns
	}
	p.sessions[machine] = sess
	connectedAt := time.Now()
	p.connectedAt[machine] = connectedAt
	p.machineSubs.Notify(apigen.ClusterMachine{Name: machine, Connected: true, ConnectedAt: connectedAt})
}

func (p *Primary) unregisterSession(machine string, expected *Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if current, ok := p.sessions[machine]; ok && current == expected {
		delete(p.sessions, machine)
		delete(p.connectedAt, machine)
		p.machineSubs.Notify(apigen.ClusterMachine{Name: machine, Connected: false})
	}
}

// RequestLogs sends a log request to the named worker and returns a reader that
// yields the streamed log data. The caller must read until EOF (or close the
// reader to abort).
func (p *Primary) RequestLogs(machineName string, req *apigen.MsgToWorker) (io.ReadCloser, error) {
	p.mu.RLock()
	sess, ok := p.sessions[machineName]
	p.mu.RUnlock()
	if !ok {
		return nil, &MachineNotConnectedError{Machine: machineName}
	}
	return sess.requestLogs(req)
}

func (p *Primary) RequestLogSearch(machineName string, req *apigen.MsgToWorker) (*LogSearchStream, error) {
	p.mu.RLock()
	sess, ok := p.sessions[machineName]
	p.mu.RUnlock()
	if !ok {
		return nil, &MachineNotConnectedError{Machine: machineName}
	}
	return sess.requestLogSearch(req)
}

// ConnectedMachines returns the set of currently connected worker machines and
// when each connected.
func (p *Primary) ConnectedMachines() map[string]time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]time.Time, len(p.sessions))
	for name := range p.sessions {
		out[name] = p.connectedAt[name]
	}
	return out
}

func (p *Primary) FetchMachinesSnapshotAndSubscribe() ([]*apigen.ClusterMachine, chan apigen.ClusterMachine, func()) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	machines := make([]*apigen.ClusterMachine, 0, len(p.sessions))
	for name := range p.sessions {
		machines = append(machines, &apigen.ClusterMachine{
			Name:        name,
			Connected:   true,
			ConnectedAt: p.connectedAt[name],
		})
	}
	sub := p.machineSubs.Subscribe(nil)
	return machines, sub.Ch, sub.UnsubscribeFunc
}

// MachineNotConnectedError is returned when a log proxy request targets a
// machine that has no active cluster session.
type MachineNotConnectedError struct {
	Machine string
}

func (e *MachineNotConnectedError) Error() string {
	return "machine not connected: " + e.Machine
}
