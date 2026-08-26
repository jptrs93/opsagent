// Package clusterhandler implements the primary-side cluster handler. It is the
// server side of the generated OpsagentClusterV1 bidirectional stream: workers
// connect over mTLS, the primary sends them the current per-machine deployment
// snapshot, forwards ongoing config updates, and handles incoming status writes
// and log proxy requests. Peer identity is the worker's client-cert CN, lifted
// into the request context by VerifyClusterPeer.
package clusterhandler

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/lib/acmestate"
	"github.com/jptrs93/opsagent/backend/lib/issuedtls"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/util/certu"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage"
)

var _ apigen.OpsagentClusterV1Handler = (*Handler)(nil)

type machineCtxKey struct{}

type peerCertCtxKey struct{}

var clusterForbiddenErr = apigen.NewApiErr("Forbidden", "cluster_request_not_authorized", http.StatusForbidden)

// VerifyClusterPeer is the MuxConfig.VerifyAuth hook for the cluster mux. The
// worker is already authenticated by mTLS (the listener requires and verifies a
// client cert); this lifts the verified CN into the auth context as the node
// identifier. It rejects connections without a peer certificate.
func VerifyClusterPeer(ctx context.Context, _ http.ResponseWriter, r *http.Request, _ apigen.AccessPolicy) (apigen.Context, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return apigen.Context{}, fmt.Errorf("cluster peer presented no certificate")
	}
	peerCert := r.TLS.PeerCertificates[0]
	machine := peerCert.Subject.CommonName
	if machine == "" {
		return apigen.Context{}, fmt.Errorf("cluster peer certificate has no CN")
	}
	ctx = context.WithValue(ctx, machineCtxKey{}, machine)
	ctx = context.WithValue(ctx, peerCertCtxKey{}, peerCert)
	return apigen.Context{Ctx: ctx}, nil
}

func peerCertFromContext(ctx context.Context) *x509.Certificate {
	cert, _ := ctx.Value(peerCertCtxKey{}).(*x509.Certificate)
	return cert
}

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

func scheduledInstancePredicateForNode(nodeID int32) storage.ScheduledInstancePredicate {
	return func(state apigen.ScheduledInstanceState) bool {
		return state.Instance.NodeID == nodeID
	}
}

func (p *Handler) requireScheduledInstancePredicate(ctx context.Context) (storage.ScheduledInstancePredicate, error) {
	machine, err := requireMachine(ctx)
	if err != nil {
		return nil, err
	}
	nodeID, err := p.store.NodeIDByIdentifier(machine)
	if err != nil {
		return nil, clusterForbiddenErr
	}
	return scheduledInstancePredicateForNode(nodeID), nil
}

// Handler manages worker sessions and forwards state between the local store
// and connected workers. It implements apigen.OpsagentClusterV1Handler; the
// generated mux invokes PostV1ClusterConnect once per worker connection.
type Handler struct {
	store             *state.Service
	assets            assetProvider
	githubCredentials githubcredentials.Provider
	secrets           *secrets.Manager
	networkPrefix     network.Prefix
	networkMaps       networkMapProvider
	acme              *acmestate.Holder
	issuedTLS         *issuedtls.Issuer

	mu          sync.RWMutex
	sessions    map[int32]*Session  // node ID → session
	connectedAt map[int32]time.Time // node ID → when session was accepted
}

type assetProvider interface {
	OpenAsset(ctx context.Context, assetID int32) (sizeBytes int64, body io.ReadCloser, err error)
}

type networkMapProvider interface {
	SnapshotAndSubscribe(nodeID int32) (*apigen.ClusterNetMap, <-chan *apigen.ClusterNetMap, func())
	// RecordApplied and ForgetNode drive the barrier that holds back retiring a
	// draining placement until every worker has programmed the routing that
	// replaced it.
	RecordApplied(nodeID int32, appliedSequence int64)
	ForgetNode(nodeID int32)
}

func New(store *state.Service, assets assetProvider, githubCredentials githubcredentials.Provider, secretsMgr *secrets.Manager, networkPrefix network.Prefix, networkMaps networkMapProvider, acme *acmestate.Holder, issuedTLS *issuedtls.Issuer) *Handler {
	return &Handler{
		store:             store,
		assets:            assets,
		githubCredentials: githubCredentials,
		secrets:           secretsMgr,
		networkPrefix:     networkPrefix,
		networkMaps:       networkMaps,
		acme:              acme,
		issuedTLS:         issuedTLS,
		sessions:          make(map[int32]*Session),
		connectedAt:       make(map[int32]time.Time),
	}
}

func (p *Handler) GetV1ClusterGithubCredentials(authCtx apigen.Context) (*apigen.GithubCredentials, error) {
	predicate, err := p.requireScheduledInstancePredicate(authCtx)
	if err != nil {
		return nil, err
	}
	if !p.allowedRefs(predicate).usesGithub {
		return nil, clusterForbiddenErr
	}
	creds, err := p.githubCredentials.LoadCredentials(authCtx)
	if err != nil {
		return nil, err
	}
	return &apigen.GithubCredentials{Token: creds.Token, ChangedAt: creds.ChangedAt}, nil
}

func (p *Handler) GetV1ClusterAsset(authCtx apigen.Context, r *http.Request, w http.ResponseWriter) error {
	assetVersionID, err := int32QueryParam(r, "asset_version_id")
	if err != nil {
		return err
	}
	predicate, err := p.requireScheduledInstancePredicate(authCtx)
	if err != nil {
		return err
	}
	if !p.allowedRefs(predicate).assetAllowed(assetVersionID) {
		return clusterForbiddenErr
	}
	sizeBytes, body, err := p.assets.OpenAsset(authCtx, assetVersionID)
	if err != nil {
		return err
	}
	defer body.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(sizeBytes, 10))
	if _, err := io.Copy(w, body); err != nil {
		slog.ErrorContext(authCtx, fmt.Sprintf("stream cluster asset %d failed", assetVersionID), "err", err)
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
	scheduledInstanceIDs map[int32]struct{}
	deploymentIDs        map[int32]struct{}
	secretIDs            map[int32]struct{}
	configIDs            map[int32]struct{}
	assetIDs             map[int32]struct{}
	usesGithub           bool
}

func (p *Handler) allowedRefs(predicate storage.ScheduledInstancePredicate) clusterAllowedRefs {
	snapshot := p.store.FetchScheduledSnapshot(predicate)
	refs := buildAllowedRefs(snapshot)
	var bindings map[string]int32
	if p.acme != nil {
		bindings = acmestate.Bindings(p.acme.Get())
	}
	addIngressCertRefs(refs, snapshot, bindings)
	return refs
}

func addIngressCertRefs(refs clusterAllowedRefs, snapshot []apigen.ScheduledInstanceState, bindings map[string]int32) {
	for _, state := range snapshot {
		for _, route := range state.Config.Spec.Networking.Ingress {
			if route == nil || route.Kind != apigen.IngressKind_INGRESS_KIND_HTTPS || route.HttpsConfig == nil {
				continue
			}
			source := route.HttpsConfig.CertSource
			if source != nil && source.Secret != nil {
				if source.Secret.SecretVersionID > 0 {
					refs.secretIDs[source.Secret.SecretVersionID] = struct{}{}
				}
				continue
			}
			hostname := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(route.Hostname)), ".")
			if id, ok := bindings[hostname]; ok {
				refs.secretIDs[id] = struct{}{}
			}
		}
	}
}

func buildAllowedRefs(snapshot []apigen.ScheduledInstanceState) clusterAllowedRefs {
	refs := clusterAllowedRefs{
		scheduledInstanceIDs: make(map[int32]struct{}),
		deploymentIDs:        make(map[int32]struct{}),
		secretIDs:            make(map[int32]struct{}),
		configIDs:            make(map[int32]struct{}),
		assetIDs:             make(map[int32]struct{}),
	}
	for _, state := range snapshot {
		cfg := state.Config
		if state.Instance.ID != 0 {
			refs.scheduledInstanceIDs[state.Instance.ID] = struct{}{}
		}
		if cfg.ID != 0 {
			refs.deploymentIDs[cfg.ID] = struct{}{}
		}
		container := cfg.Spec.Container()
		if container != nil && container.Source.NixDockerBuild != nil {
			refs.usesGithub = true
		}
		if container == nil {
			continue
		}
		for _, value := range container.Runtime.EnvVars {
			if value == nil {
				continue
			}
			if value.SecretVersionID != nil && *value.SecretVersionID > 0 {
				refs.secretIDs[*value.SecretVersionID] = struct{}{}
			}
			if value.ConfigVersionID != nil && *value.ConfigVersionID > 0 {
				refs.configIDs[*value.ConfigVersionID] = struct{}{}
			}
			if value.AssetVersionID > 0 {
				refs.assetIDs[value.AssetVersionID] = struct{}{}
			}
		}
		for _, mount := range container.Runtime.AssetMounts {
			if mount == nil || mount.AssetVersionID <= 0 {
				continue
			}
			refs.assetIDs[mount.AssetVersionID] = struct{}{}
		}
	}
	return refs
}

func (r clusterAllowedRefs) scheduledInstanceAllowed(id int32) bool {
	_, ok := r.scheduledInstanceIDs[id]
	return ok
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

func (p *Handler) GetV1ClusterSecrets(authCtx apigen.Context, req *apigen.ClusterSecretsRequest) (*apigen.ClusterSecretsResponse, error) {
	if p.secrets == nil {
		return nil, fmt.Errorf("secrets manager is not configured")
	}
	if req == nil || len(req.Ids) == 0 {
		return nil, fmt.Errorf("at least one secret id is required")
	}
	predicate, err := p.requireScheduledInstancePredicate(authCtx)
	if err != nil {
		return nil, err
	}
	if !p.allowedRefs(predicate).allSecretsAllowed(req.Ids) {
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

func (p *Handler) GetV1ClusterConfigs(authCtx apigen.Context, req *apigen.ClusterConfigsRequest) (*apigen.ClusterConfigsResponse, error) {
	if req == nil || len(req.Ids) == 0 {
		return nil, fmt.Errorf("at least one config id is required")
	}
	predicate, err := p.requireScheduledInstancePredicate(authCtx)
	if err != nil {
		return nil, err
	}
	if !p.allowedRefs(predicate).allConfigsAllowed(req.Ids) {
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

func (p *Handler) GetV1ClusterIssuedTls(authCtx apigen.Context, req *apigen.ClusterIssuedTLSRequest) (*apigen.ClusterIssuedTLSResponse, error) {
	if p.issuedTLS == nil {
		return nil, fmt.Errorf("issued TLS is not configured")
	}
	if req == nil || req.DeploymentID <= 0 {
		return nil, fmt.Errorf("deployment_id is required")
	}
	predicate, err := p.requireScheduledInstancePredicate(authCtx)
	if err != nil {
		return nil, err
	}
	for _, instance := range p.store.FetchScheduledSnapshot(predicate) {
		if instance.Config.ID != req.DeploymentID {
			continue
		}
		if instance.Config.Spec.Container() == nil || instance.Config.Spec.Container().Runtime.IssuedTlsMount == nil {
			return nil, clusterForbiddenErr
		}
		return p.issuedTLS.Issue(&instance.Config)
	}
	return nil, clusterForbiddenErr
}

func (p *Handler) GetV1ClusterRenewCertificate(authCtx apigen.Context) (*apigen.ClusterRenewCertificateResponse, error) {
	if p.secrets == nil {
		return nil, fmt.Errorf("secrets manager is not configured")
	}
	machine, err := requireMachine(authCtx)
	if err != nil {
		return nil, err
	}
	if _, err := p.store.NodeIDByIdentifier(machine); err != nil {
		return nil, clusterForbiddenErr
	}
	peerCert := peerCertFromContext(authCtx)
	if peerCert == nil {
		return nil, fmt.Errorf("cluster request missing peer certificate")
	}
	caCert, workerCert, notAfter, err := certu.RenewWorkerCertificate(p.secrets, machine, peerCert.PublicKey)
	if err != nil {
		return nil, err
	}
	slog.InfoContext(authCtx, fmt.Sprintf("renewed worker cluster certificate machine=%s notAfter=%v", machine, notAfter))
	return &apigen.ClusterRenewCertificateResponse{
		CertPem:   workerCert,
		CaCertPem: caCert,
		NotAfter:  notAfter.UnixMilli(),
	}, nil
}

func (p *Handler) PostV1ClusterConnect(authCtx apigen.Context, reqs iter.Seq2[*apigen.MsgToMaster, error]) iter.Seq2[*apigen.MsgToWorker, error] {
	return func(yield func(*apigen.MsgToWorker, error) bool) {
		machine := machineFromContext(authCtx)
		if machine == "" {
			yield(nil, fmt.Errorf("cluster connection missing machine identity"))
			return
		}
		nodeID, err := p.store.NodeIDByIdentifier(machine)
		if err != nil {
			yield(nil, fmt.Errorf("cluster node %q is not registered", machine))
			return
		}
		predicate := scheduledInstancePredicateForNode(nodeID)

		sessCtx, cancel := context.WithCancel(logu.AddKV(authCtx, "node", machine))
		defer cancel()

		sess := newSession(sessCtx, cancel, nodeID, machine, predicate, p.store, p.networkMaps)
		sess.acme = p.acme
		sess.networkPrefix = p.networkPrefix
		p.registerSession(nodeID, machine, sess)
		defer p.unregisterSession(nodeID, machine, sess)

		sess.run(reqs, yield)
	}
}

func (p *Handler) registerSession(nodeID int32, identifier string, sess *Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.sessions[nodeID]; ok {
		old.cancel() // kick the stale session so its handler returns
	}
	p.sessions[nodeID] = sess
	connectedAt := time.Now()
	p.connectedAt[nodeID] = connectedAt
	p.store.SetNodeStatusByIdentifier(identifier, true, connectedAt)
}

func (p *Handler) unregisterSession(nodeID int32, identifier string, expected *Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if current, ok := p.sessions[nodeID]; ok && current == expected {
		delete(p.sessions, nodeID)
		delete(p.connectedAt, nodeID)
		p.store.SetNodeStatusByIdentifier(identifier, false, time.Time{})
	}
}

// RequestLogs's caller must read the returned reader until EOF, or close it to
// abort.
func (p *Handler) RequestLogs(nodeID int32, req *apigen.MsgToWorker) (io.ReadCloser, error) {
	p.mu.RLock()
	sess, ok := p.sessions[nodeID]
	p.mu.RUnlock()
	if !ok {
		return nil, &NodeNotConnectedError{NodeID: nodeID}
	}
	return sess.requestLogs(req)
}

// RequestLogQuery runs a one-shot structured log query on a worker and
// returns its complete response.
func (p *Handler) RequestLogQuery(ctx context.Context, nodeID int32, req *apigen.LogQueryRequest) (*apigen.LogQueryResponse, error) {
	p.mu.RLock()
	sess, ok := p.sessions[nodeID]
	p.mu.RUnlock()
	if !ok {
		return nil, &NodeNotConnectedError{NodeID: nodeID}
	}
	return sess.requestLogQuery(ctx, req)
}

func (p *Handler) ConnectedNodes() map[int32]time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[int32]time.Time, len(p.sessions))
	for nodeID := range p.sessions {
		out[nodeID] = p.connectedAt[nodeID]
	}
	return out
}

// NodeNotConnectedError is returned when a log proxy request targets a node
// that has no active cluster session.
type NodeNotConnectedError struct {
	NodeID int32
}

func (e *NodeNotConnectedError) Error() string {
	return fmt.Sprintf("node not connected: %d", e.NodeID)
}
