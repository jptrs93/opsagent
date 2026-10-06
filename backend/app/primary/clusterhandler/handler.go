// Package clusterhandler implements the primary-side cluster handler. It is the
// server side of the generated OpsagentClusterV1 bidirectional stream: secondaries
// connect over mTLS, the primary sends them the current per-machine deployment
// snapshot, forwards ongoing config updates, and handles incoming status writes
// and log proxy requests. Peer identity is the secondary's client-cert CN, lifted
// into the request context by VerifyClusterPeer.
package clusterhandler

import (
	"context"
	"crypto/x509"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/pki"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
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
	"github.com/jptrs93/opsagent/backend/lib/engine/imageref"
	"github.com/jptrs93/opsagent/backend/lib/enrollment"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/storage"
)

var _ apigen.OpsagentClusterV1Handler = (*Handler)(nil)

type machineCtxKey struct{}

type peerCertCtxKey struct{}
type remoteAddressCtxKey struct{}

var clusterForbiddenErr = apigen.NewApiErr("Forbidden", "cluster_request_not_authorized", http.StatusForbidden)

// VerifyClusterPeer is the MuxConfig.VerifyAuth hook for the cluster mux. The
// secondary is already authenticated by mTLS (the listener requires and verifies a
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
	ctx = context.WithValue(ctx, remoteAddressCtxKey{}, r.RemoteAddr)
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

func scheduledInstancePredicateForNode(nodeID uint64) storage.ScheduledInstancePredicate {
	return func(state apigen.ScheduledInstanceState) bool {
		return state.Instance.NodeID == nodeID
	}
}

func (p *Handler) requireScheduledInstancePredicate(ctx context.Context) (storage.ScheduledInstancePredicate, error) {
	machine, err := requireMachine(ctx)
	if err != nil {
		return nil, err
	}
	nodeID, err := nodes.MemberNodeIDByIdentifier(p.store.Queries(), machine)
	if err != nil {
		return nil, clusterForbiddenErr
	}
	return scheduledInstancePredicateForNode(nodeID), nil
}

// Handler manages secondary sessions and forwards state between the local store
// and connected secondaries. It implements apigen.OpsagentClusterV1Handler; the
// generated mux invokes PostV1ClusterConnect once per secondary connection.
type Handler struct {
	store             *state.Service
	assets            assetProvider
	githubCredentials githubcredentials.Provider
	secrets           *secrets.Manager
	networkPrefix     network.Prefix
	networkMaps       networkMapProvider
	acme              *acmestate.Holder
	nixStores         nixStoreResetProvider
	issuedTLS         *pki.Issuer

	mu          sync.RWMutex
	sessions    map[uint64]*Session  // node ID → session
	connectedAt map[uint64]time.Time // node ID → when session was accepted
}

type assetProvider interface {
	OpenAsset(ctx context.Context, ref apigen.ValueRef) (sizeBytes int64, body io.ReadCloser, err error)
}

type nixStoreResetProvider interface {
	SnapshotAndSubscribe() (*apigen.NixStoreResets, <-chan *apigen.NixStoreResets, func())
}

type networkMapProvider interface {
	SnapshotAndSubscribe(nodeID uint64) (*apigen.ClusterNetMap, <-chan *apigen.ClusterNetMap, func())
	// RecordApplied and ForgetNode drive the barrier that holds back retiring a
	// draining placement until every secondary has programmed the routing that
	// replaced it.
	RecordApplied(nodeID uint64, appliedSequence int64)
	ForgetNode(nodeID uint64)
}

func New(store *state.Service, assets assetProvider, githubCredentials githubcredentials.Provider, secretsMgr *secrets.Manager, networkPrefix network.Prefix, networkMaps networkMapProvider, acme *acmestate.Holder, nixStores nixStoreResetProvider, issuedTLS *pki.Issuer) *Handler {
	return &Handler{
		store:             store,
		assets:            assets,
		githubCredentials: githubCredentials,
		secrets:           secretsMgr,
		networkPrefix:     networkPrefix,
		networkMaps:       networkMaps,
		acme:              acme,
		nixStores:         nixStores,
		issuedTLS:         issuedTLS,
		sessions:          make(map[uint64]*Session),
		connectedAt:       make(map[uint64]time.Time),
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
	assetID, err := uintQueryParam(r, "asset_id", 64)
	if err != nil {
		return err
	}
	version, err := uintQueryParam(r, "version", 32)
	if err != nil {
		return err
	}
	ref := apigen.ValueRef{ID: assetID, Version: uint32(version)}
	predicate, err := p.requireScheduledInstancePredicate(authCtx)
	if err != nil {
		return err
	}
	if !p.allowedRefs(predicate).assetAllowed(ref) {
		return clusterForbiddenErr
	}
	sizeBytes, body, err := p.assets.OpenAsset(authCtx, ref)
	if err != nil {
		return err
	}
	defer body.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(sizeBytes, 10))
	if _, err := io.Copy(w, body); err != nil {
		slog.ErrorContext(authCtx, fmt.Sprintf("stream cluster asset %s failed", ref), "err", err)
	}
	return nil
}

func uintQueryParam(r *http.Request, name string, bits int) (uint64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	value, err := strconv.ParseUint(raw, 10, bits)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("%s must be a positive uint%d", name, bits)
	}
	return value, nil
}

type clusterAllowedRefs struct {
	scheduledInstanceIDs map[uint64]struct{}
	deploymentIDs        map[uint64]struct{}
	secrets              map[apigen.ValueRef]struct{}
	configs              map[apigen.ValueRef]struct{}
	assets               map[apigen.ValueRef]struct{}
	usesGithub           bool
}

func (p *Handler) allowedRefs(predicate storage.ScheduledInstancePredicate) clusterAllowedRefs {
	snapshot := p.store.FetchScheduledSnapshot(predicate)
	refs := buildAllowedRefs(snapshot)
	var bindings map[string]apigen.ValueRef
	if p.acme != nil {
		bindings = acmestate.Bindings(p.acme.Get())
	}
	addIngressCertRefs(refs, snapshot, bindings)
	return refs
}

func addIngressCertRefs(refs clusterAllowedRefs, snapshot []apigen.ScheduledInstanceState, bindings map[string]apigen.ValueRef) {
	for _, state := range snapshot {
		for _, route := range state.Config.Deployment.Spec.Networking.Ingress {
			https := route.Config.Value.Https
			if https == nil {
				continue
			}
			if source := https.CertSource; source.Present && source.Value.Value.Secret != nil {
				if ref := source.Value.Value.Secret.Secret.Ref(); ref.Valid() {
					refs.secrets[ref] = struct{}{}
				}
				continue
			}
			hostname := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(route.Hostname)), ".")
			if ref, ok := bindings[hostname]; ok {
				refs.secrets[ref] = struct{}{}
			}
		}
	}
}

func buildAllowedRefs(snapshot []apigen.ScheduledInstanceState) clusterAllowedRefs {
	refs := clusterAllowedRefs{
		scheduledInstanceIDs: make(map[uint64]struct{}),
		deploymentIDs:        make(map[uint64]struct{}),
		secrets:              make(map[apigen.ValueRef]struct{}),
		configs:              make(map[apigen.ValueRef]struct{}),
		assets:               make(map[apigen.ValueRef]struct{}),
	}
	for _, state := range snapshot {
		cfg := state.Config
		if state.Instance.ID != 0 {
			refs.scheduledInstanceIDs[state.Instance.ID] = struct{}{}
		}
		if cfg.Deployment.ID != 0 {
			refs.deploymentIDs[cfg.Deployment.ID] = struct{}{}
		}
		container := cfg.Deployment.Spec.Container()
		if container != nil && container.Source.Value.NixImageBuild != nil {
			refs.usesGithub = true
		}
		if container == nil {
			continue
		}
		if image := container.Source.Value.RemoteImage; image != nil {
			if ref, err := imageref.Parse(image.Image); err == nil && strings.EqualFold(ref.Registry, "ghcr.io") {
				refs.usesGithub = true
			}
		}
		for _, value := range container.Runtime.EnvVars {
			switch v := value.Value; {
			case v.Secret != nil:
				if ref := v.Secret.Secret.Ref(); ref.Valid() {
					refs.secrets[ref] = struct{}{}
				}
			case v.Config != nil:
				if ref := v.Config.Config.Ref(); ref.Valid() {
					refs.configs[ref] = struct{}{}
				}
			case v.Asset != nil:
				if ref := v.Asset.Asset.Ref(); ref.Valid() {
					refs.assets[ref] = struct{}{}
				}
			}
		}
		for _, mount := range container.Runtime.AssetMounts {
			if ref := mount.Asset.Ref(); ref.Valid() {
				refs.assets[ref] = struct{}{}
			}
		}
	}
	return refs
}

func (r clusterAllowedRefs) scheduledInstanceAllowed(id uint64) bool {
	_, ok := r.scheduledInstanceIDs[id]
	return ok
}

func (r clusterAllowedRefs) deploymentAllowed(id uint64) bool {
	_, ok := r.deploymentIDs[id]
	return ok
}

func (r clusterAllowedRefs) allSecretsAllowed(refs []apigen.ValueRef) bool {
	return allValueRefsAllowed(refs, r.secrets)
}

func (r clusterAllowedRefs) allConfigsAllowed(refs []apigen.ValueRef) bool {
	return allValueRefsAllowed(refs, r.configs)
}

func (r clusterAllowedRefs) assetAllowed(ref apigen.ValueRef) bool {
	_, ok := r.assets[ref]
	return ok
}

func allValueRefsAllowed(refs []apigen.ValueRef, allowed map[apigen.ValueRef]struct{}) bool {
	for _, ref := range refs {
		if !ref.Valid() {
			return false
		}
		if _, ok := allowed[ref]; !ok {
			return false
		}
	}
	return true
}

func secretValueRefs(refs []apigen.SecretRef) []apigen.ValueRef {
	out := make([]apigen.ValueRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.Ref())
	}
	return out
}

func configValueRefs(refs []apigen.ConfigRef) []apigen.ValueRef {
	out := make([]apigen.ValueRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.Ref())
	}
	return out
}

func (p *Handler) GetV1ClusterSecrets(authCtx apigen.Context, req *apigen.ClusterSecretsRequest) (*apigen.ClusterSecretsResponse, error) {
	if p.secrets == nil {
		return nil, fmt.Errorf("secrets manager is not configured")
	}
	if req == nil || len(req.Refs) == 0 {
		return nil, fmt.Errorf("at least one secret ref is required")
	}
	predicate, err := p.requireScheduledInstancePredicate(authCtx)
	if err != nil {
		return nil, err
	}
	refs := secretValueRefs(req.Refs)
	if !p.allowedRefs(predicate).allSecretsAllowed(refs) {
		return nil, clusterForbiddenErr
	}
	values, err := p.secrets.ResolveMany(refs)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.ClusterSecretValue, 0, len(values))
	for ref, value := range values {
		items = append(items, apigen.ClusterSecretValue{Ref: ref.Secret(), Value: []byte(value)})
	}
	return &apigen.ClusterSecretsResponse{Items: items}, nil
}

func (p *Handler) GetV1ClusterConfigs(authCtx apigen.Context, req *apigen.ClusterConfigsRequest) (*apigen.ClusterConfigsResponse, error) {
	if req == nil || len(req.Refs) == 0 {
		return nil, fmt.Errorf("at least one config ref is required")
	}
	predicate, err := p.requireScheduledInstancePredicate(authCtx)
	if err != nil {
		return nil, err
	}
	refs := configValueRefs(req.Refs)
	if !p.allowedRefs(predicate).allConfigsAllowed(refs) {
		return nil, clusterForbiddenErr
	}
	values, err := values.ResolveConfigs(p.store.Queries(), refs)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.ClusterConfigValue, 0, len(values))
	for ref, value := range values {
		items = append(items, apigen.ClusterConfigValue{Ref: ref.Config(), Value: value})
	}
	return &apigen.ClusterConfigsResponse{Items: items}, nil
}

func (p *Handler) GetV1ClusterIssuedTls(authCtx apigen.Context, req *apigen.ClusterIssuedTLSRequest) (*apigen.ClusterIssuedTLSResponse, error) {
	if p.issuedTLS == nil {
		return nil, fmt.Errorf("issued TLS is not configured")
	}
	if req == nil || req.DeploymentID == 0 {
		return nil, fmt.Errorf("deployment_id is required")
	}
	predicate, err := p.requireScheduledInstancePredicate(authCtx)
	if err != nil {
		return nil, err
	}
	for _, instance := range p.store.FetchScheduledSnapshot(predicate) {
		if instance.Config.Deployment.ID != req.DeploymentID {
			continue
		}
		if container := instance.Config.Deployment.Spec.Container(); container == nil || !container.Runtime.IssuedTlsMount.Present {
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
	if _, err := nodes.MemberNodeIDByIdentifier(p.store.Queries(), machine); err != nil {
		return nil, clusterForbiddenErr
	}
	peerCert := peerCertFromContext(authCtx)
	if peerCert == nil {
		return nil, fmt.Errorf("cluster request missing peer certificate")
	}
	caCert, secondaryCert, notAfter, err := pki.RenewSecondaryCertificate(p.secrets, machine, peerCert.PublicKey)
	if err != nil {
		return nil, err
	}
	slog.InfoContext(authCtx, fmt.Sprintf("renewed secondary cluster certificate machine=%s notAfter=%v", machine, notAfter))
	return &apigen.ClusterRenewCertificateResponse{
		CertPem:   secondaryCert,
		CaCertPem: caCert,
		NotAfter:  notAfter.UnixMilli(),
	}, nil
}

func (p *Handler) PostV1ClusterConnect(authCtx apigen.Context, reqs iter.Seq2[*apigen.MsgToPrimary, error]) iter.Seq2[*apigen.MsgToSecondary, error] {
	return func(yield func(*apigen.MsgToSecondary, error) bool) {
		machine := machineFromContext(authCtx)
		if machine == "" {
			yield(nil, fmt.Errorf("cluster connection missing machine identity"))
			return
		}
		nodeID, err := nodes.MemberNodeIDByIdentifier(p.store.Queries(), machine)
		if err != nil {
			if nodes.IsEvictedIdentifier(p.store.Queries(), machine) {
				slog.WarnContext(authCtx, fmt.Sprintf("rejected cluster connection from evicted node machine=%s", machine))
				yield(nil, enrollment.NodeEvictedErr)
				return
			}
			yield(nil, fmt.Errorf("cluster node %q is not registered", machine))
			return
		}
		predicate := scheduledInstancePredicateForNode(nodeID)

		sessCtx, cancel := context.WithCancel(logu.AddKV(authCtx, "node", machine))
		defer cancel()

		sess := newSession(sessCtx, cancel, nodeID, machine, predicate, p.store, p.networkMaps)
		sess.acme = p.acme
		sess.nixStores = p.nixStores
		sess.networkPrefix = p.networkPrefix
		p.registerSession(nodeID, machine, sess)
		defer p.unregisterSession(nodeID, machine, sess)

		sess.run(reqs, yield)
	}
}

func (p *Handler) RunEvictionWatch(ctx context.Context) {
	updates, unsubscribe := p.store.SubscribeUpdates()
	defer func() { unsubscribe() }()
	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-updates:
			if !ok {
				unsubscribe()
				updates, unsubscribe = p.store.SubscribeUpdates()
				continue
			}
			for _, m := range update.Mutations {
				if e := m.Entity(); e != nil && e.Value.Node != nil && e.Value.Node.Status == apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_EVICTED {
					p.evictSession(m.EntityID())
				}
			}
		}
	}
}

func (p *Handler) evictSession(nodeID uint64) {
	p.mu.RLock()
	sess, ok := p.sessions[nodeID]
	p.mu.RUnlock()
	if !ok {
		return
	}
	slog.WarnContext(sess.sessCtx, fmt.Sprintf("ending cluster session of evicted node=%d", nodeID))
	sess.evict()
}

func (p *Handler) registerSession(nodeID uint64, identifier string, sess *Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.sessions[nodeID]; ok {
		old.cancel() // kick the stale session so its handler returns
	}
	p.sessions[nodeID] = sess
	connectedAt := time.Now()
	p.connectedAt[nodeID] = connectedAt
	nodes.SetNodeStatusByIdentifier(p.store, identifier, true, connectedAt)
}

func (p *Handler) unregisterSession(nodeID uint64, identifier string, expected *Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if current, ok := p.sessions[nodeID]; ok && current == expected {
		delete(p.sessions, nodeID)
		delete(p.connectedAt, nodeID)
		nodes.SetNodeStatusByIdentifier(p.store, identifier, false, time.Time{})
	}
}

// RequestLogs's caller must read the returned reader until EOF, or close it to
// abort.
func (p *Handler) RequestLogs(nodeID uint64, req *apigen.MsgToSecondary) (io.ReadCloser, error) {
	p.mu.RLock()
	sess, ok := p.sessions[nodeID]
	p.mu.RUnlock()
	if !ok {
		return nil, &NodeNotConnectedError{NodeID: nodeID}
	}
	return sess.requestLogs(req)
}

// RequestLogQuery runs a one-shot structured log query on a secondary and
// returns its complete response.
func (p *Handler) RequestLogQuery(ctx context.Context, nodeID uint64, req *apigen.LogQueryRequest) (*apigen.LogQueryResponse, error) {
	p.mu.RLock()
	sess, ok := p.sessions[nodeID]
	p.mu.RUnlock()
	if !ok {
		return nil, &NodeNotConnectedError{NodeID: nodeID}
	}
	return sess.requestLogQuery(ctx, req)
}

// RequestMetricsQuery runs a one-shot metrics rollup on a secondary.
func (p *Handler) RequestMetricsQuery(ctx context.Context, nodeID uint64, req *apigen.MetricsQueryRequest) (*apigen.MetricsQueryResponse, error) {
	p.mu.RLock()
	sess, ok := p.sessions[nodeID]
	p.mu.RUnlock()
	if !ok {
		return nil, &NodeNotConnectedError{NodeID: nodeID}
	}
	return sess.requestMetricsQuery(ctx, req)
}

// RequestMetricsLatest fetches the latest sample per running container from
// a secondary.
func (p *Handler) RequestMetricsLatest(ctx context.Context, nodeID uint64) (*apigen.MetricsLatestResponse, error) {
	p.mu.RLock()
	sess, ok := p.sessions[nodeID]
	p.mu.RUnlock()
	if !ok {
		return nil, &NodeNotConnectedError{NodeID: nodeID}
	}
	return sess.requestMetricsLatest(ctx)
}

func (p *Handler) ConnectedNodes() map[uint64]time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[uint64]time.Time, len(p.sessions))
	for nodeID := range p.sessions {
		out[nodeID] = p.connectedAt[nodeID]
	}
	return out
}

// NodeNotConnectedError is returned when a log proxy request targets a node
// that has no active cluster session.
type NodeNotConnectedError struct {
	NodeID uint64
}

func (e *NodeNotConnectedError) Error() string {
	return fmt.Sprintf("node not connected: %d", e.NodeID)
}
